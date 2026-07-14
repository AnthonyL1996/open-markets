package api

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"

	"openmarkets/server/internal/config"
	"openmarkets/server/internal/store"
)

// tradeServer builds a test server whose store has an accept-time pricer (Oil 400, Coal 150 cents/unit).
func tradeServer(t *testing.T) *testServer {
	t.Helper()
	cfg := config.Load()
	cfg.RatePerMin = 0
	st := store.NewMemory("")
	st.SetPricer(func(_ string, c string) (int64, bool) {
		v, ok := map[string]int64{"Oil": 400, "Coal": 150}[c]
		return v, ok
	})
	srv := New(cfg, st, log.New(io.Discard, "", 0))
	ts := newHandlerTestServer(t, srv.Handler())
	return ts
}

func idOf(bearer string) string { return strings.SplitN(bearer, ".", 2)[0] }

// leagueAB creates a league owned by A, joined by B; returns the league id.
func leagueAB(t *testing.T, ts *testServer, aTok, bTok string) string {
	t.Helper()
	var lg struct{ LeagueID, JoinCode string }
	if code := do(t, ts, "POST", "/leagues", aTok, `{"name":"L"}`, &lg); code != http.StatusCreated {
		t.Fatalf("create league: %d", code)
	}
	var jr struct{ LeagueID string }
	if code := do(t, ts, "POST", "/leagues/join", bTok, fmt.Sprintf(`{"joinCode":%q}`, lg.JoinCode), &jr); code != http.StatusOK {
		t.Fatalf("join league: %d", code)
	}
	return lg.LeagueID
}

func TestTradeShortfall_MintsCappedBondIdempotent(t *testing.T) {
	ts := tradeServer(t)
	a := createMember(t, ts)
	b := createMember(t, ts)
	lid := leagueAB(t, ts, a, b)

	// A gives 100 Oil (frozen value 40000 = §400), takes §10 gold; 1 installment, 20% default rate.
	offer := fmt.Sprintf(`{"leagueId":%q,"counterparty":%q,"defaultRateBps":2000,"installments":1,
		"items":[{"kind":"commodity","commodity":"Oil","qtyFixed":100000,"dir":"give"},
		         {"kind":"gold","goldCents":1000,"dir":"take"}]}`, lid, idOf(b))
	var created struct {
		ID string `json:"id"`
	}
	if code := do(t, ts, "POST", "/trades", a, offer, &created); code != http.StatusCreated {
		t.Fatalf("offer: %d", code)
	}
	if code := do(t, ts, "POST", "/trades/"+created.ID+"/accept", b, "", nil); code != http.StatusOK {
		t.Fatalf("accept: %d", code)
	}

	// A under-delivers its Oil on installment 0 and OVER-reports (999999) → bond capped at A's give value 40000,
	// debtor = A (the shorter), creditor = B, at the trade's default rate, 1 installment, shortfall origin.
	var res struct {
		Bond struct {
			DebtorID       string `json:"debtorId"`
			CreditorID     string `json:"creditorId"`
			Origin         string `json:"origin"`
			PrincipalCents int64  `json:"principalCents"`
			InterestBps    int64  `json:"interestBps"`
			Installments   int    `json:"installments"`
			Status         string `json:"status"`
		} `json:"bond"`
	}
	if code := do(t, ts, "POST", "/trades/"+created.ID+"/shortfall", a, `{"installment":0,"cents":999999}`, &res); code != http.StatusOK {
		t.Fatalf("shortfall: %d", code)
	}
	if res.Bond.DebtorID != idOf(a) || res.Bond.CreditorID != idOf(b) {
		t.Fatalf("bond parties debtor=%s creditor=%s, want %s/%s", res.Bond.DebtorID, res.Bond.CreditorID, idOf(a), idOf(b))
	}
	if res.Bond.PrincipalCents != 40000 {
		t.Fatalf("principal=%d, want 40000 (capped at give value)", res.Bond.PrincipalCents)
	}
	if res.Bond.InterestBps != 2000 || res.Bond.Installments != 1 {
		t.Fatalf("terms rate=%d inst=%d, want 2000/1", res.Bond.InterestBps, res.Bond.Installments)
	}
	if !strings.HasPrefix(res.Bond.Origin, "trade-shortfall:") {
		t.Fatalf("origin=%q, want trade-shortfall:*", res.Bond.Origin)
	}
	if res.Bond.Status != "active" {
		t.Fatalf("status=%q, want active", res.Bond.Status)
	}

	// Idempotent: a second report for the same installment is rejected (no double-mint).
	if code := do(t, ts, "POST", "/trades/"+created.ID+"/shortfall", a, `{"installment":0,"cents":40000}`, nil); code != http.StatusConflict {
		t.Fatalf("repeat shortfall: %d, want 409", code)
	}
}

func TestTradeFlow_OfferAcceptSettle(t *testing.T) {
	ts := tradeServer(t)
	a := createMember(t, ts)
	b := createMember(t, ts)
	lid := leagueAB(t, ts, a, b)

	// A offers: gives 100 Oil (+40000), takes §10.00 gold (+1000) → A nets +41000; B is the net payer.
	offer := fmt.Sprintf(`{"leagueId":%q,"counterparty":%q,"defaultRateBps":2000,"installments":1,
		"items":[{"kind":"commodity","commodity":"Oil","qtyFixed":100000,"dir":"give"},
		         {"kind":"gold","goldCents":1000,"dir":"take"}]}`, lid, idOf(b))
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if code := do(t, ts, "POST", "/trades", a, offer, &created); code != http.StatusCreated {
		t.Fatalf("offer: %d", code)
	}
	if created.Status != "offered" {
		t.Fatalf("status %q, want offered", created.Status)
	}

	// Offerer cannot accept their own trade.
	if code := do(t, ts, "POST", "/trades/"+created.ID+"/accept", a, "", nil); code != http.StatusConflict {
		t.Fatalf("offerer accept: %d, want 409", code)
	}
	// B accepts.
	if code := do(t, ts, "POST", "/trades/"+created.ID+"/accept", b, "", nil); code != http.StatusOK {
		t.Fatalf("accept: %d", code)
	}

	// A (the receiver) can't settle the nonzero installment; B (the payer) can.
	if code := do(t, ts, "POST", "/trades/"+created.ID+"/settle", a, "", nil); code != http.StatusConflict {
		t.Fatalf("receiver settle: %d, want 409", code)
	}
	var settled struct {
		Trade struct{ Status string } `json:"trade"`
		Event struct {
			PayerID    string `json:"payerId"`
			ReceiverID string `json:"receiverId"`
			Cents      int64  `json:"cents"`
		} `json:"event"`
	}
	if code := do(t, ts, "POST", "/trades/"+created.ID+"/settle", b, "", &settled); code != http.StatusOK {
		t.Fatalf("settle: %d", code)
	}
	if settled.Trade.Status != "completed" {
		t.Errorf("trade status %q, want completed", settled.Trade.Status)
	}
	if settled.Event.PayerID != idOf(b) || settled.Event.ReceiverID != idOf(a) || settled.Event.Cents != 41000 {
		t.Errorf("event = %+v, want B->A 41000", settled.Event)
	}

	// Settlement feed reflects the booked event.
	var feed struct {
		Events []struct {
			Seq   int64 `json:"seq"`
			Cents int64 `json:"cents"`
		} `json:"events"`
	}
	if code := do(t, ts, "GET", "/settlements?league="+lid+"&since=0", a, "", &feed); code != http.StatusOK {
		t.Fatalf("settlements: %d", code)
	}
	if len(feed.Events) != 1 || feed.Events[0].Cents != 41000 {
		t.Fatalf("feed = %+v, want one 41000 event", feed.Events)
	}
}

func TestTrade_OfferRejectsBadTerms(t *testing.T) {
	ts := tradeServer(t)
	a := createMember(t, ts)
	b := createMember(t, ts)
	lid := leagueAB(t, ts, a, b)

	// Below the league default-rate floor (20%).
	bad := fmt.Sprintf(`{"leagueId":%q,"counterparty":%q,"defaultRateBps":500,"installments":1,
		"items":[{"kind":"commodity","commodity":"Oil","qtyFixed":100000,"dir":"give"},
		         {"kind":"gold","goldCents":1000,"dir":"take"}]}`, lid, idOf(b))
	if code := do(t, ts, "POST", "/trades", a, bad, nil); code != http.StatusBadRequest {
		t.Fatalf("below floor: %d, want 400", code)
	}
	// One-sided basket (give only).
	oneSided := fmt.Sprintf(`{"leagueId":%q,"counterparty":%q,"defaultRateBps":2000,"installments":1,
		"items":[{"kind":"gold","goldCents":1000,"dir":"give"}]}`, lid, idOf(b))
	if code := do(t, ts, "POST", "/trades", a, oneSided, nil); code != http.StatusBadRequest {
		t.Fatalf("one-sided: %d, want 400", code)
	}
}
