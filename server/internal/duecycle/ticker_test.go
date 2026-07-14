package duecycle

import (
	"sync"
	"testing"
	"time"

	"openmarkets/server/internal/money"
	"openmarkets/server/internal/store"
)

// clock is a settable time source so the store's timestamps (AcceptedDay, Bond.Created) share the same time
// base as the ticker's synthetic `now`, making the due-clock deterministic.
type clock struct {
	mu  sync.Mutex
	sec int64
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return time.Unix(c.sec, 0).UTC() }
func (c *clock) set(sec int64)  { c.mu.Lock(); c.sec = sec; c.mu.Unlock() }

const base = int64(10000)

// activeTrade sets up a league (A offerer, B counterparty) and an accepted 1-installment trade where B owes A,
// accepted at t=base. Returns the store, the clock, the trade, and the ids.
func activeTrade(t *testing.T) (m *store.Memory, ck *clock, tr store.Trade, a, b, lid string) {
	t.Helper()
	m = store.NewMemory("")
	ck = &clock{sec: base}
	m.SetClock(ck.now)
	m.SetPricer(func(_ string, c string) (int64, bool) {
		v, ok := map[string]int64{"Oil": 400, "Coal": 150}[c]
		return v, ok
	})
	accA, _, _ := m.CreateAccount()
	accB, _, _ := m.CreateAccount()
	lg, _ := m.CreateLeague(accA.ID, "L")
	if err := m.JoinLeague(accB.ID, lg.ID); err != nil {
		t.Fatal(err)
	}
	created, err := m.CreateTrade(store.Trade{
		LeagueID: lg.ID, OfferedBy: accA.ID, Counterparty: accB.ID, DefaultRateBps: 2000, Installments: 1,
		Items: []store.LineItem{
			{Kind: store.LineCommodity, Commodity: "Oil", QtyFixed: 100 * money.QtyScale, Dir: store.DirGive},
			{Kind: store.LineGold, GoldCents: 1000, Dir: store.DirTake},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tr, err = m.SetTradeStatus(accB.ID, created.ID, "accept") // AcceptedDay == base
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	return m, ck, tr, accA.ID, accB.ID, lg.ID
}

func TestTick_TradeAutoSettlesWhenDue(t *testing.T) {
	m, _, tr, a, b, lid := activeTrade(t)
	tk := New(m, Config{Interval: time.Minute, GraceIntervals: 1})
	// Installment 0 is due at base+60. There is no miss-grace now: the server settles it automatically on the
	// net payer's behalf the moment it comes due.
	if ts, _, _ := tk.Tick(time.Unix(base+59, 0)); ts != 0 {
		t.Errorf("before due: settled %d, want 0", ts)
	}
	if ts, _, _ := tk.Tick(time.Unix(base+61, 0)); ts != 1 {
		t.Fatalf("at due: settled %d, want 1", ts)
	}
	got, _ := m.GetTrade(tr.ID)
	if got.Status != store.TradeCompleted {
		t.Errorf("trade status %s, want completed", got.Status)
	}
	// Payment is booked as a settlement event B (net payer) → A (net receiver); NO auto-bond is minted.
	evs, _, _ := m.SettlementsForAccount(lid, a, 0)
	if len(evs) != 1 || evs[0].PayerID != b || evs[0].ReceiverID != a || evs[0].Cents <= 0 {
		t.Fatalf("expected one settlement B->A, got %+v", evs)
	}
	if bonds, _ := m.BondsFor(lid, a); len(bonds) != 0 {
		t.Fatalf("auto-settle must not mint a bond, got %+v", bonds)
	}
	// Completed trade → no further settlements.
	if ts, _, _ := tk.Tick(time.Unix(base+10_000, 0)); ts != 0 {
		t.Errorf("re-tick settled = %d, want 0", ts)
	}
}

func TestTick_BondGoesDelinquentThenDefaults(t *testing.T) {
	m, ck, tr, a, _, lid := activeTrade(t)
	// Small garnish floor so a defaulted bond isn't cleared in the same tick it defaults (keeps the
	// delinquent→default transition observable here; the escapability test below covers garnishment-to-clear).
	p := store.DefaultEconParams()
	p.GarnishMinWriteDownCents = 1000
	m.SetEconParams(p)
	tk := New(m, Config{Interval: time.Minute, GraceIntervals: 1})

	// Seed a bond by missing the trade installment directly (trades now auto-settle on the ticker, so we mint the
	// bond explicitly). Advance the clock first so the bond is CREATED at a known time (base+121).
	ck.set(base + 121)
	if _, _, err := m.MissTradeInstallment(tr.ID); err != nil {
		t.Fatalf("seed bond: %v", err)
	}
	bonds, _ := m.BondsFor(lid, a)
	if len(bonds) != 1 {
		t.Fatalf("want 1 bond, got %d", len(bonds))
	}
	created := bonds[0].Created.Unix() // == base+121
	bondID := bonds[0].ID

	// Bond installment 0 due at created+60, missed after grace at created+120 → delinquent.
	if _, bm, _ := tk.Tick(time.Unix(created+121, 0)); bm != 1 {
		t.Fatalf("first bond sweep missed %d, want 1", bm)
	}
	if bd, _ := m.GetBond(bondID); bd.Status != store.BondDelinquent {
		t.Errorf("status %s, want delinquent", bd.Status)
	}
	// The prior miss advanced the cursor; next obligation due at created+180 → second miss → terminal default.
	if _, bm, _ := tk.Tick(time.Unix(created+181, 0)); bm != 1 {
		t.Fatalf("second bond sweep missed %d, want 1", bm)
	}
	bd, _ := m.GetBond(bondID)
	if bd.Status != store.BondDefaultedReceivable {
		t.Fatalf("status %s, want defaultedReceivable", bd.Status)
	}
	frozen := bd.TotalDueCents
	// Terminal: the frozen TotalDueCents never changes (interest stops; only GarnishedCents moves).
	tk.Tick(time.Unix(created+100_000, 0))
	if bd2, _ := m.GetBond(bondID); bd2.TotalDueCents != frozen {
		t.Errorf("TotalDueCents changed after default: %d != %d", bd2.TotalDueCents, frozen)
	}
	_ = tr
}

// Auto-settle is server-driven, so it does NOT wait on the payer being online: an offline obligor's installment
// still settles on time (the booked event reconciles to their treasury whenever their client next polls). This
// closes the dodge-by-quitting hole without needing the old auto-bond penalty.
func TestTick_TradeAutoSettlesEvenWhenPayerOffline(t *testing.T) {
	m, _, _, a, b, lid := activeTrade(t) // B (net payer) is never Touch()-ed → offline
	tk := New(m, Config{Interval: time.Minute, GraceIntervals: 1, OfflineGraceIntervals: 3, OfflineThreshold: 30 * time.Second})
	if ts, _, _ := tk.Tick(time.Unix(base+61, 0)); ts != 1 {
		t.Fatalf("offline payer: settled %d, want 1 (offline must not delay server settlement)", ts)
	}
	evs, _, _ := m.SettlementsForAccount(lid, a, 0)
	if len(evs) != 1 || evs[0].PayerID != b || evs[0].ReceiverID != a {
		t.Fatalf("expected one settlement B->A, got %+v", evs)
	}
}

// The escapability invariant: garnishment monotonically reduces a defaulted debt to zero in bounded ticks, and
// the city leaves austerity. Uses a small garnish floor so the austerity window is observable.
func TestGarnish_EscapabilityAndCityState(t *testing.T) {
	m, ck, tr, a, b, lid := activeTrade(t)
	p := store.DefaultEconParams()
	p.GarnishMinWriteDownCents = 10000 // §100 / tick → ~5 ticks to clear the ~§492 debt
	m.SetEconParams(p)
	tk := New(m, Config{Interval: time.Minute, GraceIntervals: 1})

	ck.set(base + 121)
	if _, _, err := m.MissTradeInstallment(tr.ID); err != nil { // seed an auto-bond B owes A at base+121
		t.Fatalf("seed bond: %v", err)
	}
	bonds, _ := m.BondsFor(lid, a)
	bondID := bonds[0].ID
	created := bonds[0].Created.Unix()
	tk.Tick(time.Unix(created+121, 0)) // delinquent
	tk.Tick(time.Unix(created+181, 0)) // default (+ first garnish)

	aust, out, n := m.CityState(lid, b)
	if !aust || n != 1 || out <= 0 {
		t.Fatalf("expected austerity: aust=%v out=%d n=%d", aust, out, n)
	}

	prev := out
	for i := 0; i < 12 && out > 0; i++ {
		tk.Tick(time.Unix(created+10000+int64(i)*60, 0))
		_, out, _ = m.CityState(lid, b)
		if out > 0 && out >= prev {
			t.Fatalf("garnish did not reduce outstanding: %d -> %d", prev, out)
		}
		prev = out
	}
	if aust2, out2, _ := m.CityState(lid, b); aust2 || out2 != 0 {
		t.Errorf("city still in austerity: aust=%v out=%d", aust2, out2)
	}
	if bd, _ := m.GetBond(bondID); bd.Status != store.BondCleared {
		t.Errorf("bond status %s, want cleared", bd.Status)
	}
}

// The timebox backstop: a debt too large for the garnish floor to clear is written off after AusterityMaxTicks.
func TestGarnish_TimeboxWriteOff(t *testing.T) {
	m, ck, tr, a, b, lid := activeTrade(t)
	p := store.DefaultEconParams()
	p.GarnishMinWriteDownCents = 1 // never clears the ~§492 debt
	p.AusterityMaxTicks = 3
	m.SetEconParams(p)
	tk := New(m, Config{Interval: time.Minute, GraceIntervals: 1})

	ck.set(base + 121)
	if _, _, err := m.MissTradeInstallment(tr.ID); err != nil { // seed an auto-bond B owes A at base+121
		t.Fatalf("seed bond: %v", err)
	}
	bonds, _ := m.BondsFor(lid, a)
	bondID := bonds[0].ID
	created := bonds[0].Created.Unix()
	tk.Tick(time.Unix(created+121, 0))
	tk.Tick(time.Unix(created+181, 0)) // default + garnish tick 1
	for i := 0; i < 5; i++ {
		tk.Tick(time.Unix(created+10000+int64(i)*60, 0)) // garnish ticks 2,3 → write-off at 3
	}
	if bd, _ := m.GetBond(bondID); bd.Status != store.BondWrittenOff {
		t.Errorf("bond status %s, want writtenOff", bd.Status)
	}
	if aust, _, _ := m.CityState(lid, b); aust {
		t.Errorf("city should have escaped austerity after write-off")
	}
	_ = a
}
