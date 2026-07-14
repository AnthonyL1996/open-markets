// Package api wires the HTTP surface: routing, middleware (panic recovery, rate limiting, logging),
// authentication, and JSON helpers. Handlers live in handlers.go.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"openmarkets/server/internal/config"
	"openmarkets/server/internal/id"
	"openmarkets/server/internal/market"
	"openmarkets/server/internal/store"
)

// Server holds the dependencies shared across handlers.
type Server struct {
	cfg         config.Config
	store       store.Store
	params      market.Params
	limiter     *limiter
	acctLimiter *slidingLimiter // stricter per-IP limit on UNAUTHENTICATED account creation
	logger      *log.Logger
}

// New builds a Server. logger may be nil (defaults to the standard logger).
func New(cfg config.Config, st store.Store, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	if cfg.Console && cfg.ConsoleToken == "" {
		logger.Printf("WARNING: /console is served WITHOUT a token (OM_CONSOLE=1, OM_CONSOLE_TOKEN unset) — " +
			"the operator console is a full co-op counterparty tool; set OM_CONSOLE_TOKEN or OM_CONSOLE=0 on a public server")
	}
	return &Server{
		cfg:   cfg,
		store: st,
		params: market.Params{
			VolumeRef: cfg.VolumeRef,
			Min:       cfg.IndexMin,
			Max:       cfg.IndexMax,
		},
		limiter:     newLimiter(cfg.RatePerMin),
		acctLimiter: newSlidingLimiter(cfg.AcctPerHour, time.Hour),
		logger:      logger,
	}
}

// Handler returns the fully-wrapped http.Handler (routes + global middleware).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz) // k3s liveness/readiness: no auth, no rate limit
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /eol", s.handleEOL)
	mux.HandleFunc("POST /accounts", s.handleCreateAccount)
	mux.HandleFunc("POST /accounts/name", s.handleSetAccountName)
	mux.HandleFunc("GET /leagues", s.handleListLeagues)
	mux.HandleFunc("POST /leagues", s.handleCreateLeague)
	mux.HandleFunc("POST /leagues/join", s.handleJoinLeague)
	mux.HandleFunc("GET /leagues/members", s.handleLeagueMembers)
	mux.HandleFunc("POST /report", s.handleReport)
	mux.HandleFunc("POST /report/batch", s.handleReportBatch)
	mux.HandleFunc("GET /index", s.handleIndex)
	mux.HandleFunc("GET /prices", s.handlePrices)
	// Trades (two-sided basket), bonds, and the settlement-event feed (Phase 2a).
	mux.HandleFunc("POST /trades", s.handleOfferTrade)
	mux.HandleFunc("GET /trades", s.handleListTrades)
	mux.HandleFunc("POST /trades/{id}/accept", s.handleTradeTransition("accept"))
	mux.HandleFunc("POST /trades/{id}/decline", s.handleTradeTransition("decline"))
	mux.HandleFunc("POST /trades/{id}/cancel", s.handleTradeTransition("cancel"))
	mux.HandleFunc("POST /trades/{id}/settle", s.handleSettleTrade)
	mux.HandleFunc("POST /trades/{id}/shortfall", s.handleTradeShortfall)
	mux.HandleFunc("GET /bonds", s.handleListBonds)
	mux.HandleFunc("POST /bonds/{id}/settle", s.handleSettleBond)
	// Manual loan negotiation (Phase 3): offer → counter/accept/decline/cancel (offers appear in GET /bonds).
	mux.HandleFunc("POST /loans", s.handleOfferLoan)
	mux.HandleFunc("POST /loans/{id}/counter", s.handleCounterLoan)
	mux.HandleFunc("POST /loans/{id}/accept", s.handleAcceptLoan)
	mux.HandleFunc("POST /loans/{id}/decline", s.handleLoanTransition("decline"))
	mux.HandleFunc("POST /loans/{id}/cancel", s.handleLoanTransition("cancel"))
	mux.HandleFunc("GET /settlements", s.handleSettlements)
	mux.HandleFunc("GET /citystate", s.handleCityState)
	mux.HandleFunc("POST /cityprofile", s.handleCityProfile)           // the caller's city snapshot for leaguemates
	mux.HandleFunc("GET /cityprofile/history", s.handleCityHistory)    // a leaguemate's retained city time-series + net-§ curve
	mux.HandleFunc("GET /investments", s.handleInvestments)            // league-wide active investments + durable history
	mux.HandleFunc("POST /investment-office", s.handleInvestmentOffer) // M8 co-op lever: grant a friend a buff
	mux.HandleFunc("POST /bailout", s.handleBailout)                   // M8 co-op lever: pay down a friend's defaulted debt
	mux.HandleFunc("GET /audit", s.handleAudit)
	mux.HandleFunc("GET /feed", s.handleFeed)                              // recent league activity feed, newest-first (member-only)
	mux.HandleFunc("GET /chronicle", s.handleChronicle)                   // the league's narrated saga, oldest→newest (member-only)
	mux.HandleFunc("GET /chronicle/onthisday", s.handleChronicleOnThisDay) // "on this day in league history" (member-only)
	mux.HandleFunc("GET /crises", s.handleCrises)                          // active shared league crises (any account; global)
	// Co-op MEGAPROJECTS (Great Works, social slice 4): view a league's projects + contribute commodities/§ (member-only).
	mux.HandleFunc("GET /projects", s.handleProjects)
	mux.HandleFunc("POST /projects/{id}/contribute-gold", s.handleContributeProjectGold)
	mux.HandleFunc("POST /projects/{id}/contribute-goods", s.handleContributeProjectGoods)
	mux.HandleFunc("GET /leaderboards", s.handleLeaderboards)              // per-league ranked boards + titles (member-only)
	mux.HandleFunc("GET /global-leaderboards", s.handleGlobalLeaderboards) // cross-league anonymized boards (any account)
	// Admin surface — token-gated (OM_CONSOLE_TOKEN). Registered always; each handler 401s unless the token is set
	// AND matches (admin must be explicitly enabled, even locally — these routes are destructive).
	mux.HandleFunc("GET /admin/stats", s.handleAdminStats)
	mux.HandleFunc("GET /admin/leagues", s.handleAdminLeagues)
	mux.HandleFunc("GET /admin/leagues/{id}", s.handleAdminLeague)
	mux.HandleFunc("POST /admin/leagues/{id}/delete", s.handleAdminDeleteLeague)
	mux.HandleFunc("POST /admin/leagues/{id}/kick", s.handleAdminKick)
	if s.cfg.Console {
		mux.HandleFunc("GET /console", s.handleConsole)
	}
	return s.recover(s.rateLimit(s.logRequests(mux)))
}

// ---- middleware ----

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Printf("panic: %v", rec)
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{
			ResponseWriter: w,
			status:         http.StatusOK,
			logger:         s.logger,
			reqID:          id.Short(),
			method:         r.Method,
			path:           r.URL.Path,
			clientIP:       s.clientIP(r),
		}
		next.ServeHTTP(sw, r)
		// Access log: method, path, status, request id, client IP, and the ?league= param when present (so a
		// "my trade failed" report can be tied to a specific league + request id). HTTP-request granularity — fine
		// to log per call (not a per-frame hot path).
		league := r.URL.Query().Get("league")
		if league == "" {
			league = r.URL.Query().Get("leagueId")
		}
		s.logger.Printf("req=%s %s %s -> %d ip=%s league=%q (%s)",
			sw.reqID, r.Method, r.URL.Path, sw.status, sw.clientIP, league, time.Since(start).Round(time.Millisecond))
	})
}

func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /healthz is the k3s liveness/readiness probe — never rate-limit it (a busy node's frequent probes must
		// not 429 and trigger a restart loop).
		if r.URL.Path != "/healthz" && !s.limiter.allow(s.clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusWriter wraps the ResponseWriter to capture the response status, and carries the per-request access-log
// context (request id + method/path/clientIP + the logger) so writeErr can log a non-2xx response without
// threading *http.Request through all ~100 call sites.
type statusWriter struct {
	http.ResponseWriter
	status   int
	logger   *log.Logger
	reqID    string
	method   string
	path     string
	clientIP string
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// ---- auth ----

// authAccount resolves and verifies the caller from either an Authorization: Bearer <id>.<secret>
// header or ?account=&secret= query params (the latter lets a bare UnityWebRequest.Get authenticate
// without custom headers). Returns the account id on success.
func (s *Server) authAccount(r *http.Request) (string, bool) {
	accountID, secret := extractCreds(r)
	if accountID == "" || secret == "" {
		return "", false
	}
	a, err := s.store.GetAccount(accountID)
	if err != nil {
		return "", false
	}
	if !id.Verify(a.Salt, a.SecretHash, secret) {
		return "", false
	}
	s.store.Touch(accountID) // runtime online signal for the due-clock's offline grace
	return accountID, true
}

func extractCreds(r *http.Request) (account, secret string) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tok := strings.TrimPrefix(h, "Bearer ")
		if i := strings.IndexByte(tok, '.'); i > 0 {
			return tok[:i], tok[i+1:]
		}
	}
	return r.URL.Query().Get("account"), r.URL.Query().Get("secret")
}

// ---- JSON helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	// Log non-2xx responses (status + message + path + request id) so failures are debuggable from the access log
	// alone. The access-log context rides on the statusWriter the logRequests middleware installs; when w isn't a
	// statusWriter (e.g. the panic path before middleware, or a direct test call) we just skip the extra log line.
	if sw, ok := w.(*statusWriter); ok && sw.logger != nil && (status < 200 || status >= 300) {
		sw.logger.Printf("req=%s %s %s -> %d ip=%s error=%q",
			sw.reqID, sw.method, sw.path, status, sw.clientIP, msg)
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	// io.LimitReader caps the body (a body over the limit decodes to a clean error, not a 413), avoiding the
	// nil-ResponseWriter panic that http.MaxBytesReader(nil, ...) would raise when the limit is crossed.
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	// Reject trailing data after the JSON value (a single object is expected) — io.LimitReader caps the read
	// but doesn't stop a valid small value followed by garbage (Codex LOW).
	if dec.More() {
		return errTrailingData
	}
	return nil
}

var errTrailingData = errors.New("unexpected data after JSON value")

// clientIP resolves the caller's IP for rate-limiting + access logs. By default it keys on RemoteAddr only
// (so a client can't spoof its bucket via headers). When cfg.TrustProxy is set — i.e. the server sits behind a
// trusted reverse proxy / Cloudflare Tunnel that rewrites RemoteAddr to the proxy's own address — it honors
// CF-Connecting-IP, then the FIRST hop of X-Forwarded-For, then RemoteAddr, so each league member gets their
// own bucket instead of the whole league sharing the proxy's address.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" {
			return cf
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				xff = xff[:i]
			}
			if first := strings.TrimSpace(xff); first != "" {
				return first
			}
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// ---- rate limiter (fixed-window per key) ----

type limiter struct {
	perMin int
	mu     sync.Mutex
	window time.Time
	counts map[string]int
}

func newLimiter(perMin int) *limiter {
	return &limiter{perMin: perMin, counts: map[string]int{}}
}

// allow reports whether key may make another request in the current minute. perMin <= 0 disables.
func (l *limiter) allow(key string) bool {
	if l.perMin <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().Truncate(time.Minute)
	if now != l.window {
		l.window = now
		l.counts = map[string]int{}
	}
	l.counts[key]++
	return l.counts[key] <= l.perMin
}

// ---- sliding-window per-key limiter (account creation) ----
//
// A dedicated, stricter limiter for the UNAUTHENTICATED POST /accounts endpoint, kept separate from the general
// per-minute limiter so abusive account minting is throttled without affecting normal API traffic. A true sliding
// window (per-key timestamp slice) rather than a fixed window, so a burst can't straddle a window boundary and
// double the effective allowance. Mutex-guarded; max <= 0 disables (local testing).
type slidingLimiter struct {
	max    int
	window time.Duration
	mu     sync.Mutex
	hits   map[string][]time.Time
}

func newSlidingLimiter(max int, window time.Duration) *slidingLimiter {
	return &slidingLimiter{max: max, window: window, hits: map[string][]time.Time{}}
}

// allow reports whether key may perform another action now, recording it if so. Evicts timestamps older than the
// window before counting. max <= 0 disables (always allows, no bookkeeping).
func (l *slidingLimiter) allow(key string) bool {
	if l.max <= 0 {
		return true
	}
	now := time.Now()
	cutoff := now.Add(-l.window)
	l.mu.Lock()
	defer l.mu.Unlock()
	// Drop expired timestamps for this key (also bounds the slice; an idle key keeps at most `max` entries).
	src := l.hits[key]
	kept := src[:0]
	for _, t := range src {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}
