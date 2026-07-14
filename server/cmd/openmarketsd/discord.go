package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"openmarkets/server/internal/store"
)

// discordPoster bridges new league settlement activity to a Discord channel via an incoming webhook. It is a
// best-effort, read-only side channel: it polls the store on an interval, formats NEW events into a concise
// message, and POSTs them. Failures are logged and ignored — it must never block or crash the server.
//
// The cursor (max seq already posted, per league) lives only in memory. On boot it is SEEDED to each league's
// current max seq, so a restart does NOT backfill history into Discord — only events that happen after boot are
// posted. A restart re-seeds to current, so at worst the bridge skips events around a restart, never floods.
type discordPoster struct {
	store      store.Store
	webhookURL string
	interval   time.Duration
	logger     *log.Logger
	http       *http.Client

	mu     sync.Mutex
	cursor map[string]int64 // leagueID → max seq already posted
}

func newDiscordPoster(st store.Store, webhookURL string, interval time.Duration, logger *log.Logger) *discordPoster {
	return &discordPoster{
		store:      st,
		webhookURL: webhookURL,
		interval:   interval,
		logger:     logger,
		http:       &http.Client{Timeout: 10 * time.Second},
		cursor:     map[string]int64{},
	}
}

// seed primes each league's cursor to its current max seq so startup does not backfill old events into Discord.
func (d *discordPoster) seed() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, lg := range d.store.AllLeagues() {
		var max int64
		// A single ascending scan; the last event's seq is the max (limit generous for a friend-group league).
		for _, e := range d.store.SettlementsSince(lg.ID, 0, 100000) {
			if e.Seq > max {
				max = e.Seq
			}
		}
		d.cursor[lg.ID] = max
	}
}

// run is the poller loop. It returns when stop is closed.
func (d *discordPoster) run(stop <-chan struct{}) {
	d.seed()
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			d.pollSafe()
		case <-stop:
			return
		}
	}
}

// pollSafe runs one poll, recovering from any panic so a single bad tick can never crash the loop.
func (d *discordPoster) pollSafe() {
	defer func() {
		if r := recover(); r != nil {
			d.logger.Printf("discord: recovered from panic: %v", r)
		}
	}()
	d.poll()
}

// poll scans every league for events past its cursor and posts a batched message per league.
func (d *discordPoster) poll() {
	for _, lg := range d.store.AllLeagues() {
		d.mu.Lock()
		since := d.cursor[lg.ID]
		d.mu.Unlock()

		events := d.store.SettlementsSince(lg.ID, since, 50)
		if len(events) == 0 {
			continue
		}
		msg, maxSeq := d.format(lg, events)
		if msg == "" {
			continue
		}
		if d.post(msg) {
			d.mu.Lock()
			if maxSeq > d.cursor[lg.ID] {
				d.cursor[lg.ID] = maxSeq
			}
			d.mu.Unlock()
		}
		// On a failed POST the cursor is left untouched so the events are retried next tick.
	}
}

// format renders a league's new events into one concise Discord message and returns the max seq covered.
func (d *discordPoster) format(lg store.League, events []store.SettlementEvent) (string, int64) {
	league := lg.Name
	if league == "" {
		league = "league " + shortID(lg.ID)
	}
	var b strings.Builder
	var maxSeq int64
	for _, e := range events {
		if e.Seq > maxSeq {
			maxSeq = e.Seq
		}
		icon, verb := feedIconVerb(e.Ref)
		b.WriteString(icon)
		b.WriteString(" **")
		b.WriteString(league)
		b.WriteString("**: ")
		b.WriteString(d.name(e.PayerID))
		b.WriteString(" → ")
		b.WriteString(d.name(e.ReceiverID))
		b.WriteString(" §")
		b.WriteString(formatCents(e.Cents))
		b.WriteString(" (")
		b.WriteString(verb)
		b.WriteString(")\n")
	}
	return strings.TrimRight(b.String(), "\n"), maxSeq
}

// name resolves an account's display name, falling back to a short id.
func (d *discordPoster) name(id string) string {
	if id == "" {
		return "—"
	}
	if a, err := d.store.GetAccount(id); err == nil && a.DisplayName != "" {
		return a.DisplayName
	}
	return shortID(id)
}

// post sends one {"content": ...} payload to the webhook. Returns true on a 2xx response; logs+ignores failures.
func (d *discordPoster) post(content string) bool {
	body, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		d.logger.Printf("discord: marshal: %v", err)
		return false
	}
	req, err := http.NewRequest(http.MethodPost, d.webhookURL, bytes.NewReader(body))
	if err != nil {
		d.logger.Printf("discord: new request: %v", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		d.logger.Printf("discord: post: %v", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		d.logger.Printf("discord: webhook returned %d", resp.StatusCode)
		return false
	}
	return true
}

// feedIconVerb maps a settlement Ref prefix to a Discord-friendly icon + a human verb.
func feedIconVerb(ref string) (icon, verb string) {
	switch {
	case strings.HasPrefix(ref, "trade-shortfall:"):
		return "📦", "shortfall"
	case strings.HasPrefix(ref, "trade:"):
		return "💰", "trade"
	case strings.HasPrefix(ref, "bond:"):
		return "📜", "bond"
	case strings.HasPrefix(ref, "garnish:"):
		return "⚖️", "garnish"
	case strings.HasPrefix(ref, "invest:"):
		return "🏗️", "investment"
	case strings.HasPrefix(ref, "bailout:"):
		return "🛟", "bailout"
	case strings.HasPrefix(ref, "loan:"):
		return "🏦", "loan"
	default:
		return "•", "transfer"
	}
}

// formatCents renders cents as a whole-§ amount with thousands separators (e.g. 123456 → "1,234").
func formatCents(cents int64) string {
	whole := cents / 100
	neg := whole < 0
	if neg {
		whole = -whole
	}
	s := strconv.FormatInt(whole, 10)
	// Insert thousands separators.
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// shortID trims an opaque id to a short, human-glanceable prefix.
func shortID(id string) string {
	if len(id) > 6 {
		return id[:6]
	}
	return id
}
