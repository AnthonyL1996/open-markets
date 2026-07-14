// Package projects holds the curated BANK of Great-Work templates and the generator that turns a template into a
// concrete store.Project scaled to a league's size. It is intentionally LLM-ready: the human-facing flavor (the
// project name + description) is produced behind a single small `flavor` seam, so a future LLM generator can drop
// in richer, context-aware narration without touching the requirement-scaling math.
package projects

import (
	"math/rand"
	"strings"

	"openmarkets/server/internal/store"
)

// Template is one curated kind of Great Work. Commodities is the POOL the generator picks a few requirement
// commodities from (filtered against the league's real tradable set); QtyPer is the per-member base unit
// requirement (scaled by member count); GoldReq is the per-member base § requirement in cents (0 = no § req).
// BuffKind/BuffMag/BuffDays describe the lasting reward: BuffMag is a SYNTHETIC CostCents fed to
// store.InvestBuffMagnitude (so the buff magnitude derives + caps exactly like the investment-office buff).
type Template struct {
	NameFmt      string   // "The Grand {Commodity} Exchange" — {Commodity} is replaced by the first pick's display name
	DescFmt      string   // a sentence; {Commodity} / {Commodities} are filled by flavor()
	Picks        int      // how many requirement commodities to draw from the pool (clamped to what's available)
	QtyPer       int64    // base units required PER MEMBER, per commodity
	GoldReq      int64    // base § (cents) required PER MEMBER (0 = none)
	BuffKind     string   // store.Demand{Residential,Commercial,Workplace}
	BuffMag      int64    // synthetic CostCents → store.InvestBuffMagnitude
	BuffDays     int      // the buff lifetime in due-cycle ticks (≈ in-game days)
	TradeReward  string   // optional store.EffectMarketShield (server-applied) or store.EffectPriceEdge (client-applied export bonus)
	TradePctBips int      // basis points for the trade reward magnitude
	Pool         []string // commodity wire-keys this Work can draw requirements from (filtered to the league's set)
}

// Bank is the curated set of Great Works. A few varied flavors spanning different commodity pools, buff channels,
// and magnitudes. Tunable. The Pool entries are the canonical CS1 commodity wire-keys; Generate intersects each
// template's Pool with the league's actual tradable set so a template never requires a commodity the world lacks.
//
// BuffMag CALIBRATION (important): each BuffMag is a synthetic CostCents fed to store.InvestBuffMagnitude, which
// caps demand at InvestDemandBoostCap (20) and attractiveness at InvestAttractRateCap (500) — and BOTH caps are
// reached at exactly 5_000_000 cents. So every BuffMag MUST stay in the sub-cap band [2_500_000, 5_000_000] or the
// projects collapse to an identical max buff (the bug this calibration fixed). Within that band the reward scales
// linearly and the six Works stay distinct: demand 10→20, attractiveness 250→500 (attract = demand × 25), ordered
// weakest→strongest Granary < Exchange < Foundry < Refinery < Rail < Arcology. Do NOT raise any value past 5M.
//
// Trade rewards (themed on the Work's FIRST chosen commodity): heavy-industry Works grant marketShield
// (server-enforced, conservation-neutral — dampens index impact so you can dump exports without crashing your
// own price); commercial Works grant priceEdge (client-applied — sell the themed commodity dearer vs. the void).
// priceEdge rides the SAME /citystate effects payload and is conservation-safe: void-sourced export income has no
// counterparty, and peer contract settlement is a separate booking path the edge never touches.
var Bank = []Template{
	{
		NameFmt: "The Grand {Commodity} Exchange",
		DescFmt: "A monumental trading hall for {Commodity}. Stock its vaults and every builder's commerce thrives.",
		Picks:   1, QtyPer: 40, GoldReq: 2_000_000,
		BuffKind: store.DemandCommercial, BuffMag: 3_000_000, BuffDays: 10,
		TradeReward: store.EffectPriceEdge, TradePctBips: 800,
		Pool: []string{"Goods", "Oil", "Petrol", "Coal", "Lumber", "Food"},
	},
	{
		NameFmt: "Trans-League Rail",
		DescFmt: "A great railway binding the league together, built from {Commodities}.",
		Picks:   2, QtyPer: 25, GoldReq: 3_000_000,
		BuffKind: store.DemandWorkplace, BuffMag: 4_500_000, BuffDays: 12,
		TradeReward: store.EffectMarketShield, TradePctBips: 3000,
		Pool: []string{"Coal", "Ore", "Metals", "Lumber", "Goods", "Logs"},
	},
	{
		NameFmt: "The Central Foundry",
		DescFmt: "A colossal foundry forging the league's future from {Commodities}.",
		Picks:   2, QtyPer: 30, GoldReq: 2_500_000,
		BuffKind: store.DemandWorkplace, BuffMag: 3_500_000, BuffDays: 10,
		TradeReward: store.EffectMarketShield, TradePctBips: 3000,
		Pool: []string{"Ore", "Metals", "Coal", "Oil", "Petrol"},
	},
	{
		NameFmt: "The People's Granary",
		DescFmt: "A vast granary against lean years, filled with {Commodities}.",
		Picks:   2, QtyPer: 35, GoldReq: 1_500_000,
		BuffKind: store.DemandResidential, BuffMag: 2_500_000, BuffDays: 14,
		Pool: []string{"Food", "Grain", "Fish", "Lumber"},
	},
	{
		NameFmt: "The Skyline Arcology",
		DescFmt: "A towering arcology that lifts the whole league, raised from {Commodities}.",
		Picks:   3, QtyPer: 20, GoldReq: 4_000_000,
		BuffKind: store.DemandResidential, BuffMag: 5_000_000, BuffDays: 12,
		Pool: []string{"Goods", "Metals", "Lumber", "Ore", "Food"},
	},
	{
		NameFmt: "The Grand Refinery",
		DescFmt: "A sprawling refinery that powers the league, fed by {Commodities}.",
		Picks:   2, QtyPer: 28, GoldReq: 3_500_000,
		BuffKind: store.DemandCommercial, BuffMag: 4_000_000, BuffDays: 11,
		TradeReward: store.EffectPriceEdge, TradePctBips: 1000,
		Pool: []string{"Oil", "Petrol", "Coal", "Ore"},
	},
}

// DisplayNamer resolves a commodity wire-key to its player-facing name (pricing.DisplayName). Injected so the
// projects package stays decoupled from the pricing package.
type DisplayNamer func(commodity string) string

// Generate turns a template into a concrete, OPEN-shaped store.Project scaled to leagueMemberCount, drawing its
// requirement commodities from the template's pool intersected with commodityPool (the league's real tradable
// set). Requirement quantities and the § requirement scale linearly with member count (min 1). The Name +
// Description come from the flavor() seam. The returned Project has no id/status (CreateProject assigns them).
//
// If the intersected pool is empty (no overlap), Generate returns a zero Project with an empty Name — the caller
// should skip it (defensive; with the canonical pools + the real commodity set this won't happen in practice).
func Generate(t Template, leagueMemberCount int, rng *rand.Rand, commodityPool []string, displayName DisplayNamer) store.Project {
	members := leagueMemberCount
	if members < 1 {
		members = 1
	}
	// Intersect the template's pool with the league's tradable set, preserving the template-pool order, then shuffle.
	set := map[string]bool{}
	for _, c := range commodityPool {
		set[c] = true
	}
	var avail []string
	for _, c := range t.Pool {
		if set[c] {
			avail = append(avail, c)
		}
	}
	if len(avail) == 0 {
		return store.Project{} // no overlap — caller skips
	}
	rng.Shuffle(len(avail), func(i, j int) { avail[i], avail[j] = avail[j], avail[i] })
	picks := t.Picks
	if picks < 1 {
		picks = 1
	}
	if picks > len(avail) {
		picks = len(avail)
	}
	chosen := avail[:picks]

	reqs := make([]store.ProjectReq, 0, picks)
	for _, c := range chosen {
		qty := t.QtyPer * int64(members)
		if qty < 1 {
			qty = 1
		}
		reqs = append(reqs, store.ProjectReq{Commodity: c, Qty: qty})
	}
	goldReq := t.GoldReq * int64(members)
	tradeCommodity := ""
	if t.TradeReward != "" && t.TradePctBips > 0 {
		tradeCommodity = chosen[0]
	}

	name, desc := flavor(t, chosen, displayName)
	return store.Project{
		Name:                 name,
		Description:          desc,
		Reqs:                 reqs,
		GoldReqCents:         goldReq,
		BuffKind:             t.BuffKind,
		BuffMagnitudeCents:   t.BuffMag,
		BuffDays:             t.BuffDays,
		TradeRewardKind:      t.TradeReward,
		TradeRewardCommodity: tradeCommodity,
		TradeRewardPctBips:   t.TradePctBips,
	}
}

// flavor produces the project's Name + Description from a template and its chosen requirement commodities. This is
// the LLM HOOK SEAM: today it's a deterministic fill of {Commodity}/{Commodities} placeholders, but an LLM
// generator can replace this single function to produce richer, context-aware narration without touching the
// requirement-scaling math in Generate.
//
// TODO: LLM hook — swap this deterministic fill for a model call that takes the template + chosen commodities (and
// optionally league context) and returns a bespoke name/description. Keep the same signature so Generate is unchanged.
func flavor(t Template, chosen []string, displayName DisplayNamer) (name, desc string) {
	names := make([]string, len(chosen))
	for i, c := range chosen {
		names[i] = displayName(c)
	}
	first := ""
	if len(names) > 0 {
		first = names[0]
	}
	list := joinHuman(names)
	name = strings.Replace(t.NameFmt, "{Commodity}", first, -1)
	name = strings.Replace(name, "{Commodities}", list, -1)
	desc = strings.Replace(t.DescFmt, "{Commodity}", first, -1)
	desc = strings.Replace(desc, "{Commodities}", list, -1)
	return name, desc
}

// joinHuman renders a list as "A", "A and B", or "A, B, and C".
func joinHuman(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
	}
}
