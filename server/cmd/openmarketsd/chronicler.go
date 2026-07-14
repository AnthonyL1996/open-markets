package main

import (
	"log"
	"time"

	"openmarkets/server/internal/store"
)

// chronicler is the background saga-narrator (social slice 2). It watches each league for transitions that the
// clean in-op direct appends (founded/joined/bailout) can't catch — austerity entering/leaving, and a new
// record single-trade settlement — and AppendChronicles a frozen narration for each. It is best-effort: every
// poll is wrapped so a single bad league can never block or crash the loop.
//
// Baselines are SEEDED on boot from current state, so the chronicler never backfills pre-existing history:
//   - austerity[league|member] = the member's austerity flag at boot (only true→false / false→true transitions
//     after boot are narrated).
//   - record[league] = the league's current max single-trade settlement at boot (a later trade must EXCEED a
//     record that was already > 0 to chronicle — the first-ever trade never sets a "new record").
//   - cursor[league] = the league's current max settlement seq at boot (so old trades aren't re-scanned).
type chronicler struct {
	store    store.Store
	interval time.Duration
	logger   *log.Logger

	austerity map[string]bool  // "league|member" → last-seen austerity flag
	record    map[string]int64 // league → running max single-trade settlement cents
	cursor    map[string]int64 // league → max settlement seq already scanned for records
}

func newChronicler(st store.Store, interval time.Duration, logger *log.Logger) *chronicler {
	return &chronicler{
		store:     st,
		interval:  interval,
		logger:    logger,
		austerity: map[string]bool{},
		record:    map[string]int64{},
		cursor:    map[string]int64{},
	}
}

func austerityKey(leagueID, accountID string) string { return leagueID + "|" + accountID }

// seed primes all baselines from current state so the chronicler does not backfill pre-existing transitions.
func (c *chronicler) seed() {
	for _, lg := range c.store.AllLeagues() {
		members, err := c.store.LeagueMembers(lg.ID)
		if err != nil {
			continue
		}
		for _, m := range members {
			aust, _, _ := c.store.CityState(lg.ID, m)
			c.austerity[austerityKey(lg.ID, m)] = aust
		}
		// Seed the record + the settlement cursor from existing events (old history isn't chronicled).
		var maxSeq, maxTrade int64
		for _, e := range c.store.SettlementsSince(lg.ID, 0, 100000) {
			if e.Seq > maxSeq {
				maxSeq = e.Seq
			}
			if isTradeRef(e.Ref) && e.Cents > maxTrade {
				maxTrade = e.Cents
			}
		}
		c.cursor[lg.ID] = maxSeq
		c.record[lg.ID] = maxTrade
	}
}

// run is the poller loop. Returns when stop is closed.
func (c *chronicler) run(stop <-chan struct{}) {
	c.seed()
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			c.pollSafe()
		case <-stop:
			return
		}
	}
}

// pollSafe runs one poll, recovering from any panic so a single bad tick can never crash the loop.
func (c *chronicler) pollSafe() {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Printf("chronicler: recovered from panic: %v", r)
		}
	}()
	c.poll()
}

// poll scans every league for austerity transitions + a new record trade, appending a frozen narration for
// each. Best-effort: never blocks (it only reads + appends) and never crashes (errors are logged + skipped).
func (c *chronicler) poll() {
	for _, lg := range c.store.AllLeagues() {
		c.pollAusterity(lg)
		c.pollRecordTrade(lg)
	}
}

// pollAusterity narrates each member's austerity flag flipping since the last poll.
func (c *chronicler) pollAusterity(lg store.League) {
	members, err := c.store.LeagueMembers(lg.ID)
	if err != nil {
		return
	}
	for _, m := range members {
		key := austerityKey(lg.ID, m)
		aust, _, _ := c.store.CityState(lg.ID, m)
		prev, seen := c.austerity[key]
		if !seen {
			// A member who joined after boot: record their baseline without narrating (the "joined" entry
			// already covers the arrival; we only narrate a FLIP from a known prior state).
			c.austerity[key] = aust
			continue
		}
		if aust == prev {
			continue
		}
		var kind, text string
		if aust {
			kind, text = "austerity", "🔥 "+c.name(m)+" fell into austerity."
		} else {
			kind, text = "escaped", "🕊️ "+c.name(m)+" clawed out of austerity."
		}
		if _, err := c.store.AppendChronicle(store.ChronicleEntry{
			LeagueID: lg.ID, Kind: kind, ActorID: m, Text: text,
		}); err != nil {
			c.logger.Printf("chronicler: append austerity (%s): %v", kind, err)
			continue // leave the baseline so we retry next poll
		}
		c.austerity[key] = aust
	}
}

// pollRecordTrade narrates a new record single-trade settlement (a trade event exceeding the running record,
// when the record was already > 0 — the first-ever trade never counts).
func (c *chronicler) pollRecordTrade(lg store.League) {
	since := c.cursor[lg.ID]
	events := c.store.SettlementsSince(lg.ID, since, 1000)
	for _, e := range events {
		if e.Seq > c.cursor[lg.ID] {
			c.cursor[lg.ID] = e.Seq
		}
		if !isTradeRef(e.Ref) {
			continue
		}
		rec := c.record[lg.ID]
		if rec > 0 && e.Cents > rec {
			text := "📈 " + c.name(e.PayerID) + " → " + c.name(e.ReceiverID) +
				" set a new record trade (§" + formatCents(e.Cents) + ")."
			if _, err := c.store.AppendChronicle(store.ChronicleEntry{
				LeagueID: lg.ID, Kind: "record-trade", ActorID: e.PayerID, TargetID: e.ReceiverID,
				Cents: e.Cents, Text: text,
			}); err != nil {
				c.logger.Printf("chronicler: append record-trade: %v", err)
				// Don't advance the record on failure so the next poll retries this event.
				continue
			}
		}
		if e.Cents > c.record[lg.ID] {
			c.record[lg.ID] = e.Cents
		}
	}
}

// name resolves an account's display name, falling back to a short id (mirrors the Discord poster).
func (c *chronicler) name(id string) string {
	if id == "" {
		return "—"
	}
	if a, err := c.store.GetAccount(id); err == nil && a.DisplayName != "" {
		return a.DisplayName
	}
	return shortID(id)
}

// isTradeRef reports whether a settlement ref is a peer trade installment ("trade:…") but NOT a trade-shortfall
// bond ("trade-shortfall:…"). Record trades count only real trade settlements.
func isTradeRef(ref string) bool {
	const p = "trade:"
	return len(ref) >= len(p) && ref[:len(p)] == p
}
