package projects

import (
	"math/rand"
	"strings"
	"testing"

	"openmarkets/server/internal/store"
)

// identityNamer is a trivial DisplayNamer for tests (wire key → itself).
func identityNamer(c string) string { return c }

// TestGenerate_ScalesAndFills verifies the generator scales requirements to member count, draws only from the
// intersected commodity pool, fills the flavor seam, and carries the buff fields through.
func TestGenerate_ScalesAndFills(t *testing.T) {
	tmpl := Template{
		NameFmt: "The Grand {Commodity} Exchange", DescFmt: "Built from {Commodities}.",
		Picks: 2, QtyPer: 10, GoldReq: 1000, BuffKind: "work", BuffMag: 5000, BuffDays: 7,
		TradeReward: store.EffectMarketShield, TradePctBips: 3000,
		Pool: []string{"Oil", "Coal", "Goods", "Unobtainium"},
	}
	rng := rand.New(rand.NewSource(1))
	pool := []string{"Oil", "Coal", "Goods", "Food"} // league's real set — "Unobtainium" not present
	p := Generate(tmpl, 4, rng, pool, identityNamer)

	if p.Name == "" || !strings.Contains(p.Name, "Exchange") {
		t.Fatalf("name not filled: %q", p.Name)
	}
	if len(p.Reqs) != 2 {
		t.Fatalf("picks = %d want 2", len(p.Reqs))
	}
	for _, r := range p.Reqs {
		if r.Commodity == "Unobtainium" {
			t.Fatalf("drew a commodity outside the league set: %s", r.Commodity)
		}
		if r.Qty != 10*4 {
			t.Fatalf("qty not scaled to members: %d want 40", r.Qty)
		}
	}
	if p.GoldReqCents != 1000*4 {
		t.Fatalf("gold req not scaled: %d want 4000", p.GoldReqCents)
	}
	if p.BuffKind != "work" || p.BuffMagnitudeCents != 5000 || p.BuffDays != 7 {
		t.Fatalf("buff fields not carried: %+v", p)
	}
	if p.TradeRewardKind != store.EffectMarketShield || p.TradeRewardCommodity == "" || p.TradeRewardPctBips != 3000 {
		t.Fatalf("trade reward fields not carried: %+v", p)
	}
}

// TestGenerate_NoOverlap returns an empty Project when the template pool and the league set don't intersect, so
// the caller can skip it.
func TestGenerate_NoOverlap(t *testing.T) {
	tmpl := Template{NameFmt: "X", Picks: 1, QtyPer: 1, Pool: []string{"Unobtainium"}}
	p := Generate(tmpl, 2, rand.New(rand.NewSource(1)), []string{"Oil"}, identityNamer)
	if p.Name != "" || len(p.Reqs) != 0 {
		t.Fatalf("expected empty project on no overlap, got %+v", p)
	}
}

// TestBank_PoolsAreSane guards every bank template against an empty pool / non-positive picks (a config typo).
func TestBank_PoolsAreSane(t *testing.T) {
	for i, tm := range Bank {
		if len(tm.Pool) == 0 {
			t.Fatalf("bank[%d] %q has an empty pool", i, tm.NameFmt)
		}
		if tm.Picks < 1 {
			t.Fatalf("bank[%d] %q picks < 1", i, tm.NameFmt)
		}
	}
}
