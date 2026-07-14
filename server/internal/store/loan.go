package store

import (
	"openmarkets/server/internal/id"
	"openmarkets/server/internal/money"
)

// ── Manual bond lending (negotiated peer loans, Phase 3) ─────────────────────
//
// A manual bond is negotiated in-thread before it activates: an initiator OFFERS terms (as lender or borrower),
// the counterparty may COUNTER (revise the terms and bounce it back) or ACCEPT/DECLINE; the initiator may
// CANCEL. ProposedBy tracks whose terms stand — only the OTHER party may counter/accept/decline. On accept the
// principal transfers lender→borrower as a settlement event both sides book (conservation-safe), the schedule
// total is frozen, and repayment runs via the normal SettleBondInstallment path. Loan interest is fully
// negotiable (no floor; the default-rate floor only governs trade auto-bonds).

func (b Bond) loanParty(accountID string) bool {
	return accountID == b.CreditorID || accountID == b.DebtorID
}

// validLoanTerms checks negotiated terms (used at offer + counter). Reuses the bond-terms validation, which also
// enforces the min-principal floor, installment cap, and the per-installment bookable ceiling.
func (m *Memory) validLoanTerms(principalCents, interestBps int64, installments int) bool {
	_, err := money.ValidateBondTerms(principalCents, interestBps, installments, m.econ.MinPrincipalCents)
	return err == nil
}

// OfferLoan stores a new negotiated loan offer. b carries CreditorID (lender), DebtorID (borrower), the proposed
// terms, and ProposedBy (the initiator). Both parties must be league members and differ.
func (m *Memory) OfferLoan(b Bond) (Bond, error) {
	if b.CreditorID == "" || b.DebtorID == "" || b.CreditorID == b.DebtorID {
		return Bond{}, ErrConflict
	}
	if b.ProposedBy != b.CreditorID && b.ProposedBy != b.DebtorID {
		return Bond{}, ErrConflict
	}
	if !m.validLoanTerms(b.PrincipalCents, b.InterestBps, b.Installments) {
		return Bond{}, ErrConflict
	}
	m.mu.Lock()
	if !m.isMemberLocked(b.CreditorID, b.LeagueID) || !m.isMemberLocked(b.DebtorID, b.LeagueID) {
		m.mu.Unlock()
		return Bond{}, ErrNotFound
	}
	b.ID = id.New()
	b.Origin = BondOriginManual
	b.Status = BondOffered
	b.Settled = 0
	b.MissedCount = 0
	b.TotalDueCents = 0 // frozen at accept
	b.Created = m.clock()
	m.bonds[b.ID] = b
	m.mu.Unlock()
	return b, m.persist()
}

// CounterLoan revises an offered loan's terms and hands the turn back. Only a party who is NOT the current
// proposer may counter (you can't counter your own standing offer).
func (m *Memory) CounterLoan(accountID, bondID string, principalCents, interestBps int64, installments int) (Bond, error) {
	if !m.validLoanTerms(principalCents, interestBps, installments) {
		return Bond{}, ErrConflict
	}
	m.mu.Lock()
	b, ok := m.bonds[bondID]
	if !ok || b.Origin != BondOriginManual || !b.loanParty(accountID) {
		m.mu.Unlock()
		return Bond{}, ErrNotFound
	}
	if b.Status != BondOffered || accountID == b.ProposedBy {
		m.mu.Unlock()
		return Bond{}, ErrConflict
	}
	b.PrincipalCents = principalCents
	b.InterestBps = interestBps
	b.Installments = installments
	b.ProposedBy = accountID
	m.bonds[bondID] = b
	m.mu.Unlock()
	return b, m.persist()
}

// AcceptLoan activates an offered loan (only the non-proposer may accept): it freezes the schedule total and
// transfers the principal lender→borrower as a settlement event both sides book.
func (m *Memory) AcceptLoan(accountID, bondID string) (Bond, SettlementEvent, error) {
	m.mu.Lock()
	defer m.persistAfter()
	b, ok := m.bonds[bondID]
	if !ok || b.Origin != BondOriginManual || !b.loanParty(accountID) {
		m.mu.Unlock()
		return Bond{}, SettlementEvent{}, ErrNotFound
	}
	if b.Status != BondOffered || accountID == b.ProposedBy {
		m.mu.Unlock()
		return Bond{}, SettlementEvent{}, ErrConflict
	}
	if err := b.Activate(m.econ.MinPrincipalCents); err != nil {
		m.mu.Unlock()
		return Bond{}, SettlementEvent{}, ErrConflict
	}
	// Principal now flows lender (creditor) → borrower (debtor); repayments later flow the other way.
	ev := m.appendEventLocked(b.LeagueID, b.CreditorID, b.DebtorID, b.PrincipalCents, "loan:"+b.ID+":principal")
	m.bonds[bondID] = b
	m.mu.Unlock()
	return b, ev, nil
}

// SetLoanStatus declines (non-proposer rejects) or cancels (proposer withdraws) an offered loan.
func (m *Memory) SetLoanStatus(accountID, bondID, action string) (Bond, error) {
	m.mu.Lock()
	b, ok := m.bonds[bondID]
	if !ok || b.Origin != BondOriginManual || !b.loanParty(accountID) {
		m.mu.Unlock()
		return Bond{}, ErrNotFound
	}
	if b.Status != BondOffered {
		m.mu.Unlock()
		return Bond{}, ErrConflict
	}
	switch action {
	case "decline":
		if accountID == b.ProposedBy { // the proposer can't decline their own standing terms
			m.mu.Unlock()
			return Bond{}, ErrConflict
		}
		b.Status = BondDeclined
	case "cancel":
		if accountID != b.ProposedBy { // only the party whose terms stand may withdraw
			m.mu.Unlock()
			return Bond{}, ErrConflict
		}
		b.Status = BondCancelled
	default:
		m.mu.Unlock()
		return Bond{}, ErrConflict
	}
	m.bonds[bondID] = b
	m.mu.Unlock()
	return b, m.persist()
}
