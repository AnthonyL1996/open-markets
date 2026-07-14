// Package duecycle is the scheduled due-clock: it advances trade and bond installment deadlines on a real-time
// cadence and applies the consequences of unmet installments (auto-bond a trade shortfall; mark a bond
// delinquent/defaulted). Running on the SERVER means an offline city still accrues misses — closing the
// "dodge by quitting" hole (Codex #4). The cadence is configurable; v1 default is a fast dev interval so the
// whole loop is observable in one session. One grace interval is allowed before a due installment is missed.
package duecycle

import (
	"time"

	"openmarkets/server/internal/store"
)

// Store is the slice of the store the ticker needs (satisfied by *store.Memory).
type Store interface {
	ListActiveTrades() []store.Trade
	ListActiveBonds() []store.Bond
	ListDefaultedBonds() []store.Bond
	AutoSettleTradeInstallment(tradeID string) (store.Trade, store.SettlementEvent, error)
	MissTradeInstallment(tradeID string) (store.Trade, store.Bond, error)
	MissBondInstallment(bondID string) (store.Bond, bool, error)
	GarnishBond(bondID string) (store.Bond, store.SettlementEvent, bool, error)
	ExpireEffectsTick() int                        // age temporary co-op buffs by one tick, removing expired
	AdvanceEvents()                                // M9: step the global price-shock map one tick
	SampleHistory()                                // M9: sample the effective index into the sparkline ring
	LastActive(accountID string) (time.Time, bool) // runtime online signal for offline grace
}

// Config controls the cadence and grace.
type Config struct {
	Interval       time.Duration // real time that equals one installment period
	GraceIntervals int           // extra intervals before a due installment is missed
	// MaxMissesPerTick bounds how many overdue installments a single trade/bond can miss in ONE tick, so a long
	// outage drains its backlog over several ticks instead of a burst of sequential persists (risk-scan §C).
	MaxMissesPerTick int
	// OfflineGraceIntervals: extra grace granted to an obligor that appears offline (not seen within
	// OfflineThreshold), so a long-away player isn't auto-bonded the instant a due date passes. BOUNDED — after
	// this extra window they are still bonded, so going offline can't dodge an obligation forever (risk-scan §B).
	OfflineGraceIntervals int
	OfflineThreshold      time.Duration
}

// Ticker applies overdue consequences. It is safe to call Tick repeatedly; each call catches up any backlog.
type Ticker struct {
	store Store
	cfg   Config
}

// New builds a Ticker with sane defaults for any unset field.
func New(s Store, cfg Config) *Ticker {
	if cfg.Interval <= 0 {
		cfg.Interval = 2 * time.Minute
	}
	if cfg.GraceIntervals < 0 {
		cfg.GraceIntervals = 1
	}
	if cfg.MaxMissesPerTick < 1 {
		cfg.MaxMissesPerTick = 4
	}
	if cfg.OfflineGraceIntervals < 0 {
		cfg.OfflineGraceIntervals = 0
	}
	if cfg.OfflineThreshold <= 0 {
		cfg.OfflineThreshold = 2 * time.Minute // well above the client's ~8s poll, so only genuinely-gone players
	}
	return &Ticker{store: s, cfg: cfg}
}

// Tick runs one sweep at the given wall-clock time: it AUTO-SETTLES every trade installment that has come due
// (server-driven payment — an agreed trade pays itself, no manual action), MISSES every bond installment overdue
// beyond the grace window (advancing bonds toward default), then applies one austerity garnishment to each
// terminally-defaulted bond (the income-independent write-down that makes austerity escapable). Returns how many
// trade installments it auto-settled, how many bond installments it missed, and how many bonds it garnished.
func (t *Ticker) Tick(now time.Time) (tradesSettled, bondsMissed, garnished int) {
	for _, tr := range t.store.ListActiveTrades() {
		for n := 0; n < t.cfg.MaxMissesPerTick && t.tradeDue(tr, now); n++ {
			updated, _, err := t.store.AutoSettleTradeInstallment(tr.ID)
			if err != nil {
				break
			}
			tradesSettled++
			tr = updated
			if tr.Status != store.TradeActive || tr.Settled >= tr.Installments {
				break
			}
		}
	}
	for _, b := range t.store.ListActiveBonds() {
		for n := 0; n < t.cfg.MaxMissesPerTick && t.bondOverdue(b, now); n++ {
			updated, _, err := t.store.MissBondInstallment(b.ID)
			if err != nil {
				break
			}
			bondsMissed++
			b = updated
			if b.Status != store.BondActive && b.Status != store.BondDelinquent {
				break // terminal default — stop hammering
			}
		}
	}
	// Austerity garnishment: one income-independent write-down per defaulted bond per tick → the balance
	// shrinks monotonically and the city always escapes (or hits the timebox write-off).
	for _, b := range t.store.ListDefaultedBonds() {
		if _, _, emitted, err := t.store.GarnishBond(b.ID); err == nil && emitted {
			garnished++
		}
	}
	// Age temporary co-op buffs (M8 investment-office) one tick; expired ones are removed. No settlement event is
	// emitted (the cash moved at grant), so this cannot affect cash conservation.
	t.store.ExpireEffectsTick()
	// M9: advance the global price shocks, then sample the effective index into the sparkline history. Both touch
	// only the price index (never settlement cash), so they can't affect conservation.
	t.store.AdvanceEvents()
	t.store.SampleHistory()
	return tradesSettled, bondsMissed, garnished
}

// intervalSecs is the interval length in whole seconds.
func (t *Ticker) intervalSecs() int64 { return int64(t.cfg.Interval.Seconds()) }

// tradeDue reports whether the trade's CURRENT installment has reached its due time. The next unsettled
// installment is index Settled; it is due at AcceptedDay + (Settled+1)*interval. No grace and no offline grace
// apply: the server settles it automatically on the payer's behalf, so there is nothing to wait for — an offline
// payer's client reconciles the booked event whenever it next polls the settlement feed.
func (t *Ticker) tradeDue(tr store.Trade, now time.Time) bool {
	if tr.Status != store.TradeActive || tr.Settled >= tr.Installments {
		return false
	}
	due := tr.AcceptedDay + int64(tr.Settled+1)*t.intervalSecs()
	return now.Unix() >= due
}

// offlineGrace returns the extra grace intervals for an obligor that appears offline (never seen, or not seen
// within OfflineThreshold). Bounded: it delays — never prevents — the eventual auto-bond.
func (t *Ticker) offlineGrace(accountID string, now time.Time) int {
	if t.cfg.OfflineGraceIntervals == 0 {
		return 0
	}
	seen, ok := t.store.LastActive(accountID)
	if !ok || now.Sub(seen) > t.cfg.OfflineThreshold {
		return t.cfg.OfflineGraceIntervals
	}
	return 0
}

// bondOverdue reports whether a bond's current obligation is past due + grace. Both repayments (Settled) and
// prior misses (MissedCount) advance the timeline cursor, so consecutive misses are spaced by the interval and
// converge to terminal default after the store's miss tolerance.
func (t *Ticker) bondOverdue(b store.Bond, now time.Time) bool {
	if b.Status != store.BondActive && b.Status != store.BondDelinquent {
		return false
	}
	if b.Settled >= b.Installments {
		return false
	}
	cursor := b.Settled + b.MissedCount
	grace := t.cfg.GraceIntervals + t.offlineGrace(b.DebtorID, now)
	due := b.Created.Unix() + int64(cursor+1+grace)*t.intervalSecs()
	return now.Unix() >= due
}
