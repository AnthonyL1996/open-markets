package store

import (
	"testing"

	"openmarkets/server/internal/money"
)

// tradeLeague creates a store with accounts A and B in one league, a fixed pricer (Oil 320, Coal 250),
// and returns the store and the two account ids and league id.
func tradeLeague(t *testing.T) (m *Memory, a, b, lid string) {
	t.Helper()
	m = NewMemory("")
	m.SetPricer(func(_ string, c string) (int64, bool) {
		v, ok := map[string]int64{"Oil": 320, "Coal": 250}[c]
		return v, ok
	})
	accA, _, err := m.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	accB, _, err := m.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	lg, err := m.CreateLeague(accA.ID, "L")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.JoinLeague(accB.ID, lg.ID); err != nil {
		t.Fatal(err)
	}
	return m, accA.ID, accB.ID, lg.ID
}

// A basket where offerer A nets +62000 (Oil give 32000, Coal take -20000, gold take +50000): B pays A.
func basket(a, b, lid string, installments int) Trade {
	return Trade{
		LeagueID: lid, OfferedBy: a, Counterparty: b, DefaultRateBps: 2000, Installments: installments,
		Items: []LineItem{
			{Kind: LineCommodity, Commodity: "Oil", QtyFixed: 100 * money.QtyScale, Dir: DirGive},
			{Kind: LineCommodity, Commodity: "Coal", QtyFixed: 80 * money.QtyScale, Dir: DirTake},
			{Kind: LineGold, GoldCents: 50000, Dir: DirTake},
		},
	}
}

func TestCreateTrade_FreezesAtAcceptNotCreate(t *testing.T) {
	m, a, b, lid := tradeLeague(t)
	tr, err := m.CreateTrade(basket(a, b, lid, 1))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tr.Status != TradeOffered {
		t.Fatalf("status %s, want offered", tr.Status)
	}
	for _, li := range tr.Items { // not frozen yet
		if li.ValueCentsAtAccept != 0 {
			t.Errorf("value frozen at create: %d", li.ValueCentsAtAccept)
		}
	}
	// non-member can't be a counterparty
	if _, err := m.CreateTrade(basket(a, "ghost", lid, 1)); err != ErrNotFound {
		t.Errorf("ghost counterparty: got %v, want ErrNotFound", err)
	}
	// below the default-rate floor
	low := basket(a, b, lid, 1)
	low.DefaultRateBps = 500
	if _, err := m.CreateTrade(low); err != ErrConflict {
		t.Errorf("below floor: got %v, want ErrConflict", err)
	}
}

func TestAccept_FreezesValues_AndSettleEmitsNetEvent(t *testing.T) {
	m, a, b, lid := tradeLeague(t)
	tr, _ := m.CreateTrade(basket(a, b, lid, 1))

	// only the counterparty may accept
	if _, err := m.SetTradeStatus(a, tr.ID, "accept"); err != ErrConflict {
		t.Fatalf("offerer accept: got %v, want ErrConflict", err)
	}
	tr, err := m.SetTradeStatus(b, tr.ID, "accept")
	if err != nil || tr.Status != TradeActive {
		t.Fatalf("accept: %v status=%s", err, tr.Status)
	}
	if tr.OffererNetCents() != 62000 {
		t.Fatalf("frozen net = %d, want 62000", tr.OffererNetCents())
	}

	// B is the net payer; A (receiver) cannot settle the nonzero installment.
	if _, _, err := m.SettleTradeInstallment(a, tr.ID); err != ErrConflict {
		t.Fatalf("receiver settle: got %v, want ErrConflict", err)
	}
	tr, ev, err := m.SettleTradeInstallment(b, tr.ID)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if ev.PayerID != b || ev.ReceiverID != a || ev.Cents != 62000 {
		t.Errorf("event = %+v, want B->A 62000", ev)
	}
	if tr.Status != TradeCompleted {
		t.Errorf("status %s, want completed", tr.Status)
	}
	// The event is B→A; scoped to A (the receiver) it appears, with latestSeq == its seq.
	evs, latest, _ := m.SettlementsForAccount(lid, a, 0)
	if len(evs) != 1 || evs[0].Seq != 1 || latest != 1 {
		t.Errorf("SettlementsForAccount = %+v latest=%d, want one event seq 1 / latest 1", evs, latest)
	}
	// Scoped to a non-party it is empty (privacy).
	if other, _, _ := m.SettlementsForAccount(lid, "ghost", 0); len(other) != 0 {
		t.Errorf("non-party feed = %+v, want empty", other)
	}
}

func TestMiss_AutoBondsTheShortfall(t *testing.T) {
	m, a, b, lid := tradeLeague(t)
	tr, _ := m.CreateTrade(basket(a, b, lid, 1))
	tr, _ = m.SetTradeStatus(b, tr.ID, "accept")

	tr2, bond, err := m.MissTradeInstallment(tr.ID)
	if err != nil {
		t.Fatalf("miss: %v", err)
	}
	if tr2.Status != TradeCompleted {
		t.Errorf("trade status %s, want completed (obligation moved to bond)", tr2.Status)
	}
	// debtor is the net payer (B) who failed; creditor is the receiver (A).
	if bond.DebtorID != b || bond.CreditorID != a {
		t.Errorf("bond parties = debtor %s creditor %s, want %s/%s", bond.DebtorID, bond.CreditorID, b, a)
	}
	if bond.PrincipalCents != 62000 || bond.Status != BondActive || bond.Origin != "trade:"+tr.ID {
		t.Errorf("bond = %+v", bond)
	}
	if bond.TotalDueCents != 74400 { // 62000 + 20%
		t.Errorf("bond total = %d, want 74400", bond.TotalDueCents)
	}
}

func TestBond_SettleAndDefault(t *testing.T) {
	m, a, b, lid := tradeLeague(t)
	tr, _ := m.CreateTrade(basket(a, b, lid, 1))
	tr, _ = m.SetTradeStatus(b, tr.ID, "accept")
	_, bond, _ := m.MissTradeInstallment(tr.ID)

	// debtor B repays one installment → event B->A, delinquency (if any) cured.
	bd, ev, err := m.SettleBondInstallment(b, bond.ID)
	if err != nil {
		t.Fatalf("bond settle: %v", err)
	}
	if ev.PayerID != b || ev.ReceiverID != a || ev.Cents <= 0 {
		t.Errorf("bond event = %+v", ev)
	}
	if bd.Settled != 1 {
		t.Errorf("settled = %d, want 1", bd.Settled)
	}
	// only the debtor may settle
	if _, _, err := m.SettleBondInstallment(a, bond.ID); err != ErrNotFound {
		t.Errorf("non-debtor settle: got %v, want ErrNotFound", err)
	}

	// Two consecutive misses → terminal default (balance frozen).
	if _, def, _ := m.MissBondInstallment(bond.ID); def {
		t.Errorf("first miss should not default")
	}
	bd2, def, _ := m.MissBondInstallment(bond.ID)
	if !def || bd2.Status != BondDefaultedReceivable {
		t.Errorf("second miss: def=%v status=%s, want true/defaultedReceivable", def, bd2.Status)
	}
	_ = lid
}

func TestSettle_MultiInstallmentSchedule(t *testing.T) {
	m, a, b, lid := tradeLeague(t)
	tr, _ := m.CreateTrade(basket(a, b, lid, 4)) // net 62000 over 4 → 15500 each
	tr, _ = m.SetTradeStatus(b, tr.ID, "accept")
	var total int64
	for i := 0; i < 4; i++ {
		var ev SettlementEvent
		var err error
		tr, ev, err = m.SettleTradeInstallment(b, tr.ID)
		if err != nil {
			t.Fatalf("installment %d: %v", i, err)
		}
		total += ev.Cents
	}
	if total != 62000 {
		t.Errorf("installments summed to %d, want 62000", total)
	}
	if tr.Status != TradeCompleted {
		t.Errorf("status %s, want completed", tr.Status)
	}
	_ = lid
}
