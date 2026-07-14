package api

import (
	"errors"
	"net/http"
	"sort"
	"strconv"

	"openmarkets/server/internal/store"
)

// Wire shape for a basket offer. Line values are NOT accepted from the client — the server freezes them at
// accept time (base price × league index), so a malicious offerer can't set its own valuation (Codex #7).
type tradeLineReq struct {
	Kind      string `json:"kind"`                // "commodity" | "gold"
	Commodity string `json:"commodity,omitempty"` // commodity lines
	QtyFixed  int64  `json:"qtyFixed,omitempty"`  // commodity lines, scaled by money.QtyScale
	GoldCents int64  `json:"goldCents,omitempty"` // gold lines
	Dir       string `json:"dir"`                 // "give" | "take" (relative to the offerer)
}

type tradeOfferReq struct {
	LeagueID       string         `json:"leagueId"`
	Counterparty   string         `json:"counterparty"`
	DefaultRateBps int64          `json:"defaultRateBps"`
	Installments   int            `json:"installments"`
	Items          []tradeLineReq `json:"items"`
}

// POST /trades — offer a two-sided basket to a leaguemate.
func (s *Server) handleOfferTrade(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	var req tradeOfferReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	items := make([]store.LineItem, 0, len(req.Items))
	for _, li := range req.Items {
		items = append(items, store.LineItem{
			Kind: li.Kind, Commodity: li.Commodity, QtyFixed: li.QtyFixed, GoldCents: li.GoldCents, Dir: li.Dir,
		})
	}
	t, err := s.store.CreateTrade(store.Trade{
		LeagueID: req.LeagueID, OfferedBy: accountID, Counterparty: req.Counterparty,
		DefaultRateBps: req.DefaultRateBps, Installments: req.Installments, Items: items,
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusForbidden, "both parties must be members of the league")
	case err != nil: // ErrConflict, ErrEmptyTrade, ErrBadLine — all bad terms
		writeErr(w, http.StatusBadRequest, "invalid trade terms (check items, installments, and default rate floor)")
	default:
		writeJSON(w, http.StatusCreated, t)
	}
}

// GET /trades?league=ID — the caller's trades in a league (newest first).
func (s *Server) handleListTrades(w http.ResponseWriter, r *http.Request) {
	accountID, leagueID, ok := s.authMember(w, r)
	if !ok {
		return
	}
	list, err := s.store.TradesFor(leagueID, accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list trades")
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
	writeJSON(w, http.StatusOK, map[string]any{"trades": list})
}

// handleTradeTransition handles accept/decline/cancel.
func (s *Server) handleTradeTransition(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := s.authAccount(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		t, err := s.store.SetTradeStatus(accountID, r.PathValue("id"), action)
		s.writeTradeResult(w, t, err)
	}
}

// POST /trades/{id}/settle — the net payer books the current installment.
func (s *Server) handleSettleTrade(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	t, ev, err := s.store.SettleTradeInstallment(accountID, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such trade")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "not allowed in the trade's current state")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not settle trade")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"trade": t, "event": ev})
	}
}

type shortfallReq struct {
	Installment int   `json:"installment"`
	Cents       int64 `json:"cents"` // frozen value of the goods the caller could not deliver
}

// POST /trades/{id}/shortfall — the caller reports an undelivered give-goods shortfall for an installment; the
// server mints a cash-debt bond (caller → other party) at the trade's default rate and dings reliability (M6).
func (s *Server) handleTradeShortfall(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	var req shortfallReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	t, b, err := s.store.ReportTradeShortfall(accountID, r.PathValue("id"), req.Installment, req.Cents)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such trade")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "already reported, or not allowed in the trade's current state")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not record shortfall")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"trade": t, "bond": b})
	}
}

func (s *Server) writeTradeResult(w http.ResponseWriter, t store.Trade, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such trade")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "not allowed in the trade's current state")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not update trade")
	default:
		writeJSON(w, http.StatusOK, t)
	}
}

// GET /bonds?league=ID — the caller's bonds (as creditor or debtor), newest first.
func (s *Server) handleListBonds(w http.ResponseWriter, r *http.Request) {
	accountID, leagueID, ok := s.authMember(w, r)
	if !ok {
		return
	}
	list, err := s.store.BondsFor(leagueID, accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list bonds")
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
	writeJSON(w, http.StatusOK, map[string]any{"bonds": list})
}

// POST /bonds/{id}/settle — the debtor books one repayment.
func (s *Server) handleSettleBond(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	b, ev, err := s.store.SettleBondInstallment(accountID, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such bond (or you are not the debtor)")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "not allowed in the bond's current state")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not settle bond")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"bond": b, "event": ev})
	}
}

// GET /audit?league=ID — per-account net online cash + the conservation total (expected 0). Member-only; a
// sanity/invariant check (a non-zero total would signal a non-conserving settlement event).
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	_, leagueID, ok := s.authMember(w, r)
	if !ok {
		return
	}
	net, total, err := s.store.AuditLeague(leagueID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such league")
		return
	}
	accounts := make([]map[string]any, 0, len(net))
	for id, cents := range net {
		accounts = append(accounts, map[string]any{"accountId": id, "netCents": cents})
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i]["accountId"].(string) < accounts[j]["accountId"].(string) })
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "accounts": accounts})
}

// GET /citystate?league=ID — the caller's austerity status in a league (for the in-game austerity banner).
func (s *Server) handleCityState(w http.ResponseWriter, r *http.Request) {
	accountID, leagueID, ok := s.authMember(w, r)
	if !ok {
		return
	}
	austerity, outstanding, defaulted := s.store.CityState(leagueID, accountID)
	writeJSON(w, http.StatusOK, map[string]any{
		"austerity":            austerity,
		"outstandingDebtCents": outstanding,
		"defaultedBonds":       defaulted,
		"effects":              s.store.CityEffects(leagueID, accountID),       // M8: active co-op buffs RECEIVED by this city
		"investmentsMade":      s.store.CityEffectsIssued(leagueID, accountID), // active investments this city has GRANTED to others
		// The wall-clock due period (seconds). The client learns it here and paces its auto-settle sweep to match,
		// so it stays in step whether the period is the 45-min default or a low value set for testing.
		"dueIntervalSec": int64(s.cfg.DueInterval.Seconds()),
	})
}

// GET /investments?league=ID — full league transparency: every ACTIVE investment (issuer→grantee) plus the durable
// HISTORY of all investments ever made (from the settlement-event log, so it survives buff expiry). Member-only.
func (s *Server) handleInvestments(w http.ResponseWriter, r *http.Request) {
	_, leagueID, ok := s.authMember(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"active":  s.store.LeagueEffects(leagueID),
		"history": s.store.InvestmentHistory(leagueID),
	})
}

// POST /investment-office — grant a leaguemate (the "office" beneficiary) a temporary demand+attractiveness buff,
// paying a symmetric § cost that TRANSFERS to them (a real investment; conserves cash). Body:
// {"league":ID,"granteeId":ID,"costCents":N,"days":N}. Member-only; the caller is the issuer.
func (s *Server) handleInvestmentOffer(w http.ResponseWriter, r *http.Request) {
	accountID, leagueID, ok := s.authMember(w, r)
	if !ok {
		return
	}
	var req struct {
		League     string `json:"league"`    // accepted but UNUSED — the league comes from authMember's ?league=. Present
		GranteeID  string `json:"granteeId"` // so the strict (DisallowUnknownFields) decoder accepts clients that also
		CostCents  int64  `json:"costCents"` // send league in the body (the original client + console did).
		Days       int    `json:"days"`
		DemandKind string `json:"demandKind"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.GranteeID == "" {
		writeErr(w, http.StatusBadRequest, "granteeId required")
		return
	}
	if req.CostCents < store.InvestMinCostCents || req.CostCents > store.InvestMaxCostCents {
		writeErr(w, http.StatusBadRequest, "costCents out of range")
		return
	}
	if req.DemandKind == "" {
		req.DemandKind = store.DemandResidential // back-compat default for older clients that don't send a kind
	} else if !store.ValidDemandKind(req.DemandKind) {
		writeErr(w, http.StatusBadRequest, "demandKind must be res, com, or work")
		return
	}
	e, ev, err := s.store.GrantInvestment(leagueID, accountID, req.GranteeID, req.CostCents, req.Days, req.DemandKind)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "grantee is not a league member")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "cannot invest in yourself, or an investment from you to them is already active")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not grant investment")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"effect": e, "event": ev})
	}
}

// POST /bailout — voluntarily pay down a leaguemate's defaulted bonds (oldest first), helping them escape austerity.
// The § transfers to each bond's creditor (conserves cash). Body: {"league":ID,"debtorId":ID,"cents":N}. Member-only.
func (s *Server) handleBailout(w http.ResponseWriter, r *http.Request) {
	accountID, leagueID, ok := s.authMember(w, r)
	if !ok {
		return
	}
	var req struct {
		League   string `json:"league"`   // accepted but UNUSED (authMember reads ?league=); present so the strict decoder
		DebtorID string `json:"debtorId"` // accepts clients that also send league in the body.
		Cents    int64  `json:"cents"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.DebtorID == "" || req.Cents <= 0 {
		writeErr(w, http.StatusBadRequest, "debtorId and a positive cents are required")
		return
	}
	applied, events, err := s.store.BailoutCity(accountID, leagueID, req.DebtorID, req.Cents)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "debtor is not a league member")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "that city has no defaulted debt you can bail out")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not bail out")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"appliedCents": applied, "events": events})
	}
}

// GET /settlements?league=ID&since=SEQ — the league's settlement events after SEQ, for idempotent client booking.
func (s *Server) handleSettlements(w http.ResponseWriter, r *http.Request) {
	accountID, leagueID, ok := s.authMember(w, r)
	if !ok {
		return
	}
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "invalid since")
			return
		}
		since = n
	}
	evs, latestSeq, err := s.store.SettlementsForAccount(leagueID, accountID, since)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list settlements")
		return
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].Seq < evs[j].Seq })
	// epoch lets the client detect a genuine server data wipe (epoch changed) and safely reset its cursor;
	// latestSeq is informational. A bare seq comparison is NOT a safe reset signal.
	writeJSON(w, http.StatusOK, map[string]any{"events": evs, "latestSeq": latestSeq, "epoch": s.store.Epoch()})
}

// authMember authenticates the caller and verifies league membership (the ?league= query param). On failure it
// writes the response and returns ok=false. Shared by the GET list endpoints.
func (s *Server) authMember(w http.ResponseWriter, r *http.Request) (accountID, leagueID string, ok bool) {
	accountID, ok = s.authAccount(r)
	if !ok {
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
