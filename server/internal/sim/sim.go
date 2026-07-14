// Package sim is a randomized economy simulation harness for the M5 trade/bond/loan/austerity system. It drives
// the store + due-clock through a seeded, deterministic stream of random operations (offer/accept/settle trades,
// offer/accept/repay loans, advance time → miss/default/garnish), then drains to a steady state and checks the
// system-level invariants the unit tests can't: global cash CONSERVATION (the settlement log is zero-sum),
// austerity ESCAPABILITY (no bond stuck defaulted, no city stuck in austerity), no STRANDED active trades, and
// no settled-count overflow — all without panicking. Run() returns a Result (it asserts nothing) so it backs
// both the CI property test and the `omsim` stress command.
package sim

import (
	"math/rand"
	"time"

	"openmarkets/server/internal/duecycle"
	"openmarkets/server/internal/money"
	"openmarkets/server/internal/store"
)

// Params controls the run. Deterministic for a given Seed.
type Params struct {
	Members       int
	Rounds        int
	MaxDrainTicks int
	Seed          int64
}

// Defaults are sane CI-sized parameters.
func Defaults() Params { return Params{Members: 5, Rounds: 400, MaxDrainTicks: 4000, Seed: 1} }

// Result captures the post-run invariant state. OK is true iff every invariant held.
type Result struct {
	Accounts            int
	Trades              int
	Bonds               int
	ConservationTotal   int64 // Σ per-account net from the event log — structurally always 0 (a cheap sanity bit)
	VoidNetCents        int64 // net booked against the empty ("") account — MUST be 0 (money from/to a void counterparty)
	StrangerAccounts    int   // accounts in the net map that aren't league members — MUST be 0 (leaked counterparty)
	StuckBondsDefaulted int   // bonds still defaultedReceivable after the drain — escapability failure
	StuckActiveTrades   int   // trades still active after the drain — stranded
	AusterityCities     int   // cities still in austerity after the drain
	SettledOverflow     int   // trades/bonds with Settled > Installments — should be impossible
	DrainTicks          int
	FullyDrained        bool
	// Coverage: peak counts observed DURING the run, so a test can assert the hard paths were actually
	// exercised (a clean final state over a path that never defaulted/entered austerity proves little).
	PeakDefaultedBonds  int
	PeakAusterityCities int
	OK                  bool
}

// Run executes one deterministic simulation and returns the invariant Result (no assertions).
func Run(p Params) Result {
	if p.Members < 2 {
		p.Members = 2
	}
	if p.MaxDrainTicks < 1 {
		p.MaxDrainTicks = 4000
	}
	rng := rand.New(rand.NewSource(p.Seed))

	m := store.NewMemory("")
	cur := int64(100000)
	m.SetClock(func() time.Time { return time.Unix(cur, 0).UTC() })
	m.SetPricer(func(_ string, c string) (int64, bool) {
		v, ok := map[string]int64{"Oil": 400, "Coal": 150, "Ore": 250}[c]
		return v, ok
	})

	owner, _, _ := m.CreateAccount()
	lg, _ := m.CreateLeague(owner.ID, "Sim")
	members := []string{owner.ID}
	for i := 1; i < p.Members; i++ {
		a, _, _ := m.CreateAccount()
		_ = m.JoinLeague(a.ID, lg.ID)
		members = append(members, a.ID)
	}
	tk := duecycle.New(m, duecycle.Config{
		Interval: time.Minute, GraceIntervals: 1, MaxMissesPerTick: 16, OfflineGraceIntervals: 0,
	})

	pick := func() string { return members[rng.Intn(len(members))] }
	commodities := []string{"Oil", "Coal", "Ore"}
	dir := func() string {
		if rng.Intn(2) == 0 {
			return store.DirGive
		}
		return store.DirTake
	}
	// randBasket builds a 2-line commodity+gold basket with randomized directions and magnitudes, so the
	// offerer is the net payer on some trades and the net receiver on others (exercising both settle directions).
	// Opposite directions on the two lines guarantee a non-trivial two-sided trade the store will accept.
	randBasket := func() []store.LineItem {
		d := dir()
		opp := store.DirTake
		if d == store.DirTake {
			opp = store.DirGive
		}
		return []store.LineItem{
			{Kind: store.LineCommodity, Commodity: commodities[rng.Intn(len(commodities))],
				QtyFixed: int64(1+rng.Intn(50)) * money.QtyScale, Dir: d},
			{Kind: store.LineGold, GoldCents: int64(1 + rng.Intn(20000)), Dir: opp},
		}
	}
	allTrades := func() []store.Trade {
		seen := map[string]bool{}
		var out []store.Trade
		for _, mid := range members {
			ts, _ := m.TradesFor(lg.ID, mid)
			for _, t := range ts {
				if !seen[t.ID] {
					seen[t.ID] = true
					out = append(out, t)
				}
			}
		}
		return out
	}
	allBonds := func() []store.Bond {
		seen := map[string]bool{}
		var out []store.Bond
		for _, mid := range members {
			bs, _ := m.BondsFor(lg.ID, mid)
			for _, b := range bs {
				if !seen[b.ID] {
					seen[b.ID] = true
					out = append(out, b)
				}
			}
		}
		return out
	}

	var res Result
	// sample records peak austerity/default counts so the test can prove the hard paths were exercised.
	sample := func() {
		defaulted := 0
		for _, b := range allBonds() {
			if b.Status == store.BondDefaultedReceivable {
				defaulted++
			}
		}
		if defaulted > res.PeakDefaultedBonds {
			res.PeakDefaultedBonds = defaulted
		}
		aust := 0
		for _, mid := range members {
			if in, _, _ := m.CityState(lg.ID, mid); in {
				aust++
			}
		}
		if aust > res.PeakAusterityCities {
			res.PeakAusterityCities = aust
		}
	}

	for round := 0; round < p.Rounds; round++ {
		switch rng.Intn(6) {
		case 0: // offer a trade between two distinct members; maybe accept
			a, b := pick(), pick()
			if a == b {
				break
			}
			created, err := m.CreateTrade(store.Trade{
				LeagueID: lg.ID, OfferedBy: a, Counterparty: b, DefaultRateBps: 2000,
				Installments: 1 + rng.Intn(4), Items: randBasket(),
			})
			if err == nil && rng.Intn(2) == 0 {
				_, _ = m.SetTradeStatus(b, created.ID, "accept")
			}
		case 1: // settle a due trade installment (only the net payer can)
			for _, t := range allTrades() {
				if t.Status == store.TradeActive && t.Settled < t.Installments && rng.Intn(2) == 0 {
					payer, _ := t.NetPayerReceiver()
					_, _, _ = m.SettleTradeInstallment(payer, t.ID)
				}
			}
		case 2: // offer a manual loan; maybe accept
			lender, borrower := pick(), pick()
			if lender == borrower {
				break
			}
			off, err := m.OfferLoan(store.Bond{
				LeagueID: lg.ID, CreditorID: lender, DebtorID: borrower,
				PrincipalCents: int64(100 + rng.Intn(50000)), InterestBps: int64(rng.Intn(3000)),
				Installments: 1 + rng.Intn(6), ProposedBy: lender,
			})
			if err == nil && rng.Intn(2) == 0 {
				_, _, _ = m.AcceptLoan(borrower, off.ID)
			}
		case 3: // repay a bond (only the debtor)
			for _, b := range allBonds() {
				if (b.Status == store.BondActive || b.Status == store.BondDelinquent) && rng.Intn(2) == 0 {
					_, _, _ = m.SettleBondInstallment(b.DebtorID, b.ID)
				}
			}
		default: // advance time + tick → drives misses / defaults / garnishment
			cur += 60 * int64(1+rng.Intn(3))
			tk.Tick(time.Unix(cur, 0))
		}
		sample()
	}

	// Drain to steady state: keep ticking until no active trade and no open/defaulted bond remains (or the cap,
	// which would itself be an escapability failure surfaced below).
	hasOpen := func() bool {
		for _, t := range allTrades() {
			if t.Status == store.TradeActive && t.Settled < t.Installments {
				return true
			}
		}
		for _, b := range allBonds() {
			if b.Status == store.BondActive || b.Status == store.BondDelinquent || b.Status == store.BondDefaultedReceivable {
				return true
			}
		}
		return false
	}
	for res.DrainTicks < p.MaxDrainTicks && hasOpen() {
		cur += 60
		tk.Tick(time.Unix(cur, 0))
		res.DrainTicks++
		sample()
	}
	res.FullyDrained = !hasOpen()

	// Final invariants.
	res.Accounts = len(members)
	net, total, auditErr := m.AuditLeague(lg.ID)
	res.ConservationTotal = total
	// The real conservation check: every settlement event must move cash between two known league members.
	// A void ("") or non-member key APPEARING in the net map means an event touched a counterparty outside the
	// closed system — the bug class the tautological total can't catch. We flag presence, not just non-zero net,
	// so a malformed event masked by later activity netting back to zero can't hide (Codex).
	memberSet := map[string]bool{}
	for _, mid := range members {
		memberSet[mid] = true
	}
	for acc, cents := range net {
		if acc == "" {
			res.VoidNetCents += cents
			res.StrangerAccounts++ // a "" key existing at all is a violation
		} else if !memberSet[acc] {
			res.StrangerAccounts++
		}
	}
	trades := allTrades()
	res.Trades = len(trades)
	for _, t := range trades {
		if t.Status == store.TradeActive {
			res.StuckActiveTrades++
		}
		if t.Settled > t.Installments {
			res.SettledOverflow++
		}
	}
	bonds := allBonds()
	res.Bonds = len(bonds)
	for _, b := range bonds {
		if b.Status == store.BondDefaultedReceivable {
			res.StuckBondsDefaulted++
		}
		if b.Settled > b.Installments {
			res.SettledOverflow++
		}
	}
	for _, mid := range members {
		if aust, _, _ := m.CityState(lg.ID, mid); aust {
			res.AusterityCities++
		}
	}
	res.OK = auditErr == nil && res.ConservationTotal == 0 && res.VoidNetCents == 0 && res.StrangerAccounts == 0 &&
		res.StuckBondsDefaulted == 0 && res.StuckActiveTrades == 0 &&
		res.AusterityCities == 0 && res.SettledOverflow == 0 && res.FullyDrained
	return res
}
