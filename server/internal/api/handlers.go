package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"openmarkets/server/internal/market"
	"openmarkets/server/internal/store"
)

// GET /health — liveness + version for the client's reachability check.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.cfg.Version,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

// GET /healthz — minimal, dependency-free liveness/readiness probe for k3s. No auth, no rate limit, tiny body.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// GET /eol — end-of-life signal. A future server can flip eol=true to ask clients to stop polling
// (dead-server posture). Always present so the client can rely on it.
func (s *Server) handleEOL(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"eol": false, "message": ""})
}

// POST /accounts — mint an identity. The plaintext secret is returned ONCE; the client stores it. This endpoint is
// UNAUTHENTICATED, so a dedicated stricter per-IP limiter (OM_ACCT_PER_HOUR, default 10/h) throttles account-minting
// abuse independently of the general request limiter.
func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	if !s.acctLimiter.allow(s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "account creation rate limit exceeded")
		return
	}
	a, secret, err := s.store.CreateAccount()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create account")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"accountId": a.ID, "secret": secret})
}

type setNameReq struct {
	Name string `json:"name"`
}

// maxDisplayName caps a friendly name. Long enough for a city/player handle, short enough to fit a roster row.
const maxDisplayName = 24

// POST /accounts/name — set (or clear, with "") the caller's display name shown to leaguemates. Authenticated.
// The name is trimmed, stripped of control characters, and capped; an over-long name is truncated, not rejected.
func (s *Server) handleSetAccountName(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	var req setNameReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	a, err := s.store.SetAccountName(accountID, sanitizeName(req.Name))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not set name")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"accountId": a.ID, "displayName": a.DisplayName})
}

// sanitizeName trims, drops control characters (so a name can't smuggle newlines into a roster), and caps length.
func sanitizeName(name string) string {
	cleaned := make([]rune, 0, len(name))
	for _, ch := range strings.TrimSpace(name) {
		if unicode.IsControl(ch) {
			continue
		}
		cleaned = append(cleaned, ch)
		if len(cleaned) >= maxDisplayName {
			break
		}
	}
	return string(cleaned)
}

type createLeagueReq struct {
	Name string `json:"name"`
}

type leagueDTO struct {
	LeagueID string `json:"leagueId"`
	Name     string `json:"name"`
	JoinCode string `json:"joinCode"`
	IsOwner  bool   `json:"isOwner"`
}

// GET /leagues — the leagues the caller belongs to, so the client can offer a league switcher (gap 4.7).
// JoinCode is included only for leagues the caller owns (it's the owner's invite to share).
func (s *Server) handleListLeagues(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	leagues, err := s.store.LeaguesForAccount(accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list leagues")
		return
	}
	out := make([]leagueDTO, 0, len(leagues))
	for _, l := range leagues {
		isOwner := l.OwnerID == accountID
		code := ""
		if isOwner {
			code = l.JoinCode
		}
		out = append(out, leagueDTO{LeagueID: l.ID, Name: l.Name, JoinCode: code, IsOwner: isOwner})
	}
	writeJSON(w, http.StatusOK, map[string]any{"leagues": out})
}

// POST /leagues — create a friend group; the caller becomes owner + first member.
func (s *Server) handleCreateLeague(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	var req createLeagueReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	l, err := s.store.CreateLeague(accountID, req.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create league")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"leagueId": l.ID, "joinCode": l.JoinCode, "name": l.Name})
}

type joinReq struct {
	JoinCode string `json:"joinCode"`
}

// POST /leagues/join — join a friend group by its share code.
func (s *Server) handleJoinLeague(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	var req joinReq
	if err := decodeJSON(r, &req); err != nil || req.JoinCode == "" {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	l, err := s.store.LeagueByJoinCode(req.JoinCode)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such league")
		return
	}
	switch err := s.store.JoinLeague(accountID, l.ID); {
	case err == nil, errors.Is(err, store.ErrAlreadyMember):
		writeJSON(w, http.StatusOK, map[string]any{"leagueId": l.ID, "joined": true})
	default:
		writeErr(w, http.StatusInternalServerError, "could not join")
	}
}

type memberDTO struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName,omitempty"`
	IsOwner     bool   `json:"isOwner"`
	Reliability int    `json:"reliability"` // 0..100 on-time score (100 with no history)
	// League transparency (M7): each member's public financial standing, visible to every leaguemate.
	Austerity            bool  `json:"austerity,omitempty"`            // in austerity (owes terminally-defaulted debt)
	OutstandingDebtCents int64 `json:"outstandingDebtCents,omitempty"` // garnishable defaulted balance still owed
	NetCents             int64 `json:"netCents,omitempty"`             // this member's net cash position in the league
	// Presence + city profile (leaguemate-visible). Online is "seen within the offline threshold"; LastSeenSec is the
	// later of the runtime activity signal and the persisted profile report (so it survives a server restart).
	Online      bool  `json:"online,omitempty"`
	LastSeenSec int64 `json:"lastSeenSec,omitempty"`
	// The city profile is FLATTENED into this object (anonymous embed → its json fields promote to top-level keys),
	// so the client reads plain scalars (population, happiness, …) per member rather than a nested object. Nil when
	// the member hasn't reported a profile yet → nothing is emitted. Its accountId is shadowed by the outer field.
	*store.CityProfile
}

// GET /leagues/members?league=ID — the league's roster. Member-only (same posture as the feeds); the
// owner is flagged and sorted first so the console can show who's in the friend group. Account ids are
// the only server-side identity — display names live client-side.
func (s *Server) handleLeagueMembers(w http.ResponseWriter, r *http.Request) {
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
	l, err := s.store.GetLeague(leagueID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such league")
		return
	}
	ids, err := s.store.LeagueMembers(leagueID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list members")
		return
	}
	auditNet, _, _ := s.store.AuditLeague(leagueID) // best-effort per-member league net (transparency); nil on error
	members := make([]memberDTO, 0, len(ids))
	for _, aid := range ids {
		// Best-effort name lookup; a missing/renamed account simply has no display name (client falls back to ID).
		name := ""
		rel := 100
		if acc, err := s.store.GetAccount(aid); err == nil {
			name = acc.DisplayName
			rel = acc.Reliability()
		}
		austerity, debt, _ := s.store.CityState(leagueID, aid)
		m := memberDTO{AccountID: aid, DisplayName: name, IsOwner: aid == l.OwnerID, Reliability: rel,
			Austerity: austerity, OutstandingDebtCents: debt}
		if auditNet != nil {
			m.NetCents = auditNet[aid]
		}
		// Presence: the runtime activity signal drives "online now"; the persisted profile report time is the
		// durable last-seen fallback (so a leaguemate seen before a server restart still shows a last-seen time).
		now := time.Now().UTC()
		if seen, ok := s.store.LastActive(aid); ok {
			m.LastSeenSec = seen.Unix()
			m.Online = now.Sub(seen) <= s.cfg.DueOfflineThreshold
		}
		if prof, ok := s.store.CityProfileOf(aid); ok {
			p := prof
			m.CityProfile = &p
			if rs := prof.ReportedAt.Unix(); rs > m.LastSeenSec {
				m.LastSeenSec = rs // profile report survives restarts; use it if newer than the runtime signal
			}
		}
		members = append(members, m)
	}
	// Owner first, then the store's lexicographic order.
	sort.SliceStable(members, func(i, j int) bool { return members[i].IsOwner && !members[j].IsOwner })
	writeJSON(w, http.StatusOK, map[string]any{
		"leagueId": l.ID,
		"name":     l.Name,
		"ownerId":  l.OwnerID,
		"members":  members,
	})
}

// POST /cityprofile — the caller's own city snapshot (population/happiness/industry/treasury/…) so leaguemates can
// see it on the Members view. Account-auth: the profile is city-level (league-agnostic). The AccountID and ReportedAt
// are set server-side and never trusted from the body (no spoofing another city's stats).
func (s *Server) handleCityProfile(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	var p store.CityProfile
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	p.AccountID = accountID // identity comes from the credential, not the body
	if err := s.store.PutCityProfile(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not store profile")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GET /cityprofile/history?account=ACCOUNTID&league=LEAGUEID — the target account's retained city snapshots
// (oldest→newest) plus the cumulative net-§ curve, MEMBER-ONLY: the caller must be a member of the league, AND
// the target account must also be a member (privacy — you only see cities in a league you share).
func (s *Server) handleCityHistory(w http.ResponseWriter, r *http.Request) {
	callerID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	account := r.URL.Query().Get("account")
	leagueID := r.URL.Query().Get("league")
	if account == "" || leagueID == "" {
		writeErr(w, http.StatusBadRequest, "missing account or league")
		return
	}
	if !s.store.IsMember(callerID, leagueID) {
		writeErr(w, http.StatusForbidden, "not a member of that league")
		return
	}
	if !s.store.IsMember(account, leagueID) {
		writeErr(w, http.StatusForbidden, "target not a member of that league")
		return
	}
	snapshots := s.store.CityProfileHistory(account)
	if snapshots == nil {
		snapshots = []store.CityProfile{}
	}
	netSeries := s.store.NetCentsSeries(leagueID, account)
	if netSeries == nil {
		netSeries = []store.NetPoint{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accountId": account,
		"snapshots": snapshots,
		"netSeries": netSeries,
	})
}

type reportReq struct {
	LeagueID  string  `json:"leagueId"`
	Commodity string  `json:"commodity"`
	NetSupply float64 `json:"netSupply"`
}

// POST /report — submit the caller's current net supply/demand for a commodity in a league.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	var req reportReq
	if err := decodeJSON(r, &req); err != nil || req.LeagueID == "" || req.Commodity == "" {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	err := s.store.PutReport(store.Report{
		AccountID: accountID,
		LeagueID:  req.LeagueID,
		Commodity: req.Commodity,
		NetSupply: req.NetSupply,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusForbidden, "not a member of that league")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not record report")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /index?league=ID — the legacy single-float feed the shipping client parses (PriceFeedDto:
// {version, index, ts}). index is the league's market-wide aggregate. Requires the caller to be a
// member of the league.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	_, leagueID, ok := s.authMember(w, r)
	if !ok {
		return
	}
	reports, err := s.store.LeagueReports(leagueID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such league")
		return
	}
	// Mean of the EFFECTIVE per-commodity indices (elasticity × global event) — the legacy coarse signal, now
	// event-aware for consistency with /prices and contract pricing.
	shields := store.MarketShieldsFromEffects(s.store.LeagueEffects(leagueID))
	eff := market.EffectiveIndices(market.CommodityIndicesWithShields(reports, s.params, shields), s.store.EventStates(), s.params)
	idx := 1.0
	if len(eff) > 0 {
		var total float64
		for _, v := range eff {
			total += v
		}
		idx = total / float64(len(eff))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": s.cfg.Version,
		"index":   idx,
		"ts":      time.Now().UTC().Unix(),
	})
}

type commodityIndex struct {
	Commodity string    `json:"commodity"`
	Index     float64   `json:"index"`
	EventPct  int       `json:"eventPct"`          // M9: active price-shock swing % (0 = none)
	History   []float64 `json:"history,omitempty"` // M9: rolling effective-index ring (server-served sparkline)
}

// GET /prices?league=ID — the richer per-commodity feed. `commodities` is an ARRAY (not a map) so the
// net35 client's UnityEngine.JsonUtility can deserialize it (JsonUtility can't do maps). Sorted by
// commodity for a deterministic response. Same auth as /index.
func (s *Server) handlePrices(w http.ResponseWriter, r *http.Request) {
	_, leagueID, ok := s.authMember(w, r)
	if !ok {
		return
	}
	reports, err := s.store.LeagueReports(leagueID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such league")
		return
	}
	events := s.store.EventStates()
	shields := store.MarketShieldsFromEffects(s.store.LeagueEffects(leagueID))
	eff := market.EffectiveIndices(market.CommodityIndicesWithShields(reports, s.params, shields), events, s.params)
	hist := s.store.IndexHistory(leagueID)
	arr := make([]commodityIndex, 0, len(eff))
	for c, v := range eff {
		arr = append(arr, commodityIndex{Commodity: c, Index: v, EventPct: market.EventPct(events, c), History: hist[c]})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].Commodity < arr[j].Commodity })
	writeJSON(w, http.StatusOK, map[string]any{
		"version":     s.cfg.Version,
		"ts":          time.Now().UTC().Unix(),
		"commodities": arr,
	})
}

type reportBatchReq struct {
	LeagueID string `json:"leagueId"`
	Reports  []struct {
		Commodity string  `json:"commodity"`
		NetSupply float64 `json:"netSupply"`
	} `json:"reports"`
}

// POST /report/batch — submit several commodities' net supply/demand in one call (the client reports
// its whole day on a rollover). All-or-nothing on membership; blank-commodity rows are skipped.
func (s *Server) handleReportBatch(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	var req reportBatchReq
	if err := decodeJSON(r, &req); err != nil || req.LeagueID == "" {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !s.store.IsMember(accountID, req.LeagueID) {
		writeErr(w, http.StatusForbidden, "not a member of that league")
		return
	}
	for _, row := range req.Reports {
		if row.Commodity == "" {
			continue
		}
		if err := s.store.PutReport(store.Report{
			AccountID: accountID, LeagueID: req.LeagueID,
			Commodity: row.Commodity, NetSupply: row.NetSupply,
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, "could not record reports")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// leagueReportsForCaller authenticates the caller, checks league membership, and returns the league's
// reports for aggregation. It writes the error response itself and returns ok=false on any failure.
func (s *Server) leagueReportsForCaller(w http.ResponseWriter, r *http.Request) ([]market.Report, bool) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return nil, false
	}
	leagueID := r.URL.Query().Get("league")
	if leagueID == "" {
		writeErr(w, http.StatusBadRequest, "missing league")
		return nil, false
	}
	if !s.store.IsMember(accountID, leagueID) {
		writeErr(w, http.StatusForbidden, "not a member of that league")
		return nil, false
	}
	reports, err := s.store.LeagueReports(leagueID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such league")
		return nil, false
	}
	return reports, true
}
