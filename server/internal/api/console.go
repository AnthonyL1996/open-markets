package api

import (
	_ "embed"
	"net/http"
)

// consoleHTML is the operator console — a single self-contained page (vanilla JS, no external assets)
// embedded into the binary so the service stays a zero-dependency single artifact. Served at /console
// when enabled (cfg.Console). It is a thin client over the same public API: every privileged action it
// performs authenticates per "city" with credentials the browser stores locally — there are NO console-only
// privileged routes, so gating the page is the whole gate. On a PUBLIC server set OM_CONSOLE_TOKEN to keep the
// page (a full co-op counterparty tool) from being served to strangers; locally, leave it empty (the dev console
// stays open, with a startup warning so a prod operator notices).
//
//go:embed console.html
var consoleHTML []byte

// consoleToken extracts the admin token from the request: ?token= query param or an Authorization (raw, or "Bearer
// <tok>") / X-Console-Token header.
func consoleToken(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if t := r.Header.Get("X-Console-Token"); t != "" {
		return t
	}
	if h := r.Header.Get("Authorization"); h != "" {
		const p = "Bearer "
		if len(h) > len(p) && h[:len(p)] == p {
			return h[len(p):]
		}
		return h
	}
	return ""
}

func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	// Admin-token gate: when OM_CONSOLE_TOKEN is set, the page requires a matching ?token= or header. When it is
	// empty (local dev), the gate is open — matching the historic behaviour run-local.ps1 relies on.
	if s.cfg.ConsoleToken != "" && consoleToken(r) != s.cfg.ConsoleToken {
		writeErr(w, http.StatusUnauthorized, "console token required")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(consoleHTML)
}
