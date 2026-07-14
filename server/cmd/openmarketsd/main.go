// Command openmarketsd is the Open Markets Phase A backend: a small HTTP service that aggregates
// friend-group net supply/demand into a shared, clamped price index for the Cities: Skylines mod.
//
// Run with no arguments for sane defaults (listens on :8080, persists to data/openmarkets.json).
// Configure via OM_* environment variables — see internal/config and .env.example.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"openmarkets/server/internal/api"
	"openmarkets/server/internal/config"
	"openmarkets/server/internal/duecycle"
	"openmarkets/server/internal/market"
	"openmarkets/server/internal/pricing"
	"openmarkets/server/internal/store"
	"openmarkets/server/internal/store/postgres"
)

// backend is the union of methods main wires beyond the bare store.Store: the duecycle work-list/tick methods,
// the accept-time pricer + market-dynamics setup, and Flush. BOTH *store.Memory and *postgres.PG satisfy it, so
// the rest of main (api.New, duecycle.New, the pricer) is identical for either backend.
type backend interface {
	store.Store
	duecycle.Store
	SetMarketParams(p market.Params, commodities []string)
	SetPricer(p store.Pricer)
	EventMultipliers() map[string]float64
}

func main() {
	logger := log.New(os.Stdout, "[openmarketsd] ", log.LstdFlags|log.LUTC)
	cfg := config.Load()

	storeBackend := "in-memory/json"
	if cfg.DBURL != "" {
		storeBackend = "postgres"
	}
	// Effective config (no secrets — DBURL / ConsoleToken values omitted): a single startup line so an operator
	// can confirm what was actually resolved (after env parsing, validation, and clamping).
	logger.Printf("effective config: addr=%s store=%s dueInterval=%s index=[%.3f,%.3f] volumeRef=%.0f "+
		"ratePerMin=%d acctPerHour=%d trustProxy=%t console=%t consoleTokenSet=%t",
		cfg.Addr, storeBackend, cfg.DueInterval, cfg.IndexMin, cfg.IndexMax, cfg.VolumeRef,
		cfg.RatePerMin, cfg.AcctPerHour, cfg.TrustProxy, cfg.Console, cfg.ConsoleToken != "")

	var st backend
	if cfg.DBURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		pg, err := postgres.New(ctx, cfg.DBURL)
		cancel()
		if err != nil {
			logger.Fatalf("open postgres store: %v", err)
		}
		defer pg.Close()
		st = pg
		logger.Printf("store backend: postgres")
	} else {
		mem, err := store.Open(cfg.DataPath)
		if err != nil {
			logger.Fatalf("open store: %v", err)
		}
		st = mem
		logger.Printf("store backend: in-memory/json (data=%s)", cfg.DataPath)
	}
	// M9 market dynamics: install the index params + the commodity set the global price-shock generator rolls over.
	marketParams := market.Params{VolumeRef: cfg.VolumeRef, Min: cfg.IndexMin, Max: cfg.IndexMax}
	st.SetMarketParams(marketParams, pricing.Commodities())
	// Accept-time trade valuation: freeze line values at base price × the league's current EFFECTIVE index
	// (elasticity × the global price event).
	st.SetPricer(pricing.NewPricer(st.LeagueReports, st.EventMultipliers, marketParams,
		func(leagueID string) []market.Shield {
			return store.MarketShieldsFromEffects(st.LeagueEffects(leagueID))
		}))

	srv := api.New(cfg, st, logger)
	httpServer := &http.Server{
		Addr:         cfg.Addr,
		Handler:      srv.Handler(),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	// All background jobs register here so graceful shutdown can WAIT for them to finish their current tick
	// before the final Flush — no job is mid-write when the store is flushed/closed.
	var wg sync.WaitGroup

	// Periodic durability flush as a backstop to the persist-on-write path.
	stopFlush := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := st.Flush(); err != nil {
					logger.Printf("flush: %v", err)
				}
			case <-stopFlush:
				return
			}
		}
	}()

	// Due-clock: advance trade/bond installment deadlines and apply misses (auto-bonds, defaults) on a
	// real-time cadence, so an offline city still accrues. Sweeps at the installment interval.
	stopDue := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := duecycle.New(st, duecycle.Config{
			Interval: cfg.DueInterval, GraceIntervals: cfg.DueGrace,
			MaxMissesPerTick:      cfg.DueMaxMissesPerTick,
			OfflineGraceIntervals: cfg.DueOfflineGrace,
			OfflineThreshold:      cfg.DueOfflineThreshold,
		})
		t := time.NewTicker(cfg.DueInterval)
		defer t.Stop()
		for {
			select {
			case now := <-t.C:
				if ts, bm, gn := ticker.Tick(now.UTC()); ts > 0 || bm > 0 || gn > 0 {
					logger.Printf("due-clock: %d trade installment(s) auto-settled, %d bond miss(es), %d garnished", ts, bm, gn)
				}
			case <-stopDue:
				return
			}
		}
	}()

	// Discord activity bridge: when a webhook is configured, a background poster polls each league for NEW
	// settlement events and posts a concise summary to Discord. Seeds its per-league cursor to the current max
	// seq on boot, so it never backfills history — only events after boot are posted. Off when unconfigured.
	stopDiscord := make(chan struct{})
	discordOn := cfg.DiscordWebhook != ""
	if discordOn {
		poster := newDiscordPoster(st, cfg.DiscordWebhook, cfg.DiscordInterval, logger)
		wg.Add(1)
		go func() { defer wg.Done(); poster.run(stopDiscord) }()
		logger.Printf("discord activity bridge: enabled (interval=%s)", cfg.DiscordInterval)
	}

	// Chronicler: always-on background saga narrator. Polls each league for austerity enter/leave + a new
	// record single-trade, appending frozen narration to the league chronicle. Baselines are seeded on boot so
	// it never backfills pre-existing state. Best-effort; never blocks/crashes the server.
	stopChronicle := make(chan struct{})
	chron := newChronicler(st, cfg.ChronicleInterval, logger)
	wg.Add(1)
	go func() { defer wg.Done(); chron.run(stopChronicle) }()
	logger.Printf("chronicler: enabled (interval=%s)", cfg.ChronicleInterval)

	// Crisis scheduler (social slice 3): always-on background narrator of SHARED LEAGUE CRISES. Each interval it
	// ends any crisis whose global event has cleared and may start a new named crisis on a random commodity,
	// appending a per-league Chronicle line and injecting the shock onto the global event map. Active crises are
	// seeded on boot so it never re-narrates a pre-existing one. Best-effort; never blocks/crashes the server.
	stopCrisis := make(chan struct{})
	crisis := newCrisisScheduler(st, cfg.CrisisInterval, cfg.CrisisChance, pricing.Commodities(), logger)
	wg.Add(1)
	go func() { defer wg.Done(); crisis.run(stopCrisis) }()
	logger.Printf("crisis scheduler: enabled (interval=%s, chance=%.2f)", cfg.CrisisInterval, cfg.CrisisChance)

	// Project generator (social slice 4): always-on background curator of co-op MEGAPROJECTS (Great Works). Each
	// interval it ensures every league has exactly one open project, generating one from the curated bank (scaled
	// to member count) and appending a per-league "project-start" Chronicle line. Idempotent + boot-safe (it only
	// ensures one open project exists). Best-effort; never blocks/crashes the server.
	stopProjects := make(chan struct{})
	projGen := newProjectGenerator(st, cfg.ProjectInterval, pricing.Commodities(), logger)
	wg.Add(1)
	go func() { defer wg.Done(); projGen.run(stopProjects) }()
	logger.Printf("project generator: enabled (interval=%s)", cfg.ProjectInterval)

	go func() {
		logger.Printf("listening on %s (version=%s, data=%s)", cfg.Addr, cfg.Version, cfg.DataPath)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("listen: %v", err)
		}
	}()

	// Graceful shutdown: stop accepting, drain in-flight, flush state.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	logger.Println("shutting down...")

	close(stopFlush)
	close(stopDue)
	close(stopChronicle)
	close(stopCrisis)
	close(stopProjects)
	if discordOn {
		close(stopDiscord)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		logger.Printf("shutdown: %v", err)
	}
	// Wait for every background job to finish its current tick BEFORE the final Flush, so no job is mid-write
	// when the store is flushed.
	wg.Wait()
	if err := st.Flush(); err != nil {
		logger.Printf("final flush: %v", err)
	}
	logger.Println("bye")
}
