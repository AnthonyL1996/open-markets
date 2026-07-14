package market

import "math/rand"

// EventState is an active price shock for one commodity: a multiplier applied on top of the report-elasticity index,
// for a bounded number of due-cycle ticks. Events are GLOBAL (M9 decision) — one series per commodity shared across
// every league, so a shock hits everyone at once (one world economy). Elasticity stays per-league, so the effective
// index a league sees is its own elasticity × the global event multiplier.
type EventState struct {
	Mult      float64 `json:"mult"`      // price multiplier, e.g. 1.37 (spike) or 0.62 (slump); 1.0 = none
	TicksLeft int     `json:"ticksLeft"` // remaining due-cycle ticks before the shock clears
	// Name/Narrative/Kind name a CRISIS (social slice 3) — a curated, narrated shock the whole league weathers.
	// A plain RANDOM shock from StepEvents leaves all three EMPTY; the crisis scheduler (cmd/openmarketsd) sets
	// them via SetEvent. They are CARRIED THROUGH decay (StepEvents preserves them) so a crisis keeps its identity
	// until it clears. EffectiveIndices/ApplyEvent/EventPct read only Mult — the strings are display metadata.
	Name      string `json:"name,omitempty"`
	Narrative string `json:"narrative,omitempty"`
	Kind      string `json:"kind,omitempty"`
}

// EventParams tunes the shock generator. Defaults mirror the old client LocalPriceSim (8%/tick start chance,
// ±25–50% magnitude, 4–8 ticks). All tunable.
type EventParams struct {
	ChancePerTick float64 // per-commodity probability of STARTING a shock on a tick with no active event
	MinUp, MaxUp  float64 // spike multiplier range (e.g. 1.25 … 1.50)
	MinDown       float64 // slump multiplier range (e.g. 0.50 … 0.75)
	MaxDown       float64
	MinTicks      int // shock duration range (ticks)
	MaxTicks      int
}

// DefaultEventParams ports the client's original event feel: ~8%/tick, ±25–50% for 4–8 ticks.
func DefaultEventParams() EventParams {
	return EventParams{
		ChancePerTick: 0.08,
		MinUp:         1.25, MaxUp: 1.50,
		MinDown: 0.50, MaxDown: 0.75,
		MinTicks: 4, MaxTicks: 8,
	}
}

// StepEvents advances the global event map by one tick (PURE given rng): every active shock decays one tick and is
// removed at zero; every commodity with NO active shock rolls ChancePerTick to start a fresh one (a coin-flip
// spike/slump, random magnitude in range, random duration). Returns a NEW map (does not mutate cur). Commodities is
// the full known set (the base-price table), so a shock can start on any commodity whether or not it's being traded.
func StepEvents(cur map[string]EventState, commodities []string, p EventParams, rng *rand.Rand) map[string]EventState {
	p = p.saneEvents()
	next := make(map[string]EventState, len(cur))
	// Decay existing shocks. CARRY the crisis identity (Name/Narrative/Kind) through the decay so a named crisis
	// keeps its name until it fully clears (the scheduler diffs EventStates names to detect a crisis ending).
	for c, e := range cur {
		if e.TicksLeft > 1 {
			next[c] = EventState{
				Mult: e.Mult, TicksLeft: e.TicksLeft - 1,
				Name: e.Name, Narrative: e.Narrative, Kind: e.Kind,
			}
		}
		// TicksLeft <= 1 → dropped (cleared).
	}
	// Maybe start a new shock on each idle commodity.
	for _, c := range commodities {
		if _, active := next[c]; active {
			continue
		}
		if rng.Float64() >= p.ChancePerTick {
			continue
		}
		var mult float64
		if rng.Intn(2) == 0 {
			mult = p.MinUp + rng.Float64()*(p.MaxUp-p.MinUp) // spike
		} else {
			mult = p.MinDown + rng.Float64()*(p.MaxDown-p.MinDown) // slump
		}
		dur := p.MinTicks
		if p.MaxTicks > p.MinTicks {
			dur += rng.Intn(p.MaxTicks - p.MinTicks + 1)
		}
		next[c] = EventState{Mult: mult, TicksLeft: dur}
	}
	return next
}

// ApplyEvent folds a single event multiplier into a (report-elasticity) index and re-clamps. mult <= 0 is treated as
// neutral (1.0). Used by both the accept-time pricer and the /prices serve path so they agree on the effective index.
func ApplyEvent(index, mult float64, p Params) float64 {
	p = p.sane()
	if mult <= 0 {
		mult = 1.0
	}
	return clamp(index*mult, p.Min, p.Max)
}

// EffectiveIndices folds the global events into a league's per-commodity elasticity indices (× mult, re-clamped).
// A commodity with no event keeps its elasticity index. Pure.
func EffectiveIndices(elasticity map[string]float64, events map[string]EventState, p Params) map[string]float64 {
	p = p.sane()
	out := make(map[string]float64, len(elasticity))
	for c, v := range elasticity {
		mult := 1.0
		if e, ok := events[c]; ok && e.Mult > 0 {
			mult = e.Mult
		}
		out[c] = clamp(v*mult, p.Min, p.Max)
	}
	return out
}

// EventPct is the displayed swing for a commodity's active shock, as a signed whole percent (e.g. +37, -38); 0 when
// there's no active event. For the client's dashboard / Chirper alert.
func EventPct(events map[string]EventState, commodity string) int {
	e, ok := events[commodity]
	if !ok || e.Mult <= 0 {
		return 0
	}
	pct := (e.Mult - 1.0) * 100.0
	if pct < 0 {
		return int(pct - 0.5)
	}
	return int(pct + 0.5)
}

func (p EventParams) saneEvents() EventParams {
	if p.ChancePerTick < 0 {
		p.ChancePerTick = 0
	}
	if p.MaxUp < p.MinUp {
		p.MaxUp = p.MinUp
	}
	if p.MaxDown < p.MinDown {
		p.MaxDown = p.MinDown
	}
	if p.MinTicks < 1 {
		p.MinTicks = 1
	}
	if p.MaxTicks < p.MinTicks {
		p.MaxTicks = p.MinTicks
	}
	return p
}
