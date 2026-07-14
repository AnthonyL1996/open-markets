package postgres

import (
	"math/rand"
	"time"

	"openmarkets/server/internal/market"
	"openmarkets/server/internal/store"
)

// ── Runtime online signal (in-memory, not persisted — matches Memory.lastActive) ──

func (p *PG) Touch(accountID string) {
	if accountID == "" {
		return
	}
	p.mu.Lock()
	p.lastActive[accountID] = p.clockLocked()
	p.mu.Unlock()
}

func (p *PG) LastActive(accountID string) (time.Time, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	t, ok := p.lastActive[accountID]
	return t, ok
}

// clockLocked is clock() without taking mu (caller already holds it).
func (p *PG) clockLocked() time.Time {
	if p.now != nil {
		return p.now().UTC()
	}
	return time.Now().UTC()
}

// ── Setup knobs (startup-only; safe before serving) ───────────────────────────

// SetPricer installs the accept-time price source.
func (p *PG) SetPricer(pr store.Pricer) {
	p.mu.Lock()
	p.pricer = pr
	p.mu.Unlock()
}

// SetClock overrides the time source (tests/startup).
func (p *PG) SetClock(now func() time.Time) {
	p.mu.Lock()
	p.now = now
	p.mu.Unlock()
}

// SetEconParams overrides the economy knobs, guarding the austerity-escapability invariant exactly like Memory.
func (p *PG) SetEconParams(e store.EconParams) {
	if e.GarnishMinWriteDownCents < 1 {
		e.GarnishMinWriteDownCents = 1
	}
	if e.AusterityMaxTicks < 1 {
		e.AusterityMaxTicks = 1
	}
	p.mu.Lock()
	p.econ = e
	p.mu.Unlock()
}

// SetMarketParams installs the index aggregation params + the known commodity set.
func (p *PG) SetMarketParams(mp market.Params, commodities []string) {
	p.mu.Lock()
	p.mktParams = mp
	p.commodities = append([]string(nil), commodities...)
	p.mu.Unlock()
}

// SetEventRand overrides the shock RNG (deterministic tests).
func (p *PG) SetEventRand(rng *rand.Rand) {
	if rng == nil {
		return
	}
	p.mu.Lock()
	p.rng = rng
	p.mu.Unlock()
}

// ── M9 market dynamics (EPHEMERAL — never persisted) ──────────────────────────

func (p *PG) AdvanceEvents() {
	p.mu.Lock()
	p.priceEvents = market.StepEvents(p.priceEvents, p.commodities, p.eventParams, p.rng)
	p.mu.Unlock()
}

// SampleHistory appends the current effective index per (league, commodity) to each ring. Reads each league's
// reports from the DB (outside mu), then updates the in-memory ring under mu.
func (p *PG) SampleHistory() {
	leagues := p.allLeagueIDs()
	p.mu.Lock()
	for _, lid := range leagues {
		eff := market.EffectiveIndices(p.commodityIndices(lid), p.priceEvents, p.mktParams)
		for c, v := range eff {
			key := lid + "|" + c
			ring := append(p.indexHist[key], v)
			if len(ring) > indexHistoryLen {
				ring = ring[len(ring)-indexHistoryLen:]
			}
			p.indexHist[key] = ring
		}
	}
	p.mu.Unlock()
}

// allLeagueIDs reads every league id (for SampleHistory's per-league sweep).
func (p *PG) allLeagueIDs() []string {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx, `SELECT id FROM leagues`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return out
		}
		out = append(out, s)
	}
	return out
}

// commodityIndices computes a league's report-elasticity indices from the DB reports. Does NOT take mu; the
// caller (SampleHistory) holds it, EffectiveIndices below takes its own read snapshot.
func (p *PG) commodityIndices(leagueID string) map[string]float64 {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx, `SELECT account_id, commodity, net_supply FROM reports WHERE league_id=$1`, leagueID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var rs []market.Report
	for rows.Next() {
		var r market.Report
		if err := rows.Scan(&r.AccountID, &r.Commodity, &r.NetSupply); err != nil {
			return nil
		}
		rs = append(rs, r)
	}
	return market.CommodityIndicesWithShields(rs, p.mktParams, store.MarketShieldsFromEffects(p.LeagueEffects(leagueID)))
}

// EventMultipliers returns the global per-commodity event multiplier (omitting neutral ones).
func (p *PG) EventMultipliers() map[string]float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]float64, len(p.priceEvents))
	for c, e := range p.priceEvents {
		if e.Mult > 0 {
			out[c] = e.Mult
		}
	}
	return out
}

// SetEvent sets the GLOBAL ephemeral price event for a commodity (replacing any active one), under the same lock
// EventStates reads. The crisis scheduler injects a named crisis here; like the rest of the price-shock map this is
// EPHEMERAL (in-process only, never persisted — matches Memory).
func (p *PG) SetEvent(commodity string, e market.EventState) {
	if commodity == "" {
		return
	}
	p.mu.Lock()
	if p.priceEvents == nil {
		p.priceEvents = map[string]market.EventState{}
	}
	p.priceEvents[commodity] = e
	p.mu.Unlock()
}

// EventStates returns a copy of the global active shocks.
func (p *PG) EventStates() map[string]market.EventState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]market.EventState, len(p.priceEvents))
	for c, e := range p.priceEvents {
		out[c] = e
	}
	return out
}

// EffectiveIndices returns a league's current per-commodity effective index.
func (p *PG) EffectiveIndices(leagueID string) map[string]float64 {
	idx := p.commodityIndices(leagueID)
	p.mu.RLock()
	defer p.mu.RUnlock()
	return market.EffectiveIndices(idx, p.priceEvents, p.mktParams)
}

// IndexHistory returns the rolling effective-index history per commodity for a league (a copy).
func (p *PG) IndexHistory(leagueID string) map[string][]float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	prefix := leagueID + "|"
	out := map[string][]float64{}
	for key, ring := range p.indexHist {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			cp := make([]float64, len(ring))
			copy(cp, ring)
			out[key[len(prefix):]] = cp
		}
	}
	return out
}
