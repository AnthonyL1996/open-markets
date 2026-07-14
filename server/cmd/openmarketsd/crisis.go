package main

import (
	"log"
	"math/rand"
	"strings"
	"time"

	"openmarkets/server/internal/market"
	"openmarkets/server/internal/pricing"
	"openmarkets/server/internal/store"
)

// crisisScheduler is the always-on background narrator of SHARED LEAGUE CRISES (social slice 3): named, narrated
// economic shocks the whole league weathers together. It rides the GLOBAL price-event map (so the price effect
// hits every league at once, by M9 design) but appends a per-league Chronicle line as each crisis starts and ends.
//
// It owns a curated CRISIS BANK + an rng + the set of currently-active crisis commodities (in memory). The active
// set is SEEDED on boot from EventStates names, so the scheduler never narrates a crisis that was already running
// (a restart mid-crisis re-adopts it silently and still narrates its eventual end).
//
// Best-effort: every interval is wrapped so a single bad store call can't block or crash the loop; it stops on the
// shared shutdown signal.
type crisisScheduler struct {
	store    store.Store
	interval time.Duration
	chance   float64 // probability per interval of STARTING a crisis (config OM_CRISIS_CHANCE)
	logger   *log.Logger

	commodities []string         // the tradable wire keys a crisis can strike
	bank        []crisisTemplate // curated templates
	rng         *rand.Rand
	active      map[string]string // commodity → crisis name currently narrated as active
}

// maxActiveCrises caps how many named crises run at once (a crisis is a big, league-wide event — keep them rare).
const maxActiveCrises = 2

// crisisTemplate is one curated kind of crisis. {C} in the name/narrative is replaced with the commodity's display
// name. The magnitude range is a SIGNED percent swing (e.g. +50..+70 for a blight, -40..-55 for a glut); spike
// templates push the multiplier above 1, slump templates below 1. Duration is a tick range (≈ in-game days).
type crisisTemplate struct {
	kind      string // Chronicle/EventState Kind ("crisis" while active)
	nameTmpl  string // "The {C} Blight"
	narrative string // full sentence with {C}
	minPct    int    // inclusive signed percent (e.g. 50 or -55)
	maxPct    int    // inclusive signed percent (e.g. 70 or -40)
	minTicks  int
	maxTicks  int
}

// crisisBank is the curated set. A few flavors, each spanning a magnitude + duration range.
var crisisBank = []crisisTemplate{
	{
		kind:      "blight",
		nameTmpl:  "The {C} Blight",
		narrative: "A blight has ravaged {C} supplies — prices are soaring across the league.",
		minPct:    50, maxPct: 70, minTicks: 5, maxTicks: 7,
	},
	{
		kind:      "glut",
		nameTmpl:  "{C} Glut",
		narrative: "A massive {C} surplus has flooded the market — prices are crashing.",
		minPct:    -55, maxPct: -40, minTicks: 4, maxTicks: 6,
	},
	{
		kind:      "boom",
		nameTmpl:  "{C} Gold Rush",
		narrative: "A {C} rush grips the region — exporters are cashing in.",
		minPct:    40, maxPct: 60, minTicks: 4, maxTicks: 6,
	},
	{
		kind:      "embargo",
		nameTmpl:  "Embargo on {C}",
		narrative: "An outside embargo has choked {C} imports — it's scarce and dear.",
		minPct:    30, maxPct: 50, minTicks: 5, maxTicks: 7,
	},
}

func newCrisisScheduler(st store.Store, interval time.Duration, chance float64, commodities []string, logger *log.Logger) *crisisScheduler {
	return &crisisScheduler{
		store:       st,
		interval:    interval,
		chance:      chance,
		logger:      logger,
		commodities: append([]string(nil), commodities...),
		bank:        crisisBank,
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
		active:      map[string]string{},
	}
}

// seed adopts any crisis already running at boot (a named event in EventStates) into the active set WITHOUT
// narrating it — so a restart mid-crisis doesn't re-announce, but still narrates the eventual end.
func (cs *crisisScheduler) seed() {
	for commodity, e := range cs.store.EventStates() {
		if e.Name != "" {
			cs.active[commodity] = e.Name
		}
	}
}

// run is the interval loop. Returns when stop is closed.
func (cs *crisisScheduler) run(stop <-chan struct{}) {
	cs.seed()
	t := time.NewTicker(cs.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			cs.tickSafe()
		case <-stop:
			return
		}
	}
}

// tickSafe runs one tick, recovering from any panic so a single bad tick can never crash the loop.
func (cs *crisisScheduler) tickSafe() {
	defer func() {
		if r := recover(); r != nil {
			cs.logger.Printf("crisis: recovered from panic: %v", r)
		}
	}()
	cs.tick()
}

// tick ends any crisis whose event has cleared, then maybe starts a new one. Best-effort.
func (cs *crisisScheduler) tick() {
	states := cs.store.EventStates()
	cs.endCleared(states)
	cs.maybeStart(states)
}

// endCleared diffs the active set against current EventStates names: an active crisis whose named event is GONE
// has ended — append an end-Chronicle to every league and drop it from active.
func (cs *crisisScheduler) endCleared(states map[string]market.EventState) {
	for commodity, name := range cs.active {
		e, present := states[commodity]
		// Still active iff the same named crisis event is present (the name carries through decay).
		if present && e.Name == name {
			continue
		}
		text := "✅ The " + name + " has passed."
		// Strip a leading "The " so "The The X Blight has passed" doesn't read awkwardly — names like "{C} Glut"
		// have no article, names like "The {C} Blight" already start with "The".
		if strings.HasPrefix(name, "The ") {
			text = "✅ " + name + " has passed."
		}
		cs.appendAll("crisis-end", text)
		delete(cs.active, commodity)
	}
}

// maybeStart rolls the per-interval chance and, if it hits (and we're under the active cap), starts one crisis on a
// fresh commodity: build the named EventState, SetEvent it onto the global map, add to active, and append a
// start-Chronicle (the narrative) to every league.
func (cs *crisisScheduler) maybeStart(states map[string]market.EventState) {
	if len(cs.active) >= maxActiveCrises {
		return
	}
	if cs.chance <= 0 || cs.rng.Float64() >= cs.chance {
		return
	}
	commodity := cs.pickFreeCommodity(states)
	if commodity == "" {
		return // every commodity already has an active (named or unnamed) event
	}
	tmpl := cs.bank[cs.rng.Intn(len(cs.bank))]
	display := pricing.DisplayName(commodity)
	name := strings.Replace(tmpl.nameTmpl, "{C}", display, -1)
	narrative := strings.Replace(tmpl.narrative, "{C}", display, -1)

	// Signed percent → multiplier (e.g. +60 → 1.60, -50 → 0.50), with a random magnitude in the template's range.
	pct := tmpl.minPct
	if tmpl.maxPct > tmpl.minPct {
		pct += cs.rng.Intn(tmpl.maxPct - tmpl.minPct + 1)
	}
	mult := 1.0 + float64(pct)/100.0
	dur := tmpl.minTicks
	if tmpl.maxTicks > tmpl.minTicks {
		dur += cs.rng.Intn(tmpl.maxTicks - tmpl.minTicks + 1)
	}

	cs.store.SetEvent(commodity, market.EventState{
		Mult: mult, TicksLeft: dur, Name: name, Narrative: narrative, Kind: "crisis",
	})
	cs.active[commodity] = name
	cs.appendAll("crisis", narrative)
}

// pickFreeCommodity returns a random tradable commodity with NO active event (named or unnamed), or "" if none is
// free. We never start a crisis on a commodity that already has a shock running.
func (cs *crisisScheduler) pickFreeCommodity(states map[string]market.EventState) string {
	free := make([]string, 0, len(cs.commodities))
	for _, c := range cs.commodities {
		if _, busy := states[c]; busy {
			continue
		}
		free = append(free, c)
	}
	if len(free) == 0 {
		return ""
	}
	return free[cs.rng.Intn(len(free))]
}

// appendAll writes one frozen Chronicle line (the same kind/text) to EVERY league. Best-effort: a failed append is
// logged and skipped (the crisis state already advanced — we don't retry a narration line).
func (cs *crisisScheduler) appendAll(kind, text string) {
	for _, lg := range cs.store.AllLeagues() {
		if _, err := cs.store.AppendChronicle(store.ChronicleEntry{
			LeagueID: lg.ID, Kind: kind, Text: text,
		}); err != nil {
			cs.logger.Printf("crisis: append %s to league %s: %v", kind, shortID(lg.ID), err)
		}
	}
}
