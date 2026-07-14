// Package market holds the pure price-index aggregation. No I/O, no state — given a set of member
// reports it returns clamped indices. This is the economic core of the shared feed (M4 Phase A) and
// is exercised exhaustively by index_test.go.
package market

// Report is one member's latest net supply/demand for one commodity within a league.
// NetSupply > 0 means the city is a net EXPORTER (it floods the shared market → pushes the price DOWN);
// NetSupply < 0 means a net IMPORTER (it drains the market → pushes the price UP). Mirrors the sign
// convention the client uses for elasticity (export pushes price down).
type Report struct {
	AccountID string
	Commodity string
	NetSupply float64
}

// Shield dampens one account's contribution to the market index for one commodity. DampeningBips is a percentage
// in basis points: 3000 means the report moves the shared index 30% less, so 70% of NetSupply is counted.
type Shield struct {
	AccountID     string
	Commodity     string
	DampeningBips int
}

// Params controls how net supply maps to an index and the hard clamp bounds. These mirror the C#
// client (VolumeRef 20000, MinIndex 0.5, MaxIndex 2.0) so server and client agree on the scale.
type Params struct {
	VolumeRef float64 // net units for a full one-sided swing
	Min       float64 // index floor (must be > 0)
	Max       float64 // index ceiling
}

// CommodityIndices reduces a league's reports to a clamped index per commodity. The index is
//
//	1 - (sumNetSupply / VolumeRef)
//
// clamped to [Min, Max]: a net-supplied commodity (positive sum) drops below 1.0 (cheaper), a
// net-demanded one rises above 1.0 (dearer). Reports for the same commodity are summed across members,
// so a friend group's collective behaviour moves the shared price. Empty input → empty map.
func CommodityIndices(reports []Report, p Params) map[string]float64 {
	return CommodityIndicesWithShields(reports, p, nil)
}

// CommodityIndicesWithShields is CommodityIndices plus marketShield dampening. If several shields target the same
// account+commodity, the strongest one wins; stacking remains bounded and deterministic.
func CommodityIndicesWithShields(reports []Report, p Params, shields []Shield) map[string]float64 {
	p = p.sane()
	shieldByKey := map[string]int{}
	for _, s := range shields {
		if s.AccountID == "" || s.Commodity == "" || s.DampeningBips <= 0 {
			continue
		}
		bips := s.DampeningBips
		if bips > 10000 {
			bips = 10000
		}
		key := s.AccountID + "\x00" + s.Commodity
		if bips > shieldByKey[key] {
			shieldByKey[key] = bips
		}
	}
	sum := make(map[string]float64)
	for _, r := range reports {
		if r.Commodity == "" {
			continue
		}
		net := r.NetSupply
		if r.AccountID != "" {
			if bips := shieldByKey[r.AccountID+"\x00"+r.Commodity]; bips > 0 {
				net *= 1.0 - float64(bips)/10000.0
			}
		}
		sum[r.Commodity] += net
	}
	out := make(map[string]float64, len(sum))
	for commodity, net := range sum {
		out[commodity] = clamp(1.0-net/p.VolumeRef, p.Min, p.Max)
	}
	return out
}

// MarketIndex collapses the per-commodity indices into a single market-wide number for the legacy
// single-float client (PriceFeedDto.index). It is the plain mean of the per-commodity indices, itself
// clamped for safety. With no commodities it returns the neutral 1.0 (no nudge). This is a coarse
// signal; the per-commodity map from CommodityIndices is the real one and is what a future richer
// client should consume.
//
// NOTE (marketShield): this helper is the pure UNSHIELDED mean and is retained for tests only — it has no
// production caller. The live /index handler (api.handleIndex) computes its mean from the EFFECTIVE,
// shield-AWARE indices (CommodityIndicesWithShields + EffectiveIndices), so the shipped legacy feed already
// respects marketShield. Do not route production callers through this function — use the shield-aware path.
func MarketIndex(reports []Report, p Params) float64 {
	p = p.sane()
	idx := CommodityIndices(reports, p)
	if len(idx) == 0 {
		return 1.0
	}
	var total float64
	for _, v := range idx {
		total += v
	}
	return clamp(total/float64(len(idx)), p.Min, p.Max)
}

// sane fills in defensible defaults so a zero-value or misconfigured Params can't divide by zero or
// invert the clamp. Pure; returns a corrected copy.
func (p Params) sane() Params {
	if p.VolumeRef <= 0 {
		p.VolumeRef = 20000
	}
	if p.Min <= 0 {
		p.Min = 0.5
	}
	if p.Max < p.Min {
		p.Max = p.Min
	}
	return p
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
