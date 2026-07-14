package api

import (
	"errors"
	"net/http"

	"openmarkets/server/internal/store"
)

type loanOfferReq struct {
	LeagueID       string `json:"leagueId"`
	Role           string `json:"role"` // "lend" | "borrow" (the initiator's role)
	Counterparty   string `json:"counterparty"`
	PrincipalCents int64  `json:"principalCents"`
	InterestBps    int64  `json:"interestBps"`
	Installments   int    `json:"installments"`
}

type loanTermsReq struct {
	PrincipalCents int64 `json:"principalCents"`
	InterestBps    int64 `json:"interestBps"`
	Installments   int   `json:"installments"`
}

// POST /loans — offer a negotiated peer loan, as lender ("lend") or borrower ("borrow").
func (s *Server) handleOfferLoan(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	var req loanOfferReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	var lender, borrower string
	switch req.Role {
	case "lend":
		lender, borrower = accountID, req.Counterparty
	case "borrow":
		lender, borrower = req.Counterparty, accountID
	default:
		writeErr(w, http.StatusBadRequest, "role must be lend|borrow")
		return
	}
	b, err := s.store.OfferLoan(store.Bond{
		LeagueID: req.LeagueID, CreditorID: lender, DebtorID: borrower,
		PrincipalCents: req.PrincipalCents, InterestBps: req.InterestBps, Installments: req.Installments,
		ProposedBy: accountID,
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusForbidden, "both parties must be members of the league")
	case err != nil:
		writeErr(w, http.StatusBadRequest, "invalid loan terms")
	default:
		writeJSON(w, http.StatusCreated, b)
	}
}

// POST /loans/{id}/counter — revise the terms and hand the turn back.
func (s *Server) handleCounterLoan(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	var req loanTermsReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	b, err := s.store.CounterLoan(accountID, r.PathValue("id"), req.PrincipalCents, req.InterestBps, req.Installments)
	s.writeLoanResult(w, b, err)
}

// POST /loans/{id}/accept — activate the loan; the principal transfers lender→borrower.
func (s *Server) handleAcceptLoan(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authAccount(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	b, ev, err := s.store.AcceptLoan(accountID, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such loan offer")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "not allowed in the offer's current state")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not accept loan")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"bond": b, "event": ev})
	}
}

// handleLoanTransition handles decline/cancel.
func (s *Server) handleLoanTransition(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := s.authAccount(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		b, err := s.store.SetLoanStatus(accountID, r.PathValue("id"), action)
		s.writeLoanResult(w, b, err)
	}
}

func (s *Server) writeLoanResult(w http.ResponseWriter, b store.Bond, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such loan offer")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "not allowed in the offer's current state")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not update loan")
	default:
		writeJSON(w, http.StatusOK, b)
	}
}
