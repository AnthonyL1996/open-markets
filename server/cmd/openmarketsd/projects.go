package main

import (
	"log"
	"math/rand"
	"time"

	"openmarkets/server/internal/pricing"
	"openmarkets/server/internal/projects"
	"openmarkets/server/internal/store"
)

// projectGenerator is the always-on background curator of co-op MEGAPROJECTS (Great Works, social slice 4). On an
// interval it ensures every league has exactly ONE open project: for each league with none, it generates one from
// the curated bank (scaled to the league's member count), creates it, and appends a "project-start" Chronicle line
// ("📐 The league breaks ground on {Name}.").
//
// It is idempotent + boot-safe: it only ENSURES one open project exists, so a restart never duplicates a project a
// league already has, and the very first tick after boot fills in any league that's missing one. Best-effort: every
// tick is wrapped so a single bad league can't block or crash the loop; it stops on the shared shutdown signal.
type projectGenerator struct {
	store    store.Store
	interval time.Duration
	logger   *log.Logger

	commodities []string // the league's tradable wire-key set the bank draws requirements from
	bank        []projects.Template
	rng         *rand.Rand
}

func newProjectGenerator(st store.Store, interval time.Duration, commodities []string, logger *log.Logger) *projectGenerator {
	return &projectGenerator{
		store:       st,
		interval:    interval,
		logger:      logger,
		commodities: append([]string(nil), commodities...),
		bank:        projects.Bank,
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// run is the interval loop. It ticks ONCE immediately on start (so a fresh boot doesn't wait a full interval for
// leagues to get their first Great Work), then on the ticker. Returns when stop is closed.
func (g *projectGenerator) run(stop <-chan struct{}) {
	g.tickSafe()
	t := time.NewTicker(g.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			g.tickSafe()
		case <-stop:
			return
		}
	}
}

// tickSafe runs one tick, recovering from any panic so a single bad tick can never crash the loop.
func (g *projectGenerator) tickSafe() {
	defer func() {
		if r := recover(); r != nil {
			g.logger.Printf("projects: recovered from panic: %v", r)
		}
	}()
	g.tick()
}

// tick ensures every league with no open project gets one. Best-effort.
func (g *projectGenerator) tick() {
	for _, lid := range g.store.LeaguesWithoutOpenProject() {
		g.ensureOne(lid)
	}
}

// ensureOne generates one project from the bank for a league and creates it, then appends the start-Chronicle.
// Best-effort: a failed create/append is logged and skipped (the next tick retries the league).
func (g *projectGenerator) ensureOne(leagueID string) {
	members, err := g.store.LeagueMembers(leagueID)
	if err != nil {
		return // unknown/just-deleted league — skip
	}
	if len(members) == 0 {
		return // an empty league has no builders; wait until someone is in it
	}
	tmpl := g.bank[g.rng.Intn(len(g.bank))]
	pr := projects.Generate(tmpl, len(members), g.rng, g.commodities, pricing.DisplayName)
	if pr.Name == "" || len(pr.Reqs) == 0 {
		return // no commodity overlap for this template — try a different one next tick
	}
	pr.LeagueID = leagueID
	created, err := g.store.CreateProject(pr)
	if err != nil {
		g.logger.Printf("projects: create for league %s: %v", shortID(leagueID), err)
		return
	}
	if _, err := g.store.AppendChronicle(store.ChronicleEntry{
		LeagueID: leagueID, Kind: "project-start",
		Text: "📐 The league breaks ground on " + created.Name + ".",
	}); err != nil {
		g.logger.Printf("projects: append project-start for league %s: %v", shortID(leagueID), err)
	}
}
