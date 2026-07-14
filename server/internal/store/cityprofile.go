package store

import "time"

// cityProfileHistCap bounds the retained per-account time-series. cityProfileHistKeepRecent is the count of
// newest samples the downsampler never thins — older points are removed first, giving a recent-dense / old-sparse
// all-time view.
const (
	cityProfileHistCap        = 150
	cityProfileHistKeepRecent = 30
)

// City-profile plausibility bounds (anti-cheat). City stats are client-reported and feed the leaderboards + graphs,
// so a spoofed client could otherwise post absurd numbers. POSTURE: percentage fields are CLAMPED to [0,100] and
// counts to >=0 (the value is repaired in place); a per-day Population delta that is implausibly large only sets the
// Suspect FLAG (the snapshot is still stored — flag-not-reject, so a legit fast-growing city is never silently lost,
// and the leaderboard layer is what de-weights a flagged member). Thresholds are GENEROUS to avoid false positives.
const (
	maxPopulation = 2_000_000 // hard cap on a single reported population (clamped)
	// suspectPopAbsDelta is the floor on the "implausible per-day Population change" test; the real threshold is
	// max(this, 3× previous population), so a small city can still grow fast without being flagged.
	suspectPopAbsDelta = 20_000
)

// clampInt repairs an out-of-range int to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ClampCityProfile repairs absolute-range fields IN PLACE before storage: percentages → [0,100], counts → >=0, and
// Population additionally capped at maxPopulation. Treasury/income/expenses (int64) are left as-is by design.
// Exported so an alternate Store backend (e.g. Postgres) applies the IDENTICAL anti-cheat clamp as Memory.
func ClampCityProfile(p *CityProfile) {
	p.Happiness = clampInt(p.Happiness, 0, 100)
	p.Attractiveness = clampInt(p.Attractiveness, 0, 100)
	p.Unemployment = clampInt(p.Unemployment, 0, 100)
	p.Crime = clampInt(p.Crime, 0, 100)
	p.Population = clampInt(p.Population, 0, maxPopulation)
	const maxInt = int(^uint(0) >> 1)
	for _, f := range []*int{
		&p.Tourists, &p.LandValue, &p.BuildingCount,
		&p.ResBuildings, &p.ComBuildings, &p.OffBuildings, &p.IndBuildings, &p.IndWorkers,
		&p.FarmWorkers, &p.ForestWorkers, &p.OreWorkers, &p.OilWorkers,
	} {
		*f = clampInt(*f, 0, maxInt)
	}
}

// ImplausibleDelta reports whether the move from prev (a previously stored snapshot) to next is implausible for a
// single per-day post. Currently only Population is checked: a change of more than max(suspectPopAbsDelta, 3× prev
// population) in one post is implausible. The FIRST post (no prior history) can't be delta-checked, so it relies on
// the absolute cap only (handled by ClampCityProfile) — never flagged here.
// Exported so an alternate Store backend (e.g. Postgres) applies the IDENTICAL Suspect flag rule as Memory.
func ImplausibleDelta(prev, next CityProfile) bool {
	threshold := suspectPopAbsDelta
	if t := 3 * prev.Population; t > threshold {
		threshold = t
	}
	delta := next.Population - prev.Population
	if delta < 0 {
		delta = -delta
	}
	return delta > threshold
}

// PutCityProfile stores (replacing) an account's latest leaguemate-visible city snapshot AND appends a copy to the
// retained time-series (downsampled to a bounded cap). ReportedAt is stamped server-side from the store clock — it
// is the durable "last seen" signal (persisted, unlike the runtime lastActive map). Reliability is stamped from the
// account's current on-time reputation (100 if unknown). Keyed by account, so the same city is visible to every
// league the account belongs to.
func (m *Memory) PutCityProfile(p CityProfile) error {
	p.ReportedAt = m.clock()
	// Anti-cheat: repair absolute-range fields in place (clamp), then FLAG (don't reject) an implausible per-day
	// delta against the previously stored snapshot. Flag-not-reject by design — a legit fast-growing city is still
	// stored; the leaderboard layer de-weights a flagged member instead of dropping the post.
	ClampCityProfile(&p)
	m.mu.Lock()
	if prev, ok := m.cityProfiles[p.AccountID]; ok && ImplausibleDelta(prev, p) {
		p.Suspect = true
	}
	rel := 100
	if a, ok := m.accounts[p.AccountID]; ok {
		rel = a.Reliability()
	}
	p.Reliability = rel
	m.cityProfiles[p.AccountID] = p
	hist := append(m.cityProfileHist[p.AccountID], p)
	hist = DownsampleHistory(hist)
	m.cityProfileHist[p.AccountID] = hist
	m.mu.Unlock()
	return m.persist()
}

// DownsampleHistory keeps the series under cityProfileHistCap by removing, when over cap, exactly ONE existing
// sample: among all samples EXCEPT the newest cityProfileHistKeepRecent, the one whose time gap to its PREVIOUS
// neighbor is smallest (the most redundant, closely-spaced old point). This always retains the newest points and
// thins the oldest, most-redundant ones first → an all-time view dense recently and sparse long ago. Ties remove
// the older (lower-index) sample. The input is assumed time-ordered oldest→newest.
func DownsampleHistory(hist []CityProfile) []CityProfile {
	if len(hist) <= cityProfileHistCap {
		return hist
	}
	// Candidates: indices [1 .. len-keepRecent). Index 0 has no previous neighbor; the newest keepRecent are kept.
	limit := len(hist) - cityProfileHistKeepRecent
	bestIdx := -1
	var bestGap time.Duration
	for i := 1; i < limit; i++ {
		gap := hist[i].ReportedAt.Sub(hist[i-1].ReportedAt)
		if bestIdx == -1 || gap < bestGap {
			bestIdx = i
			bestGap = gap
		}
	}
	if bestIdx == -1 {
		// Degenerate (keepRecent >= len-1): nothing eligible to thin; drop the oldest to honour the cap.
		bestIdx = 0
	}
	return append(hist[:bestIdx], hist[bestIdx+1:]...)
}

// CityProfileOf returns the latest stored city profile for an account and whether one exists.
func (m *Memory) CityProfileOf(accountID string) (CityProfile, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.cityProfiles[accountID]
	return p, ok
}

// CityProfileHistory returns a copy of an account's retained city snapshots (oldest→newest), or empty.
func (m *Memory) CityProfileHistory(accountID string) []CityProfile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	hist := m.cityProfileHist[accountID]
	return append([]CityProfile(nil), hist...)
}

// NetCentsSeries walks the league's settlement events in Seq/time order and returns the account's cumulative
// net-§ curve: a NetPoint at each event touching the account (running net += Cents as receiver, −= Cents as
// payer). Empty if no event touches the account. Same data AuditLeague reduces to a total — here as a curve.
func (m *Memory) NetCentsSeries(leagueID, accountID string) []NetPoint {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []NetPoint
	var running int64
	for _, e := range m.events {
		if e.LeagueID != leagueID {
			continue
		}
		touched := false
		if e.ReceiverID == accountID {
			running += e.Cents
			touched = true
		}
		if e.PayerID == accountID {
			running -= e.Cents
			touched = true
		}
		if touched {
			out = append(out, NetPoint{TS: e.Created, Cents: running})
		}
	}
	return DownsampleNetSeries(out)
}

// netSeriesCap bounds the returned net-§ curve. A long-lived, high-trade league can accumulate far more settlement
// events than the snapshot ring (150), and the client parses + plots this on the net35 main thread — so stride it
// down to a chartable size, always keeping the first and last point (the true endpoints of the curve).
const netSeriesCap = 150

func DownsampleNetSeries(pts []NetPoint) []NetPoint {
	if len(pts) <= netSeriesCap {
		return pts
	}
	out := make([]NetPoint, netSeriesCap)
	last := len(pts) - 1
	for k := 0; k < netSeriesCap; k++ {
		out[k] = pts[k*last/(netSeriesCap-1)] // k=0→first, k=cap-1→last; evenly strided in between
	}
	return out
}
