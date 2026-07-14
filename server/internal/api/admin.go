package api

import (
	"net/http"

	"openmarkets/server/internal/store"
)

// The admin surface is a token-gated operator API, reusing the same OM_CONSOLE_TOKEN gate as the /console page
// (consoleToken / cfg.ConsoleToken). UNLIKE /console (whose local-dev gate is open when the token is empty), the
// admin routes here are DESTRUCTIVE (delete a league, kick a member), so they require the token to be EXPLICITLY
// set: an unset-or-mismatched token is 401 — admin must be deliberately enabled, even locally.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.ConsoleToken == "" || consoleToken(r) != s.cfg.ConsoleToken {
		writeErr(w, http.StatusUnauthorized, "admin token required")
		return false
	}
	return true
}

// GET /admin/stats — aggregate operator counts (doubles as a no-Prometheus /metrics).
func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.store.AdminStats())
}

// GET /admin/leagues — every league with a member count.
func (s *Server) handleAdminLeagues(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	leagues := s.store.AllLeagues()
	out := make([]map[string]any, 0, len(leagues))
	for _, l := range leagues {
		mem, _ := s.store.LeagueMembers(l.ID)
		out = append(out, map[string]any{
			"id":          l.ID,
			"name":        l.Name,
			"ownerId":     l.OwnerID,
			"memberCount": len(mem),
			"created":     l.Created,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"leagues": out})
}

// GET /admin/leagues/{id} — a league plus its members (id + display name + austerity + net §).
func (s *Server) handleAdminLeague(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	leagueID := r.PathValue("id")
	lg, err := s.store.GetLeague(leagueID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such league")
		return
	}
	memberIDs, err := s.store.LeagueMembers(leagueID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such league")
		return
	}
	// AuditLeague's net map gives each member's net online § (settlement log). An unknown league was already
	// ruled out above; ignore the error and treat a missing member as net 0.
	net, _, _ := s.store.AuditLeague(leagueID)
	members := make([]map[string]any, 0, len(memberIDs))
	for _, aid := range memberIDs {
		name := ""
		if a, err := s.store.GetAccount(aid); err == nil {
			name = a.DisplayName
		}
		austerity, outstanding, defaulted := s.store.CityState(leagueID, aid)
		members = append(members, map[string]any{
			"accountId":            aid,
			"displayName":          name,
			"austerity":            austerity,
			"outstandingDebtCents": outstanding,
			"defaultedBonds":       defaulted,
			"netCents":             net[aid],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       lg.ID,
		"name":     lg.Name,
		"ownerId":  lg.OwnerID,
		"joinCode": lg.JoinCode,
		"created":  lg.Created,
		"members":  members,
	})
}

// POST /admin/leagues/{id}/delete — delete a league and cascade its members/reports/trades/bonds/effects/events.
func (s *Server) handleAdminDeleteLeague(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	leagueID := r.PathValue("id")
	if err := s.store.DeleteLeague(leagueID); err != nil {
		if err == store.ErrNotFound {
			writeErr(w, http.StatusNotFound, "no such league")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not delete league")
		return
	}
	s.logger.Printf("admin: deleted league %s", leagueID)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": leagueID})
}

// POST /admin/leagues/{id}/kick?account=ID — remove a membership (v1: membership row only; settled history stays).
func (s *Server) handleAdminKick(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	leagueID := r.PathValue("id")
	accountID := r.URL.Query().Get("account")
	if accountID == "" {
		writeErr(w, http.StatusBadRequest, "missing account")
		return
	}
	if err := s.store.RemoveMember(accountID, leagueID); err != nil {
		if err == store.ErrNotFound {
			writeErr(w, http.StatusNotFound, "no such league or member")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not remove member")
		return
	}
	s.logger.Printf("admin: kicked %s from league %s", accountID, leagueID)
	writeJSON(w, http.StatusOK, map[string]any{"kicked": accountID, "league": leagueID})
}
