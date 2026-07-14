package store

import (
	"math/rand"

	"openmarkets/server/internal/market"
)

// indexHistoryLen is the rolling sample count kept per (league, commodity) for the dashboard sparkline.
const indexHistoryLen = 16

// SetMarketParams installs the index aggregation params + the known commodity set the shock generator rolls over.
// Startup-only (before serving). The commodity set is the base-price table keys.
func (m *Memory) SetMarketParams(p market.Params, commodities []string) {
	m.mu.Lock()
	m.mktParams = p
	m.commodities = append([]string(nil), commodities...)
	m.mu.Unlock()
}

// SetEventRand overrides the shock RNG (deterministic tests). Startup/test only.
func (m *Memory) SetEventRand(rng *rand.Rand) {
	if rng == nil {
		return
	}
	m.mu.Lock()
	m.rng = rng
	m.mu.Unlock()
}

// AdvanceEvents steps the GLOBAL price-shock map one due-cycle tick (start/decay). Driven by the due-clock.
// Ephemeral — events are never persisted (a restart clears them; they regenerate). Touches only the price index,
// never settlement cash, so it cannot affect conservation.
func (m *Memory) AdvanceEvents() {
	m.mu.Lock()
	m.priceEvents = market.StepEvents(m.priceEvents, m.commodities, m.eventParams, m.rng)
	m.mu.Unlock()
}

// SampleHistory appends the current EFFECTIVE index (per-league elasticity × global event) to each
// (league, commodity) ring, capped at indexHistoryLen. Driven by the due-clock so the sparkline samples on a fixed
// cadence. Ephemeral.
func (m *Memory) SampleHistory() {
	m.mu.Lock()
	for lid := range m.leagues {
		eff := market.EffectiveIndices(m.commodityIndicesLocked(lid), m.priceEvents, m.mktParams)
		for c, v := range eff {
			// Key invariant: leagueID is an opaque id.New() (no '|') and commodities are fixed enum keys, so the
			// first '|' cleanly separates them in IndexHistory's prefix split (same assumption as reportKey).
			key := lid + "|" + c
			ring := append(m.indexHist[key], v)
			if len(ring) > indexHistoryLen {
				ring = ring[len(ring)-indexHistoryLen:]
			}
			m.indexHist[key] = ring
		}
	}
	m.mu.Unlock()
}

// commodityIndicesLocked computes a league's report-elasticity indices. Caller holds m.mu.
func (m *Memory) commodityIndicesLocked(leagueID string) map[string]float64 {
	var rs []market.Report
	for _, r := range m.reports {
		if r.LeagueID == leagueID {
			rs = append(rs, market.Report{AccountID: r.AccountID, Commodity: r.Commodity, NetSupply: r.NetSupply})
		}
	}
	var effects []Effect
	for _, e := range m.effects {
		if e.LeagueID == leagueID {
			effects = append(effects, e)
		}
	}
	return market.CommodityIndicesWithShields(rs, m.mktParams, MarketShieldsFromEffects(effects))
}

// EventMultipliers returns the GLOBAL per-commodity event multiplier (omitting neutral ones) — for the accept-time
// pricer to fold into the contract index.
func (m *Memory) EventMultipliers() map[string]float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]float64, len(m.priceEvents))
	for c, e := range m.priceEvents {
		if e.Mult > 0 {
			out[c] = e.Mult
		}
	}
	return out
}

// SetEvent sets the GLOBAL ephemeral price event for a commodity (replacing any active one), under the same lock
// EventStates reads. Used by the crisis scheduler to inject a named, narrated crisis. Ephemeral (not persisted) —
// touches only the price index, never settlement cash, so it cannot affect conservation.
func (m *Memory) SetEvent(commodity string, e market.EventState) {
	if commodity == "" {
		return
	}
	m.mu.Lock()
	if m.priceEvents == nil {
		m.priceEvents = map[string]market.EventState{}
	}
	m.priceEvents[commodity] = e
	m.mu.Unlock()
}

// EventStates returns a copy of the global active shocks (for the /prices eventPct field + the client's alert).
func (m *Memory) EventStates() map[string]market.EventState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]market.EventState, len(m.priceEvents))
	for c, e := range m.priceEvents {
		out[c] = e
	}
	return out
}

// EffectiveIndices returns a league's current per-commodity effective index (elasticity × global event), for /prices.
func (m *Memory) EffectiveIndices(leagueID string) map[string]float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return market.EffectiveIndices(m.commodityIndicesLocked(leagueID), m.priceEvents, m.mktParams)
}

// IndexHistory returns the rolling effective-index history per commodity for a league (a copy), for /prices.
func (m *Memory) IndexHistory(leagueID string) map[string][]float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	prefix := leagueID + "|"
	out := map[string][]float64{}
	for key, ring := range m.indexHist {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			cp := make([]float64, len(ring))
			copy(cp, ring)
			out[key[len(prefix):]] = cp
		}
	}
	return out
}
