package main

import (
	"io"
	"log"
	"math/rand"
	"testing"

	"openmarkets/server/internal/market"
	"openmarkets/server/internal/store"
)

func newTestCrisis(t *testing.T) (*crisisScheduler, *store.Memory) {
	t.Helper()
	st := store.NewMemory("")
	cs := newCrisisScheduler(st, 0, 1.0, []string{"Oil", "Coal", "Goods"}, log.New(io.Discard, "", 0))
	cs.rng = rand.New(rand.NewSource(1)) // deterministic
	return cs, st
}

// seed a league so AppendChronicle/AllLeagues have something to write to.
func seedLeague(t *testing.T, st *store.Memory) string {
	t.Helper()
	a, _, _ := st.CreateAccount()
	lg, err := st.CreateLeague(a.ID, "L")
	if err != nil {
		t.Fatalf("create league: %v", err)
	}
	return lg.ID
}

// TestCrisis_StartThenEnd: with chance=1 a tick starts a crisis (SetEvent + a "crisis" chronicle line in the
// league); clearing the event makes the next tick narrate the end ("crisis-end").
func TestCrisis_StartThenEnd(t *testing.T) {
	cs, st := newTestCrisis(t)
	lid := seedLeague(t, st)
	baseSeq := int64(len(st.Chronicle(lid, 0, 1000)))

	cs.tick() // starts one crisis (chance=1)
	if len(cs.active) != 1 {
		t.Fatalf("expected 1 active crisis, got %d", len(cs.active))
	}
	var commodity, name string
	for c, n := range cs.active {
		commodity, name = c, n
	}
	// EventStates carries the named crisis.
	if e := st.EventStates()[commodity]; e.Name != name || e.Mult <= 0 {
		t.Fatalf("SetEvent crisis missing: %+v", e)
	}
	// A start chronicle ("crisis") landed in the league.
	chron := st.Chronicle(lid, 0, 1000)
	if int64(len(chron)) <= baseSeq || chron[len(chron)-1].Kind != "crisis" {
		t.Fatalf("expected a crisis chronicle line, got %+v", chron)
	}

	// Force the event to clear (set a 1-tick event, then advance it to 0 → removed), then tick → the end is
	// narrated and the crisis leaves the active set.
	st.SetEvent(commodity, market.EventState{Mult: 1.0, TicksLeft: 1})
	st.AdvanceEvents() // 1→0 → removed
	if _, ok := st.EventStates()[commodity]; ok {
		t.Fatalf("event should have cleared before end-detection")
	}

	cs.chance = 0 // don't start a new one this tick — isolate the end narration
	cs.tick()
	if len(cs.active) != 0 {
		t.Fatalf("cleared crisis should leave the active set, got %d", len(cs.active))
	}
	chron = st.Chronicle(lid, 0, 1000)
	if chron[len(chron)-1].Kind != "crisis-end" {
		t.Fatalf("expected a crisis-end chronicle line, got %+v", chron[len(chron)-1])
	}
}

// TestCrisis_SeedDoesNotRenarrate: a crisis already present at boot is adopted (active) but NOT re-narrated; when it
// later clears, the end IS narrated.
func TestCrisis_SeedAdoptsExisting(t *testing.T) {
	cs, st := newTestCrisis(t)
	lid := seedLeague(t, st)
	st.SetEvent("Oil", market.EventState{Mult: 1.6, TicksLeft: 3, Name: "The Oil Blight", Kind: "crisis"})
	before := len(st.Chronicle(lid, 0, 1000))

	cs.seed()
	if cs.active["Oil"] != "The Oil Blight" {
		t.Fatalf("seed did not adopt the existing crisis: %+v", cs.active)
	}
	if after := len(st.Chronicle(lid, 0, 1000)); after != before {
		t.Fatalf("seed re-narrated a pre-existing crisis (chronicle grew %d→%d)", before, after)
	}
}

// TestCrisis_NoDoubleOnBusyCommodity: maybeStart never picks a commodity that already has an event (named or not).
func TestCrisis_SkipsBusyCommodity(t *testing.T) {
	cs, st := newTestCrisis(t)
	seedLeague(t, st)
	// Occupy every commodity with an unnamed shock → no free commodity → no crisis starts.
	for _, c := range cs.commodities {
		st.SetEvent(c, market.EventState{Mult: 1.2, TicksLeft: 5})
	}
	cs.maybeStart(st.EventStates())
	if len(cs.active) != 0 {
		t.Fatalf("started a crisis on a busy commodity: %+v", cs.active)
	}
}
