package pricing

import (
	"testing"

	"openmarkets/server/internal/market"
)

func TestUnitPriceCents(t *testing.T) {
	// Oil base 400 (scaled units; cents = price/100). At index 1.0 → 4 cents/unit; 2.0 → 8; 0.5 → 2.
	cases := []struct {
		base  int64
		index float64
		want  int64
	}{
		{400, 1.0, 4},
		{400, 2.0, 8},
		{400, 0.5, 2},
		{150, 1.0, 2},     // Coal 150 → 1.5 → round half-up → 2
		{10000, 1.0, 100}, // LuxuryProducts → §1.00/unit
		{400, 0, 4},       // non-positive index → neutral 1.0
	}
	for _, c := range cases {
		if got := UnitPriceCents(c.base, c.index); got != c.want {
			t.Errorf("UnitPriceCents(%d,%v) = %d, want %d", c.base, c.index, got, c.want)
		}
	}
}

func TestNewPricer(t *testing.T) {
	// No reports → neutral index 1.0 → Oil 400 → 4 cents.
	p := NewPricer(func(string) ([]market.Report, error) { return nil, nil },
		func() map[string]float64 { return nil }, // no active events
		market.Params{VolumeRef: 20000, Min: 0.5, Max: 2.0},
		nil)
	if v, ok := p("L", "Oil"); !ok || v != 4 {
		t.Errorf("Oil neutral = (%d,%v), want (4,true)", v, ok)
	}
	if _, ok := p("L", "Unobtainium"); ok {
		t.Errorf("unknown commodity should be !ok")
	}

	// A +50% global Oil event folds into the accept price: 400 × (1.0 × 1.5) / 100 = 6 cents.
	pe := NewPricer(func(string) ([]market.Report, error) { return nil, nil },
		func() map[string]float64 { return map[string]float64{"Oil": 1.5} },
		market.Params{VolumeRef: 20000, Min: 0.5, Max: 2.0},
		nil)
	if v, ok := pe("L", "Oil"); !ok || v != 6 {
		t.Errorf("Oil with +50%% event = (%d,%v), want (6,true)", v, ok)
	}
}
