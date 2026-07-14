package market

import (
	"math/rand"
	"testing"
)

func TestStepEvents_DecaysAndClears(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	p := DefaultEventParams()
	p.ChancePerTick = 0 // no new shocks — isolate decay
	cur := map[string]EventState{"Oil": {Mult: 1.4, TicksLeft: 2}}

	cur = StepEvents(cur, []string{"Oil"}, p, rng)
	if e := cur["Oil"]; e.TicksLeft != 1 || e.Mult != 1.4 {
		t.Fatalf("after 1 tick: want {1.4,1}, got %+v", e)
	}
	cur = StepEvents(cur, []string{"Oil"}, p, rng)
	if _, ok := cur["Oil"]; ok {
		t.Fatalf("shock should have cleared at TicksLeft 1→0, got %+v", cur["Oil"])
	}
}

// TestStepEvents_DecayPreservesCrisisName asserts the crisis identity (Name/Narrative/Kind) survives a decay tick
// — the scheduler relies on a named event keeping its name until it fully clears.
func TestStepEvents_DecayPreservesCrisisName(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	p := DefaultEventParams()
	p.ChancePerTick = 0 // isolate decay — no new shocks
	cur := map[string]EventState{"Oil": {
		Mult: 1.6, TicksLeft: 3, Name: "The Oil Blight", Narrative: "A blight…", Kind: "crisis",
	}}
	cur = StepEvents(cur, []string{"Oil"}, p, rng)
	e := cur["Oil"]
	if e.TicksLeft != 2 || e.Mult != 1.6 {
		t.Fatalf("after decay: want {1.6,2}, got %+v", e)
	}
	if e.Name != "The Oil Blight" || e.Narrative != "A blight…" || e.Kind != "crisis" {
		t.Fatalf("decay dropped crisis identity: %+v", e)
	}
}

func TestStepEvents_StartsBoundedShocks(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	p := DefaultEventParams()
	p.ChancePerTick = 1.0 // guarantee a shock starts on every idle commodity
	commodities := []string{"Oil", "Ore", "Coal", "Goods", "Food"}

	ev := StepEvents(nil, commodities, p, rng)
	if len(ev) != len(commodities) {
		t.Fatalf("every commodity should get a shock, got %d/%d", len(ev), len(commodities))
	}
	for c, e := range ev {
		// Magnitude in one of the two ranges.
		inUp := e.Mult >= p.MinUp && e.Mult <= p.MaxUp
		inDown := e.Mult >= p.MinDown && e.Mult <= p.MaxDown
		if !inUp && !inDown {
			t.Fatalf("%s mult %v out of both shock ranges", c, e.Mult)
		}
		if e.TicksLeft < p.MinTicks || e.TicksLeft > p.MaxTicks {
			t.Fatalf("%s duration %d out of [%d,%d]", c, e.TicksLeft, p.MinTicks, p.MaxTicks)
		}
	}
	// An already-active commodity is not restarted (count stays stable, no panic).
	ev2 := StepEvents(ev, commodities, p, rng)
	if len(ev2) > len(commodities) {
		t.Fatalf("active commodities must not stack, got %d", len(ev2))
	}
}

func TestApplyEvent_Clamps(t *testing.T) {
	p := Params{VolumeRef: 20000, Min: 0.5, Max: 2.0}
	// 1.8 × 1.5 = 2.7 → clamped to Max 2.0.
	if got := ApplyEvent(1.8, 1.5, p); got != 2.0 {
		t.Fatalf("spike clamp: want 2.0, got %v", got)
	}
	// 0.8 × 0.5 = 0.4 → clamped to Min 0.5.
	if got := ApplyEvent(0.8, 0.5, p); got != 0.5 {
		t.Fatalf("slump clamp: want 0.5, got %v", got)
	}
	// neutral multiplier on mult<=0.
	if got := ApplyEvent(1.3, 0, p); got != 1.3 {
		t.Fatalf("neutral: want 1.3, got %v", got)
	}
}

func TestEffectiveIndices_AppliesAndKeeps(t *testing.T) {
	p := Params{VolumeRef: 20000, Min: 0.5, Max: 2.0}
	elasticity := map[string]float64{"Oil": 1.2, "Ore": 0.9}
	events := map[string]EventState{"Oil": {Mult: 1.5, TicksLeft: 3}} // only Oil shocked
	out := EffectiveIndices(elasticity, events, p)
	if d := out["Oil"] - 1.8; d > 1e-9 || d < -1e-9 { // 1.2×1.5, under the 2.0 cap
		t.Fatalf("Oil effective: want ≈1.8, got %v", out["Oil"])
	}
	if out["Ore"] != 0.9 {
		t.Fatalf("Ore (no event) should be unchanged 0.9, got %v", out["Ore"])
	}
}

func TestEventPct_SignedRounding(t *testing.T) {
	ev := map[string]EventState{"Up": {Mult: 1.374, TicksLeft: 1}, "Down": {Mult: 0.616, TicksLeft: 1}}
	if got := EventPct(ev, "Up"); got != 37 {
		t.Fatalf("up pct: want 37, got %d", got)
	}
	if got := EventPct(ev, "Down"); got != -38 {
		t.Fatalf("down pct: want -38, got %d", got)
	}
	if got := EventPct(ev, "None"); got != 0 {
		t.Fatalf("no event: want 0, got %d", got)
	}
}
