package store

import (
	"math/rand"
	"testing"

	"openmarkets/server/internal/market"
)

// A global price shock folds into a league's effective index, is sampled into the history ring, and decays away.
func TestMarketState_EventsFoldAndHistorySamples(t *testing.T) {
	m := NewMemory("")
	m.SetMarketParams(market.Params{VolumeRef: 20000, Min: 0.5, Max: 2.0}, []string{"Oil", "Ore"})
	m.eventParams.ChancePerTick = 0 // isolate: no NEW shocks start during the test
	m.SetEventRand(rand.New(rand.NewSource(1)))

	acc, _, err := m.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	lg, err := m.CreateLeague(acc.ID, "L")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.PutReport(Report{AccountID: acc.ID, LeagueID: lg.ID, Commodity: "Oil", NetSupply: 0}); err != nil {
		t.Fatal(err) // NetSupply 0 → neutral elasticity 1.0, but Oil now appears in the index map
	}

	// Inject a +50% global Oil shock for 2 ticks (in-package).
	m.priceEvents["Oil"] = market.EventState{Mult: 1.5, TicksLeft: 2}

	// EffectiveIndices folds the event: Oil 1.0 × 1.5 = 1.5.
	if eff := m.EffectiveIndices(lg.ID); !approx(eff["Oil"], 1.5) {
		t.Fatalf("Oil effective index: want 1.5, got %v", eff["Oil"])
	}
	if m.EventStates()["Oil"].Mult != 1.5 {
		t.Fatalf("EventStates should report the Oil shock")
	}

	// Sample history twice → the ring holds 2 samples of the effective index.
	m.SampleHistory()
	m.SampleHistory()
	hist := m.IndexHistory(lg.ID)
	if len(hist["Oil"]) != 2 {
		t.Fatalf("Oil history should have 2 samples, got %d", len(hist["Oil"]))
	}
	for _, v := range hist["Oil"] {
		if !approx(v, 1.5) {
			t.Fatalf("history sample should be the effective 1.5, got %v", v)
		}
	}

	// AdvanceEvents decays the shock (no new shocks since ChancePerTick=0): 2 → 1 → gone.
	m.AdvanceEvents()
	if m.priceEvents["Oil"].TicksLeft != 1 {
		t.Fatalf("after 1 tick, TicksLeft want 1, got %d", m.priceEvents["Oil"].TicksLeft)
	}
	m.AdvanceEvents()
	if _, ok := m.priceEvents["Oil"]; ok {
		t.Fatalf("shock should have decayed away")
	}
	if eff := m.EffectiveIndices(lg.ID); eff["Oil"] != 1.0 {
		t.Fatalf("post-decay Oil index: want 1.0, got %v", eff["Oil"])
	}
}

func approx(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }
