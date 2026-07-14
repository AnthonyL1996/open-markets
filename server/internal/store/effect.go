package store

import (
	"sort"
	"strings"
	"time"

	"openmarkets/server/internal/id"
	"openmarkets/server/internal/market"
)

// Effect is a temporary server-authored city reward delivered in the /citystate payload. Investment-office and
// projectBuff effects grant demand + attractiveness; marketShield/priceEdge carry trade metadata. The client
// applies or displays them transiently (nothing baked into the game save). Expiry is driven by the due-clock
// (ExpireEffectsTick), so an effect lasts a bounded time whether or not the grantee is online.
type Effect struct {
	ID             string    `json:"id"`
	LeagueID       string    `json:"leagueId"`
	IssuerID       string    `json:"issuerId"`
	GranteeID      string    `json:"granteeId"`
	Kind           string    `json:"kind"`
	CostCents      int64     `json:"costCents"`              // the § the issuer invested (transferred to the grantee) — shown in the grantee's UI
	DemandBoost    int       `json:"demandBoost"`            // demand points added to the grantee's chosen channel (capped)
	DemandKind     string    `json:"demandKind"`             // which demand the boost targets: res | com | work (CS1 combines industrial+office into "work")
	AttractRate    int       `json:"attractRate"`            // attractiveness rate the grantee re-applies each cycle (capped)
	Commodity      string    `json:"commodity,omitempty"`    // trade-reward commodity; demand-only buffs leave it empty
	TradePctBips   int       `json:"tradePctBips,omitempty"` // trade reward magnitude in basis points
	TicksRemaining int       `json:"ticksRemaining"`         // due-cycle ticks (≈ in-game days) left before it expires
	Created        time.Time `json:"created"`
}

const EffectInvestmentOffice = "investmentOffice"
const EffectMarketShield = "marketShield"
const EffectPriceEdge = "priceEdge"

// MarketShieldsFromEffects converts active marketShield effects into the pure market package shape used by index
// aggregation and accept-time pricing. Non-trade effects and expired effects are ignored.
func MarketShieldsFromEffects(effects []Effect) []market.Shield {
	out := make([]market.Shield, 0, len(effects))
	for _, e := range effects {
		if e.Kind != EffectMarketShield || e.TicksRemaining <= 0 || e.GranteeID == "" || e.Commodity == "" ||
			e.TradePctBips <= 0 {
			continue
		}
		out = append(out, market.Shield{AccountID: e.GranteeID, Commodity: e.Commodity, DampeningBips: e.TradePctBips})
	}
	return out
}

// Demand channels the investment buff can target. CS1's DemandExtensionBase exposes exactly three channels;
// industrial and office share the single "workplace" channel, so there's no finer split than these.
const (
	DemandResidential = "res"
	DemandCommercial  = "com"
	DemandWorkplace   = "work" // industrial + office (combined in CS1)
)

// ValidDemandKind reports whether k is one of the three addressable demand channels.
func ValidDemandKind(k string) bool {
	return k == DemandResidential || k == DemandCommercial || k == DemandWorkplace
}

// Investment-office guardrail bounds. The buff magnitude is DERIVED from the § cost and capped, so a richer player
// can't buy an unbounded buff (magnitude cap, guardrail #3); the duration is bounded (time-box, guardrail #1); and
// only one active grant per issuer→grantee pair is allowed (cooldown, guardrail #4). Tunable.
const (
	InvestMinCostCents int64 = 100000    // §1,000 floor — a grant always costs something real
	InvestMaxCostCents int64 = 100000000 // §1,000,000 ceiling per grant
	InvestMinDays            = 1
	InvestMaxDays            = 14

	InvestDemandBoostCap = 20  // max demand points
	InvestAttractRateCap = 500 // max attractiveness rate/cycle (tune in-game)

	investCentsPerDemandPt int64 = 250000 // §2,500 per demand point  → §50k = 20 (cap)
	investCentsPerAttract  int64 = 10000  // §100 per attractiveness pt → §50k = 500 (cap)
)

// InvestBuffMagnitude derives the (demand, attractiveness) buff from the § cost, each floored at 1 and capped.
// Exported so an alternate Store backend (e.g. Postgres) derives the IDENTICAL buff magnitude as Memory.
func InvestBuffMagnitude(costCents int64) (demand, attract int) {
	demand = int(costCents / investCentsPerDemandPt)
	if demand < 1 {
		demand = 1
	} else if demand > InvestDemandBoostCap {
		demand = InvestDemandBoostCap
	}
	attract = int(costCents / investCentsPerAttract)
	if attract < 1 {
		attract = 1
	} else if attract > InvestAttractRateCap {
		attract = InvestAttractRateCap
	}
	return demand, attract
}

// GrantInvestment books the symmetric cash transfer (issuer → grantee, conserving cash) and creates a temporary
// investment-office buff on the grantee. The caller (HTTP handler) validates the cost is within
// [InvestMinCostCents, InvestMaxCostCents]; this method clamps the duration and derives + caps the buff magnitude.
// Returns ErrConflict for a self-grant or a duplicate active grant from the same issuer to the same grantee
// (cooldown = the buff's lifetime), and ErrNotFound if either party is not a league member.
func (m *Memory) GrantInvestment(leagueID, issuerID, granteeID string, costCents int64, days int, demandKind string) (Effect, SettlementEvent, error) {
	if issuerID == granteeID {
		return Effect{}, SettlementEvent{}, ErrConflict
	}
	if !ValidDemandKind(demandKind) {
		demandKind = DemandResidential // defensive default; the handler already rejects invalid kinds
	}
	if days < InvestMinDays {
		days = InvestMinDays
	} else if days > InvestMaxDays {
		days = InvestMaxDays
	}

	m.mu.Lock()
	set := m.members[leagueID]
	if set == nil || !set[issuerID] || !set[granteeID] {
		m.mu.Unlock()
		return Effect{}, SettlementEvent{}, ErrNotFound
	}
	// Cooldown: refuse a second active investment from the same issuer to the same grantee (no buff stacking from
	// one source; a different friend can still invest). The active grant is the cooldown window.
	for _, e := range m.effects {
		if e.Kind == EffectInvestmentOffice && e.LeagueID == leagueID &&
			e.IssuerID == issuerID && e.GranteeID == granteeID && e.TicksRemaining > 0 {
			m.mu.Unlock()
			return Effect{}, SettlementEvent{}, ErrConflict
		}
	}

	demand, attract := InvestBuffMagnitude(costCents)
	e := Effect{
		ID: id.New(), LeagueID: leagueID, IssuerID: issuerID, GranteeID: granteeID,
		Kind: EffectInvestmentOffice, CostCents: costCents, DemandBoost: demand, DemandKind: demandKind, AttractRate: attract,
		TicksRemaining: days, Created: m.clock(),
	}
	if m.effects == nil {
		m.effects = map[string]Effect{}
	}
	m.effects[e.ID] = e
	// Symmetric cost: the issuer pays the grantee — a real cash investment, zero-sum (conserves cash, passes
	// AuditLeague). The "cost" to the issuer is the § they hand over; the grantee gets the cash AND the buff.
	ev := m.appendEventLocked(leagueID, issuerID, granteeID, costCents, "invest:"+e.ID)
	m.mu.Unlock()
	// The cash event + effect are now committed in memory. Persist best-effort (log on failure) and return nil —
	// matching every other settlement-booking path (trade/bond settle, garnish): a disk-write failure must NOT be
	// reported to the client as a failed grant when the transfer is already live, or the client's cash view diverges.
	m.persistAfter()
	return e, ev, nil
}

// CityEffects returns the grantee's active (non-expired) effects in a league, oldest first. Read off /citystate.
func (m *Memory) CityEffects(leagueID, accountID string) []Effect {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Effect
	for _, e := range m.effects {
		if e.LeagueID == leagueID && e.GranteeID == accountID && e.TicksRemaining > 0 {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// CityEffectsIssued returns the active effects this account GRANTED (the investments it made) in a league, oldest
// first — the issuer-side mirror of CityEffects, for the "investments you've made" view.
func (m *Memory) CityEffectsIssued(leagueID, accountID string) []Effect {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Effect
	for _, e := range m.effects {
		if e.LeagueID == leagueID && e.IssuerID == accountID && e.TicksRemaining > 0 {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// LeagueEffects returns ALL active effects in a league (every issuer→grantee grant), oldest first — the league-wide
// transparency view. Each effect is self-describing (issuer + grantee + §).
func (m *Memory) LeagueEffects(leagueID string) []Effect {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Effect
	for _, e := range m.effects {
		if e.LeagueID == leagueID && e.TicksRemaining > 0 {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// InvestmentHistory returns the durable record of every investment ever made in a league — derived from the
// settlement-event log (the invest cash transfer is tagged ref "invest:<effectId>"), which survives effect expiry.
// Newest first. Carries the money trail (issuer/grantee/§/when) but not the buff details (kind/days), which live only
// on the now-expired Effect.
func (m *Memory) InvestmentHistory(leagueID string) []SettlementEvent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []SettlementEvent
	for _, e := range m.events {
		if e.LeagueID == leagueID && strings.HasPrefix(e.Ref, "invest:") {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq > out[j].Seq }) // newest first
	return out
}

// ExpireEffectsTick decrements every active effect's remaining lifetime by one due-cycle tick and removes any that
// reach zero, returning the number removed. Driven by the due-clock so a buff lasts a bounded number of ticks
// (≈ in-game days) whether or not the grantee is online. Removing an effect emits NO settlement event (the cash
// already moved at grant time), so it cannot affect cash conservation.
func (m *Memory) ExpireEffectsTick() int {
	m.mu.Lock()
	changed, expired := false, 0
	for k, e := range m.effects {
		changed = true
		if e.TicksRemaining <= 1 {
			delete(m.effects, k)
			expired++
			continue
		}
		e.TicksRemaining--
		m.effects[k] = e
	}
	m.mu.Unlock()
	if changed {
		m.persistAfter()
	}
	return expired
}
