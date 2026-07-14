package api

import (
	"fmt"
	"net/http"
	"testing"
)

func TestLoanFlow_OfferCounterAccept(t *testing.T) {
	ts := tradeServer(t)
	a := createMember(t, ts) // lender
	b := createMember(t, ts) // borrower
	lid := leagueAB(t, ts, a, b)

	// A offers to lend B §100 (10000c) at 10% over 4.
	offer := fmt.Sprintf(`{"leagueId":%q,"role":"lend","counterparty":%q,"principalCents":10000,"interestBps":1000,"installments":4}`, lid, idOf(b))
	var created struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		ProposedBy string `json:"proposedBy"`
	}
	if code := do(t, ts, "POST", "/loans", a, offer, &created); code != http.StatusCreated {
		t.Fatalf("offer: %d", code)
	}
	if created.Status != "offered" || created.ProposedBy != idOf(a) {
		t.Fatalf("offered = status %q proposedBy %q", created.Status, created.ProposedBy)
	}

	// The proposer (A) can't accept their own standing terms.
	if code := do(t, ts, "POST", "/loans/"+created.ID+"/accept", a, "", nil); code != http.StatusConflict {
		t.Fatalf("proposer accept: %d, want 409", code)
	}
	// B counters to 20%.
	counter := `{"principalCents":10000,"interestBps":2000,"installments":4}`
	if code := do(t, ts, "POST", "/loans/"+created.ID+"/counter", b, counter, nil); code != http.StatusOK {
		t.Fatalf("counter: %d", code)
	}
	// Now it's A's turn; A accepts → principal flows A→B.
	var accepted struct {
		Bond struct {
			Status        string `json:"status"`
			TotalDueCents int64  `json:"totalDueCents"`
		} `json:"bond"`
		Event struct {
			PayerID    string `json:"payerId"`
			ReceiverID string `json:"receiverId"`
			Cents      int64  `json:"cents"`
		} `json:"event"`
	}
	if code := do(t, ts, "POST", "/loans/"+created.ID+"/accept", a, "", &accepted); code != http.StatusOK {
		t.Fatalf("accept: %d", code)
	}
	if accepted.Bond.Status != "active" || accepted.Bond.TotalDueCents != 12000 {
		t.Errorf("accepted bond = %q / %d, want active/12000", accepted.Bond.Status, accepted.Bond.TotalDueCents)
	}
	if accepted.Event.PayerID != idOf(a) || accepted.Event.ReceiverID != idOf(b) || accepted.Event.Cents != 10000 {
		t.Errorf("principal event = %+v, want A->B 10000", accepted.Event)
	}
}

func TestLoan_OfferRejectsBadRole(t *testing.T) {
	ts := tradeServer(t)
	a := createMember(t, ts)
	b := createMember(t, ts)
	lid := leagueAB(t, ts, a, b)
	bad := fmt.Sprintf(`{"leagueId":%q,"role":"gift","counterparty":%q,"principalCents":10000,"interestBps":1000,"installments":4}`, lid, idOf(b))
	if code := do(t, ts, "POST", "/loans", a, bad, nil); code != http.StatusBadRequest {
		t.Fatalf("bad role: %d, want 400", code)
	}
}
