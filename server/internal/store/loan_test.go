package store

import "testing"

func TestLoan_NegotiateCounterAccept_AndReputation(t *testing.T) {
	m, a, b, lid := tradeLeague(t) // a = lender, b = borrower (we'll have b initiate a borrow-request)

	// b requests to borrow §100 (10000c) at 10% over 4 from a; b is the lender? No — b borrows, so lender=a.
	off, err := m.OfferLoan(Bond{
		LeagueID: lid, CreditorID: a, DebtorID: b, PrincipalCents: 10000, InterestBps: 1000, Installments: 4,
		ProposedBy: b, // borrower initiated
	})
	if err != nil || off.Status != BondOffered || off.ProposedBy != b {
		t.Fatalf("offer: %v status=%s proposedBy=%s", err, off.Status, off.ProposedBy)
	}
	// The proposer (b) cannot accept or counter their own standing terms.
	if _, _, err := m.AcceptLoan(b, off.ID); err != ErrConflict {
		t.Errorf("proposer accept: got %v, want ErrConflict", err)
	}
	if _, err := m.CounterLoan(b, off.ID, 10000, 1000, 4); err != ErrConflict {
		t.Errorf("proposer counter: got %v, want ErrConflict", err)
	}
	// a (lender) counters: same principal but 20% interest, and now it's b's turn.
	c, err := m.CounterLoan(a, off.ID, 10000, 2000, 4)
	if err != nil || c.InterestBps != 2000 || c.ProposedBy != a {
		t.Fatalf("counter: %v bps=%d proposedBy=%s", err, c.InterestBps, c.ProposedBy)
	}
	// b accepts a's terms → principal flows a→b; total frozen at 12000 (10000 + 20%).
	acc, ev, err := m.AcceptLoan(b, off.ID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if acc.Status != BondActive || acc.TotalDueCents != 12000 {
		t.Errorf("accepted bond = status %s total %d, want active/12000", acc.Status, acc.TotalDueCents)
	}
	if ev.PayerID != a || ev.ReceiverID != b || ev.Cents != 10000 {
		t.Errorf("principal event = %+v, want a->b 10000", ev)
	}

	// Repay one installment (b is debtor) → on-time reputation for b.
	if _, _, err := m.SettleBondInstallment(b, off.ID); err != nil {
		t.Fatalf("repay: %v", err)
	}
	acctB, _ := m.GetAccount(b)
	if acctB.OnTimeCount != 1 || acctB.Reliability() != 100 {
		t.Errorf("borrower reputation = onTime %d rel %d, want 1/100", acctB.OnTimeCount, acctB.Reliability())
	}
}

func TestLoan_DeclineAndCancel(t *testing.T) {
	m, a, b, lid := tradeLeague(t)
	mk := func() Bond {
		o, err := m.OfferLoan(Bond{LeagueID: lid, CreditorID: a, DebtorID: b, PrincipalCents: 10000, InterestBps: 1000, Installments: 4, ProposedBy: a})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	// The non-proposer (b) declines.
	o1 := mk()
	if got, err := m.SetLoanStatus(b, o1.ID, "decline"); err != nil || got.Status != BondDeclined {
		t.Errorf("decline: %v status=%s", err, got.Status)
	}
	// The proposer (a) cancels their own offer; the non-proposer cannot cancel.
	o2 := mk()
	if _, err := m.SetLoanStatus(b, o2.ID, "cancel"); err != ErrConflict {
		t.Errorf("non-proposer cancel: got %v, want ErrConflict", err)
	}
	if got, err := m.SetLoanStatus(a, o2.ID, "cancel"); err != nil || got.Status != BondCancelled {
		t.Errorf("cancel: %v status=%s", err, got.Status)
	}
}

// Codex MEDIUM fix: paying a bond installment LATE (while delinquent) must not earn on-time credit — otherwise
// one late installment counts as both missed and on-time.
func TestReputation_LateRepayEarnsNoOnTimeCredit(t *testing.T) {
	m, a, b, lid := tradeLeague(t)
	off, err := m.OfferLoan(Bond{LeagueID: lid, CreditorID: a, DebtorID: b, PrincipalCents: 10000, InterestBps: 1000, Installments: 4, ProposedBy: a})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.AcceptLoan(b, off.ID); err != nil { // b (borrower) accepts → active
		t.Fatal(err)
	}
	if _, _, err := m.MissBondInstallment(off.ID); err != nil { // → delinquent, debtor missed++
		t.Fatal(err)
	}
	if bd, _ := m.GetBond(off.ID); bd.Status != BondDelinquent {
		t.Fatalf("want delinquent, got %s", bd.Status)
	}
	if _, _, err := m.SettleBondInstallment(b, off.ID); err != nil { // late catch-up → no on-time credit
		t.Fatal(err)
	}
	acct, _ := m.GetAccount(b)
	if acct.OnTimeCount != 0 || acct.MissedCount != 1 {
		t.Errorf("late-cure reputation = onTime %d missed %d, want 0/1", acct.OnTimeCount, acct.MissedCount)
	}
}

func TestLoan_RejectsBadTerms(t *testing.T) {
	m, a, b, lid := tradeLeague(t)
	// Below the min-principal floor.
	if _, err := m.OfferLoan(Bond{LeagueID: lid, CreditorID: a, DebtorID: b, PrincipalCents: 1, InterestBps: 1000, Installments: 1, ProposedBy: a}); err != ErrConflict {
		t.Errorf("dust principal: got %v, want ErrConflict", err)
	}
	// Non-member borrower.
	if _, err := m.OfferLoan(Bond{LeagueID: lid, CreditorID: a, DebtorID: "ghost", PrincipalCents: 10000, InterestBps: 1000, Installments: 4, ProposedBy: a}); err != ErrNotFound {
		t.Errorf("ghost borrower: got %v, want ErrNotFound", err)
	}
}
