package api

import (
	"net/http"
	"testing"
)

// cityHistoryDTO mirrors the GET /cityprofile/history response.
type cityHistoryDTO struct {
	AccountID string `json:"accountId"`
	Snapshots []struct {
		Population  int `json:"population"`
		Reliability int `json:"reliability"`
	} `json:"snapshots"`
	NetSeries []struct {
		TS    string `json:"ts"`
		Cents int64  `json:"cents"`
	} `json:"netSeries"`
}

// TestCityHistory_MemberSeesSnapshotsAndNetSeries verifies a leaguemate gets the target's retained snapshots and
// cumulative net-§ curve, and that a non-member is 403 while missing params are 400.
func TestCityHistory_MemberSeesSnapshotsAndNetSeries(t *testing.T) {
	ts, _ := lbServer(t)
	a := createMember(t, ts)
	b := createMember(t, ts)
	bID := idOf(b)
	leagueID := leagueAB(t, ts, a, b)

	// B reports a couple of city snapshots (history accumulates).
	do(t, ts, "POST", "/cityprofile", b, `{"population":10000}`, nil)
	do(t, ts, "POST", "/cityprofile", b, `{"population":12000}`, nil)

	// A↔B settle a one-installment trade so B has a settlement event (→ a net-series point).
	offer := `{"leagueId":"` + leagueID + `","counterparty":"` + bID + `","defaultRateBps":2000,"installments":1,
		"items":[{"kind":"commodity","commodity":"Oil","qtyFixed":100000,"dir":"give"},
		         {"kind":"gold","goldCents":1000,"dir":"take"}]}`
	var created struct{ ID string }
	if code := do(t, ts, "POST", "/trades", a, offer, &created); code != http.StatusCreated {
		t.Fatalf("offer: %d", code)
	}
	if code := do(t, ts, "POST", "/trades/"+created.ID+"/accept", b, "", nil); code != http.StatusOK {
		t.Fatalf("accept: %d", code)
	}
	if code := do(t, ts, "POST", "/trades/"+created.ID+"/settle", b, "", nil); code != http.StatusOK {
		t.Fatalf("settle: %d", code)
	}

	// A (member) fetches B's history.
	var hist cityHistoryDTO
	if code := do(t, ts, "GET", "/cityprofile/history?account="+bID+"&league="+leagueID, a, "", &hist); code != http.StatusOK {
		t.Fatalf("history: %d", code)
	}
	if hist.AccountID != bID {
		t.Fatalf("accountId = %q, want %q", hist.AccountID, bID)
	}
	if len(hist.Snapshots) != 2 {
		t.Fatalf("snapshots len = %d, want 2", len(hist.Snapshots))
	}
	// Oldest→newest.
	if hist.Snapshots[0].Population != 10000 || hist.Snapshots[1].Population != 12000 {
		t.Fatalf("snapshot populations = %d,%d, want 10000,12000", hist.Snapshots[0].Population, hist.Snapshots[1].Population)
	}
	if hist.Snapshots[0].Reliability != 100 {
		t.Fatalf("reliability = %d, want 100", hist.Snapshots[0].Reliability)
	}
	if len(hist.NetSeries) == 0 {
		t.Fatal("netSeries empty, want at least one settlement point")
	}

	// Non-member is 403.
	outsider := createMember(t, ts)
	if code := do(t, ts, "GET", "/cityprofile/history?account="+bID+"&league="+leagueID, outsider, "", nil); code != http.StatusForbidden {
		t.Fatalf("outsider = %d, want 403", code)
	}
	// Missing params → 400.
	if code := do(t, ts, "GET", "/cityprofile/history?account="+bID, a, "", nil); code != http.StatusBadRequest {
		t.Fatalf("missing league = %d, want 400", code)
	}
}
