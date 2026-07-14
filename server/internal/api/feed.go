package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"openmarkets/server/internal/market"
	"openmarkets/server/internal/store"
)

// Activity feed (social slice 1): a read-only, member-only league activity stream derived from the settlement
// event log. Nothing here mutates money/settlement state — every value is a pure read of SettlementsSince plus a
// type derived from each event's Ref prefix.

// feedItem is one shaped activity-feed row (newest-first in the response).
type feedItem struct {
	Seq      int64     `json:"seq"`
	TS       time.Time `json:"ts"` // the event's Created timestamp
	Type     string    `json:"type"`
	AccountA string    `json:"accountA"` // payer
	AccountB string    `json:"accountB"` // receiver
	Cents    int64     `json:"cents"`
	Ref      string    `json:"ref"`
}

// feedDTO is the /feed response: the league id + the recent activity items, newest first.
type feedDTO struct {
	LeagueID string     `json:"leagueId"`
	Items    []feedItem `json:"items"`
}

// feedFetchLimit is how many recent events the feed reads (then reverses to newest-first).
const feedFetchLimit = 50

// feedType derives a human-friendly activity type from a settlement event's Ref prefix (see SettlementEvent.Ref:
// "trade:…", "bond:…", "garnish:…", "invest:…", "bailout:…", "loan:…", "trade-shortfall:…").
func feedType(ref string) string {
	switch {
	case strings.HasPrefix(ref, "trade-shortfall:"):
		return "shortfall"
	case strings.HasPrefix(ref, "trade:"):
		return "trade"
	case strings.HasPrefix(ref, "bond:"):
		return "bond"
	case strings.HasPrefix(ref, "garnish:"):
		return "garnish"
	case strings.HasPrefix(ref, "invest:"):
		return "investment"
	case strings.HasPrefix(ref, "bailout:"):
		return "bailout"
	case strings.HasPrefix(ref, "loan:"):
		return "loan"
	default:
		return "other"
	}
}

// handleFeed serves the recent league activity feed, newest first (member-only, like /leaderboards).
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	leagueID := r.URL.Query().Get("league")
	if leagueID == "" {
		writeErr(w, http.StatusBadRequest, "missing league")
		return
	}
	if !s.store.IsMember(accountID, leagueID) {
		writeErr(w, http.StatusForbidden, "not a member of that league")
		return
	}

	events := s.store.SettlementsSince(leagueID, 0, feedFetchLimit) // ascending by seq
	items := make([]feedItem, 0, len(events))
	// Reverse to newest-first.
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		items = append(items, feedItem{
			Seq: e.Seq, TS: e.Created, Type: feedType(e.Ref),
			AccountA: e.PayerID, AccountB: e.ReceiverID, Cents: e.Cents, Ref: e.Ref,
		})
	}
	writeJSON(w, http.StatusOK, feedDTO{LeagueID: leagueID, Items: items})
}

// ── League Chronicle (social slice 2): a persistent, narrated saga ─────────────

// chronicleEntryDTO is one chronicle row on the wire. Text is the server-frozen narration (names already
// resolved at append time) — the client renders it verbatim.
type chronicleEntryDTO struct {
	Seq      int64     `json:"seq"`
	Kind     string    `json:"kind"`
	ActorID  string    `json:"actorId"`
	TargetID string    `json:"targetId"`
	Text     string    `json:"text"`
	Cents    int64     `json:"cents"`
	Created  time.Time `json:"created"`
}

// chronicleDTO is the /chronicle (and /chronicle/onthisday) response: the league id + entries ascending.
type chronicleDTO struct {
	LeagueID string              `json:"leagueId"`
	Entries  []chronicleEntryDTO `json:"entries"`
}

// chronicleFetchLimit caps the number of chronicle entries the saga reader returns by default.
const chronicleFetchLimit = 200

func toChronicleDTO(leagueID string, entries []store.ChronicleEntry) chronicleDTO {
	out := make([]chronicleEntryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, chronicleEntryDTO{
			Seq: e.Seq, Kind: e.Kind, ActorID: e.ActorID, TargetID: e.TargetID,
			Text: e.Text, Cents: e.Cents, Created: e.Created,
		})
	}
	return chronicleDTO{LeagueID: leagueID, Entries: out}
}

// handleChronicle serves the league's narrated saga ascending (oldest→newest), member-only (like /feed).
// ?since=SEQ resumes after a known seq; ?limit=N caps (default 200).
func (s *Server) handleChronicle(w http.ResponseWriter, r *http.Request) {
	accountID, leagueID, ok := s.authMemberFeed(w, r)
	if !ok {
		return
	}
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			since = n
		}
	}
	limit := chronicleFetchLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	_ = accountID
	entries := s.store.Chronicle(leagueID, since, limit) // ascending by seq
	writeJSON(w, http.StatusOK, toChronicleDTO(leagueID, entries))
}

// handleChronicleOnThisDay serves the league's prior-day entries that fall on today's month/day, member-only.
func (s *Server) handleChronicleOnThisDay(w http.ResponseWriter, r *http.Request) {
	_, leagueID, ok := s.authMemberFeed(w, r)
	if !ok {
		return
	}
	entries := s.store.ChronicleOnThisDay(leagueID, time.Now().UTC())
	writeJSON(w, http.StatusOK, toChronicleDTO(leagueID, entries))
}

// ── Shared league crises (social slice 3): the active named, narrated crises ────

// crisisDTO is one active crisis on the wire. Crises are GLOBAL (one shared world economy across leagues), so this
// endpoint takes no league param and needs no member gate — any authenticated account sees the same list.
type crisisDTO struct {
	Name      string `json:"name"`
	Narrative string `json:"narrative"`
	Kind      string `json:"kind"`
	Commodity string `json:"commodity"` // wire key
	EventPct  int    `json:"eventPct"`  // signed swing % (e.g. +60, -50)
	TicksLeft int    `json:"ticksLeft"`
}

// crisesDTO is the GET /crises response: the currently-active crises (named events only).
type crisesDTO struct {
	Crises []crisisDTO `json:"crises"`
}

// handleCrises serves the active shared league crises (any authenticated account). The active set is every global
// price event with a Name — a named, narrated crisis. Read-only.
func (s *Server) handleCrises(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authAccount(r); !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	states := s.store.EventStates()
	out := make([]crisisDTO, 0)
	for commodity, e := range states {
		if e.Name == "" {
			continue // skip plain (unnamed) random shocks — only crises are listed
		}
		out = append(out, crisisDTO{
			Name: e.Name, Narrative: e.Narrative, Kind: e.Kind, Commodity: commodity,
			EventPct: market.EventPct(states, commodity), TicksLeft: e.TicksLeft,
		})
	}
	// Stable order (by commodity) so the client banner doesn't reshuffle each poll.
	sort.Slice(out, func(i, j int) bool { return out[i].Commodity < out[j].Commodity })
	writeJSON(w, http.StatusOK, crisesDTO{Crises: out})
}

// authMemberFeed is the shared member-only gate for the social-slice read endpoints: it authenticates the
// caller, requires a ?league= param, and confirms membership — writing the appropriate error and returning
// ok=false on any failure (exactly the /feed auth flow).
func (s *Server) authMemberFeed(w http.ResponseWriter, r *http.Request) (accountID, leagueID string, ok bool) {
	accountID, authed := s.authAccount(r)
	if !authed {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return "", "", false
	}
	leagueID = r.URL.Query().Get("league")
	if leagueID == "" {
		writeErr(w, http.StatusBadRequest, "missing league")
		return "", "", false
	}
	if !s.store.IsMember(accountID, leagueID) {
		writeErr(w, http.StatusForbidden, "not a member of that league")
		return "", "", false
	}
	return accountID, leagueID, true
}
