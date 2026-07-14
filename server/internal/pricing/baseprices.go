// Package pricing holds the server-side commodity base-price table and the accept-time valuation.
//
// It MIRRORS the client's OpenMarkets/Data/Commodities.cs base prices (keyed by the canonical wire key = the
// CS1 TransferReason enum name). Keeping the authoritative table server-side lets the server freeze a trade's
// line values itself (base price × league index) instead of trusting an offerer-supplied price — closing the
// valuation-manipulation hole (Codex #7). Keep this in lockstep with the client table.
package pricing

import (
	"math"

	"openmarkets/server/internal/market"
)

// BasePrices maps the wire commodity key to its base price in the game's scaled index unit (the same unit as a
// contract's UnitPrice, where cents = qty × price / 100). Mirror of Commodities.cs.
var BasePrices = map[string]int64{
	// Base game
	"Oil": 400, "Ore": 300, "Logs": 200, "Grain": 200, "Coal": 150,
	"Petrol": 500, "Food": 400, "Lumber": 300, "Goods": 600,
	// Sunset Harbor DLC
	"Fish": 600,
	// Industries DLC
	"AnimalProducts": 1500, "Flours": 1500, "Paper": 1500, "PlanedTimber": 1500,
	"Petroleum": 3000, "Plastics": 3000, "Glass": 2250, "Metals": 2250, "LuxuryProducts": 10000,
}

// DefaultBasePrice mirrors the client's fallback for an unlisted commodity.
const DefaultBasePrice int64 = 300

// displayNames maps a wire key to the player-facing label (mirror of the client's Commodities.cs). Only keys whose
// label differs from the wire key need an entry; DisplayName falls back to the wire key for the rest. Used by the
// crisis scheduler to render a human-readable crisis name ("The Animal Products Blight", not "AnimalProducts").
var displayNames = map[string]string{
	"AnimalProducts": "Animal Products",
	"Flours":         "Flour",
	"PlanedTimber":   "Planed Timber",
	"LuxuryProducts": "Luxury Products",
}

// DisplayName returns the player-facing label for a wire commodity key (falling back to the key itself).
func DisplayName(commodity string) string {
	if d, ok := displayNames[commodity]; ok {
		return d
	}
	return commodity
}

// BasePrice returns the base price for a wire key, and whether it is a known commodity.
func BasePrice(commodity string) (int64, bool) {
	p, ok := BasePrices[commodity]
	return p, ok
}

// UnitPriceCents converts a base price and a league index multiplier (typically 0.5–2.0) into the frozen unit
// price in CENTS per whole unit, for money.LineValueCents. The base price is in scaled index units where
// cents = price/100, so unitPriceCents = round(base × index / 100). Rounds half-up; never negative.
func UnitPriceCents(basePrice int64, index float64) int64 {
	if index <= 0 {
		index = 1.0 // neutral if the league has no signal for this commodity
	}
	v := float64(basePrice) * index / 100.0
	if v < 0 {
		v = 0
	}
	return int64(math.Floor(v + 0.5)) // round half-up
}

// Commodities returns the known commodity wire keys (the base-price table) — the set the M9 global shock generator
// rolls over. Order is unspecified.
func Commodities() []string {
	out := make([]string, 0, len(BasePrices))
	for c := range BasePrices {
		out = append(out, c)
	}
	return out
}

// NewPricer builds the accept-time pricer: base price × the league's current EFFECTIVE index, in cents/whole unit.
// reports supplies a league's net-supply reports (the per-commodity elasticity index); events supplies the GLOBAL
// per-commodity price-shock multipliers (M9) folded on top, so a contract is priced at the shocked index AT ACCEPT
// (active contracts are already frozen, so a shock only affects deals accepted during it). The returned func matches
// store.Pricer and is lock-free w.r.t. the store mutex (reports/events are served under the store's read lock — safe
// because the trade accept path frees its write lock before pricing).
func NewPricer(reports func(leagueID string) ([]market.Report, error), events func() map[string]float64, p market.Params, shields func(leagueID string) []market.Shield) func(string, string) (int64, bool) {
	return func(leagueID, commodity string) (int64, bool) {
		base, ok := BasePrice(commodity)
		if !ok {
			return 0, false
		}
		index := 1.0 // neutral if the league has no signal for this commodity
		if rs, err := reports(leagueID); err == nil {
			var activeShields []market.Shield
			if shields != nil {
				activeShields = shields(leagueID)
			}
			if idx := market.CommodityIndicesWithShields(rs, p, activeShields); idx != nil {
				if v, present := idx[commodity]; present {
					index = v
				}
			}
		}
		if events != nil { // fold the global price shock into the accept-time index
			index = market.ApplyEvent(index, events()[commodity], p)
		}
		return UnitPriceCents(base, index), true
	}
}
