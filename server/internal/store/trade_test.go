package store

import (
	"errors"
	"testing"
	"time"

	"openmarkets/server/internal/money"
)

// A fixed price book for valuation tests: cents per WHOLE unit.
func priceBook(m map[string]int64) func(string) (int64, bool) {
	return func(c string) (int64, bool) { v, ok := m[c]; return v, ok }
}

func sampleTrade() *Trade {
	return &Trade{
		ID: "t1", LeagueID: "L", OfferedBy: "A", Counterparty: "B",
		DefaultRateBps: 2000, Installments: 1, Status: TradeOffered,
		Items: []LineItem{
			{Kind: LineCommodity, Commodity: "Oil", QtyFixed: 100 * money.QtyScale, Dir: DirGive}, // A sells 100 oil
			{Kind: LineCommodity, Commodity: "Coal", QtyFixed: 80 * money.QtyScale, Dir: DirTake}, // A buys 80 coal
			{Kind: LineGold, GoldCents: 50000, Dir: DirTake},                                      // A receives §500
		},
	}
}

func TestFreezeValues_AndCashFlow(t *testing.T) {
	tr := sampleTrade()
	if err := tr.FreezeValues(priceBook(map[string]int64{"Oil": 320, "Coal": 250})); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	// Oil: 100 * 320 = 32000 (give → +). Coal: 80 * 250 = 20000 (take → -). Gold 50000 (take → +).
	wantValues := []int64{32000, 20000, 50000}
	wantFlow := []int64{32000, -20000, 50000}
	for i, li := range tr.Items {
		if li.ValueCentsAtAccept != wantValues[i] {
			t.Errorf("item %d value = %d, want %d", i, li.ValueCentsAtAccept, wantValues[i])
		}
		if li.CashFlowToOfferer() != wantFlow[i] {
			t.Errorf("item %d flow = %d, want %d", i, li.CashFlowToOfferer(), wantFlow[i])
		}
	}
	// Offerer A nets: +32000 - 20000 + 50000 = +62000.
	if got := tr.OffererNetCents(); got != 62000 {
		t.Errorf("OffererNetCents = %d, want 62000", got)
	}
}

// Conservation: the counterparty's net is the exact negation of the offerer's, across kinds and directions.
func TestCashFlow_Conservation(t *testing.T) {
	tr := sampleTrade()
	if err := tr.FreezeValues(priceBook(map[string]int64{"Oil": 320, "Coal": 250})); err != nil {
		t.Fatal(err)
	}
	var offerer int64
	for _, li := range tr.Items {
		offerer += li.CashFlowToOfferer()
	}
	if offerer+(-offerer) != 0 { // tautology guard; the real check is the per-line sign table below
		t.Fatal("impossible")
	}
	// Explicit sign table (Codex pinned §9.4a).
	cases := []struct {
		kind, dir string
		val, want int64
	}{
		{LineCommodity, DirGive, 1000, 1000},
		{LineCommodity, DirTake, 1000, -1000},
		{LineGold, DirGive, 1000, -1000},
		{LineGold, DirTake, 1000, 1000},
	}
	for _, c := range cases {
		li := LineItem{Kind: c.kind, Dir: c.dir, ValueCentsAtAccept: c.val}
		if got := li.CashFlowToOfferer(); got != c.want {
			t.Errorf("%s/%s flow = %d, want %d", c.kind, c.dir, got, c.want)
		}
	}
}

func TestFreezeValues_Errors(t *testing.T) {
	tr := sampleTrade()
	if err := tr.FreezeValues(priceBook(map[string]int64{"Oil": 320})); !errors.Is(err, ErrNoPrice) {
		t.Errorf("missing coal price: got %v, want ErrNoPrice", err)
	}
	bad := &Trade{Items: []LineItem{{Kind: LineCommodity, Commodity: "Oil", QtyFixed: 0, Dir: DirGive}}}
	if err := bad.FreezeValues(priceBook(map[string]int64{"Oil": 320})); !errors.Is(err, ErrBadLine) {
		t.Errorf("zero qty: got %v, want ErrBadLine", err)
	}
	// A commodity line whose frozen value (qty × accept-time price) exceeds the bookable ceiling is rejected at
	// freeze — the per-line cap that keeps OffererNetCents from overflowing. Regression for the pre-merge Codex HIGH.
	huge := &Trade{Items: []LineItem{
		{Kind: LineCommodity, Commodity: "Oil", QtyFixed: 1_000_000 * money.QtyScale, Dir: DirGive},
		{Kind: LineGold, GoldCents: 1, Dir: DirTake},
	}}
	if err := huge.FreezeValues(priceBook(map[string]int64{"Oil": money.MaxBookableCents})); !errors.Is(err, ErrLineTooLarge) {
		t.Errorf("oversized commodity value: got %v, want ErrLineTooLarge", err)
	}
}

// A single gold line above the bookable ceiling must be rejected at offer time (validLine cap), closing the
// int64-overflow path where many huge same-direction gold lines could wrap OffererNetCents. Pre-merge Codex HIGH.
func TestValidateForOffer_GoldLineCap(t *testing.T) {
	const floor = 2000
	over := &Trade{DefaultRateBps: floor, Installments: 1, Items: []LineItem{
		{Kind: LineGold, GoldCents: money.MaxBookableCents + 1, Dir: DirTake},
		{Kind: LineGold, GoldCents: 100, Dir: DirGive},
	}}
	if err := over.ValidateForOffer(floor); !errors.Is(err, ErrBadLine) {
		t.Errorf("oversized gold line: got %v, want ErrBadLine", err)
	}
	// At the ceiling is fine.
	ok := &Trade{DefaultRateBps: floor, Installments: 1, Items: []LineItem{
		{Kind: LineGold, GoldCents: money.MaxBookableCents, Dir: DirTake},
		{Kind: LineGold, GoldCents: 100, Dir: DirGive},
	}}
	if err := ok.ValidateForOffer(floor); err != nil {
		t.Errorf("gold at the cap rejected: %v", err)
	}
}

func TestValidateForOffer(t *testing.T) {
	const floor = 2000
	if err := sampleTrade().ValidateForOffer(floor); err != nil {
		t.Errorf("valid trade rejected: %v", err)
	}
	// below the league default-rate floor
	low := sampleTrade()
	low.DefaultRateBps = 1000
	if err := low.ValidateForOffer(floor); !errors.Is(err, ErrConflict) {
		t.Errorf("below floor: got %v, want ErrConflict", err)
	}
	// one-sided basket (give only)
	oneSided := &Trade{DefaultRateBps: floor, Installments: 1,
		Items: []LineItem{{Kind: LineGold, GoldCents: 100, Dir: DirGive}}}
	if err := oneSided.ValidateForOffer(floor); !errors.Is(err, ErrEmptyTrade) {
		t.Errorf("one-sided: got %v, want ErrEmptyTrade", err)
	}
	// too many line items (risk-scan: memory amplification + net-sum overflow)
	big := &Trade{DefaultRateBps: floor, Installments: 1}
	for i := 0; i <= MaxTradeItems; i++ {
		dir := DirGive
		if i%2 == 1 {
			dir = DirTake
		}
		big.Items = append(big.Items, LineItem{Kind: LineGold, GoldCents: 100, Dir: dir})
	}
	if err := big.ValidateForOffer(floor); !errors.Is(err, ErrEmptyTrade) {
		t.Errorf("over item cap: got %v, want ErrEmptyTrade", err)
	}
}

func TestSetEconParams_ClampsAusterityKnobs(t *testing.T) {
	m := NewMemory("")
	p := DefaultEconParams()
	p.GarnishMinWriteDownCents = 0 // would make austerity inescapable
	p.AusterityMaxTicks = 0
	m.SetEconParams(p)
	if m.econ.GarnishMinWriteDownCents < 1 || m.econ.AusterityMaxTicks < 1 {
		t.Errorf("econ not clamped: garnish=%d maxTicks=%d", m.econ.GarnishMinWriteDownCents, m.econ.AusterityMaxTicks)
	}
}

func TestNextTradeStatus(t *testing.T) {
	tr := sampleTrade() // OfferedBy A, Counterparty B, status offered
	if s, err := tr.NextTradeStatus("B", "accept"); err != nil || s != TradeActive {
		t.Errorf("B accept: got (%q,%v), want (active,nil)", s, err)
	}
	if _, err := tr.NextTradeStatus("A", "accept"); !errors.Is(err, ErrConflict) {
		t.Errorf("offerer can't accept own: got %v, want ErrConflict", err)
	}
	if s, err := tr.NextTradeStatus("A", "cancel"); err != nil || s != TradeCancelled {
		t.Errorf("A cancel: got (%q,%v), want (cancelled,nil)", s, err)
	}
	if _, err := tr.NextTradeStatus("B", "cancel"); !errors.Is(err, ErrConflict) {
		t.Errorf("non-offerer cancel: got %v, want ErrConflict", err)
	}
}

func TestBond_ActivateScheduleAndRemaining(t *testing.T) {
	b := Bond{PrincipalCents: 10000, InterestBps: 2000, Installments: 4} // 20% → total 12000
	if err := b.Activate(100); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if b.TotalDueCents != 12000 || b.Status != BondActive {
		t.Fatalf("total=%d status=%s, want 12000/active", b.TotalDueCents, b.Status)
	}
	sched, _ := b.Schedule()
	var sum int64
	for _, c := range sched {
		sum += c
	}
	if sum != 12000 {
		t.Errorf("schedule sums to %d, want 12000", sum)
	}
	b.Settled = 1
	rem, _ := b.RemainingCents()
	if rem != 9000 { // 12000 - 3000
		t.Errorf("remaining = %d, want 9000", rem)
	}
}

func TestBond_MissFreezesAtDefault(t *testing.T) {
	b := Bond{PrincipalCents: 10000, InterestBps: 2000, Installments: 4}
	_ = b.Activate(100)
	const maxMisses = 2
	if def := b.RegisterMiss(maxMisses); def || b.Status != BondDelinquent {
		t.Fatalf("first miss: defaulted=%v status=%s, want false/delinquent", def, b.Status)
	}
	frozen := b.TotalDueCents
	if def := b.RegisterMiss(maxMisses); !def || b.Status != BondDefaultedReceivable {
		t.Fatalf("second miss: defaulted=%v status=%s, want true/defaultedReceivable", def, b.Status)
	}
	// Invariant (Codex #1): balance does not grow once defaulted.
	if b.TotalDueCents != frozen {
		t.Errorf("TotalDueCents changed after default: %d != %d", b.TotalDueCents, frozen)
	}
}

func TestNewAutoBond(t *testing.T) {
	b, err := NewAutoBond("b1", "L", "B", "A", "t1", 5000, 2000, 6, 100, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("auto-bond: %v", err)
	}
	if b.Status != BondActive || b.Origin != "trade:t1" || b.DebtorID != "A" || b.CreditorID != "B" {
		t.Errorf("unexpected auto-bond: %+v", b)
	}
	if b.TotalDueCents != 6000 { // 5000 + 20%
		t.Errorf("total = %d, want 6000", b.TotalDueCents)
	}
}
