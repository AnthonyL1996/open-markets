package api

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"testing"

	"openmarkets/server/internal/config"
	"openmarkets/server/internal/store"
)

// lbServer builds a test server AND returns the underlying Memory store, so a test can both drive the HTTP
// surface and reach into the store to engineer bond/default state (which has no public HTTP setup path). A
// pricer is installed so trades can be accepted (Oil 400 cents/unit).
func lbServer(t *testing.T) (*testServer, *store.Memory) {
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
	return ts, st
}

// leaderboardsDTO mirrors the GET /leaderboards response shape for decoding.
type leaderboardsDTO struct {
	LeagueID string `json:"leagueId"`
	Boards   []struct {
		ID             string `json:"id"`
		Label          string `json:"label"`
		HigherIsBetter bool   `json:"higherIsBetter"`
		Rows           []struct {
			AccountID   string `json:"accountId"`
			DisplayName string `json:"displayName"`
			Value       int64  `json:"value"`
			Rank        int    `json:"rank"`
		} `json:"rows"`
	} `json:"boards"`
	Titles []struct {
		AccountID string   `json:"accountId"`
		Titles    []string `json:"titles"`
	} `json:"titles"`
}

func boardByID(lb leaderboardsDTO, id string) (struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	HigherIsBetter bool   `json:"higherIsBetter"`
	Rows           []struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
		Value       int64  `json:"value"`
		Rank        int    `json:"rank"`
	} `json:"rows"`
}, bool) {
	for _, b := range lb.Boards {
		if b.ID == id {
			return b, true
		}
	}
	return lb.Boards[0], false
}

func hasTitle(lb leaderboardsDTO, accountID, title string) bool {
	for _, e := range lb.Titles {
		if e.AccountID != accountID {
			continue
		}
		for _, tt := range e.Titles {
			if tt == title {
				return true
			}
		}
	}
	return false
}

// makeDefaultedBond engineers a manual loan from creditor→debtor, then drives it to terminal default
// (defaultedReceivable) via consecutive misses. Returns the bond id.
func makeDefaultedBond(t *testing.T, st *store.Memory, leagueID, creditorID, debtorID string, principal int64) string {
	t.Helper()
	offered, err := st.OfferLoan(store.Bond{
		LeagueID: leagueID, CreditorID: creditorID, DebtorID: debtorID,
		PrincipalCents: principal, InterestBps: 1000, Installments: 4, ProposedBy: creditorID,
	})
	if err != nil {
		t.Fatalf("OfferLoan: %v", err)
	}
	if _, _, err := st.AcceptLoan(debtorID, offered.ID); err != nil {
		t.Fatalf("AcceptLoan: %v", err)
	}
	// Two consecutive misses (BondMaxMisses default = 2) → defaultedReceivable.
	for i := 0; i < 2; i++ {
		if _, _, err := st.MissBondInstallment(offered.ID); err != nil {
			t.Fatalf("MissBondInstallment: %v", err)
		}
	}
	b, _ := st.GetBond(offered.ID)
	if b.Status != store.BondDefaultedReceivable {
		t.Fatalf("bond status = %q, want defaultedReceivable", b.Status)
	}
	return offered.ID
}

func TestLeaderboards_OrderingTitlesAndRanks(t *testing.T) {
	ts, st := lbServer(t)
	a := createMember(t, ts) // owner / patron / baron-payer
	b := createMember(t, ts) // investment recipient → top net worth
	c := createMember(t, ts) // defaulter → deadbeat + bankrupt
	aID, bID, cID := idOf(a), idOf(b), idOf(c)

	// League with all three members.
	var lg struct{ LeagueID, JoinCode string }
	if code := do(t, ts, "POST", "/leagues", a, `{"name":"Boards"}`, &lg); code != http.StatusCreated {
		t.Fatalf("create league: %d", code)
	}
	for _, who := range []string{b, c} {
		if code := do(t, ts, "POST", "/leagues/join", who, `{"joinCode":"`+lg.JoinCode+`"}`, nil); code != http.StatusOK {
			t.Fatalf("join: %d", code)
		}
	}

	// Display names (so board rows carry them).
	do(t, ts, "POST", "/accounts/name", a, `{"name":"Alice"}`, nil)
	do(t, ts, "POST", "/accounts/name", b, `{"name":"Bob"}`, nil)

	// City profiles → population / happiness boards.
	do(t, ts, "POST", "/cityprofile", b, `{"population":50000,"happiness":90}`, nil)
	do(t, ts, "POST", "/cityprofile", a, `{"population":10000,"happiness":70}`, nil)

	// Reports → market-mover board (B moves the most absolute net supply).
	do(t, ts, "POST", "/report", b, `{"leagueId":"`+lg.LeagueID+`","commodity":"Oil","netSupply":-30000}`, nil)
	do(t, ts, "POST", "/report", a, `{"leagueId":"`+lg.LeagueID+`","commodity":"Oil","netSupply":5000}`, nil)

	// A invests §50,000 in B → B gains cash (top net worth), A is the patron.
	invBody := fmt.Sprintf(`{"granteeId":%q,"costCents":5000000,"days":7,"demandKind":"com"}`, bID)
	if code := do(t, ts, "POST", "/investment-office?league="+lg.LeagueID, a, invBody, nil); code != http.StatusOK {
		t.Fatalf("invest: %d", code)
	}

	// A↔B complete a trade (1 installment) → both get a completed trade.
	offer := fmt.Sprintf(`{"leagueId":%q,"counterparty":%q,"defaultRateBps":2000,"installments":1,
		"items":[{"kind":"commodity","commodity":"Oil","qtyFixed":100000,"dir":"give"},
		         {"kind":"gold","goldCents":1000,"dir":"take"}]}`, lg.LeagueID, bID)
	var created struct{ ID string }
	if code := do(t, ts, "POST", "/trades", a, offer, &created); code != http.StatusCreated {
		t.Fatalf("offer: %d", code)
	}
	if code := do(t, ts, "POST", "/trades/"+created.ID+"/accept", b, "", nil); code != http.StatusOK {
		t.Fatalf("accept: %d", code)
	}
	// A nets positive → B is the net payer and settles the single installment, completing the trade.
	if code := do(t, ts, "POST", "/trades/"+created.ID+"/settle", b, "", nil); code != http.StatusOK {
		t.Fatalf("settle: %d", code)
	}

	// C terminally defaults on a bond owed to A → C is in austerity (Bankrupt) and accrues misses (Deadbeat).
	makeDefaultedBond(t, st, lg.LeagueID, aID, cID, 200000)

	// A completes a Great Work → tops the Master Builder board.
	gw, err := st.CreateProject(store.Project{
		LeagueID: lg.LeagueID, Name: "Test Monument",
		Reqs:     []store.ProjectReq{{Commodity: "Oil", Qty: 1}},
		BuffKind: store.DemandCommercial, BuffMagnitudeCents: 1_000_000, BuffDays: 3,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, _, err := st.ContributeProjectGoods(lg.LeagueID, aID, gw.ID, "Oil", 1); err != nil {
		t.Fatalf("complete project: %v", err)
	}

	// --- Fetch the boards ---
	var lb leaderboardsDTO
	if code := do(t, ts, "GET", "/leaderboards?league="+lg.LeagueID, a, "", &lb); code != http.StatusOK {
		t.Fatalf("leaderboards: %d", code)
	}
	if lb.LeagueID != lg.LeagueID {
		t.Fatalf("leagueId = %q", lb.LeagueID)
	}

	// Every board lists all 3 members with ranks 1..3.
	for _, brd := range lb.Boards {
		if len(brd.Rows) != 3 {
			t.Fatalf("board %s has %d rows, want 3", brd.ID, len(brd.Rows))
		}
		for i, row := range brd.Rows {
			if row.Rank != i+1 {
				t.Fatalf("board %s row %d rank=%d, want %d", brd.ID, i, row.Rank, i+1)
			}
		}
	}

	// Net worth: B (received the §50k investment) is rank 1; A (paid it, minus trade) is last.
	nw, ok := boardByID(lb, "netWorth")
	if !ok {
		t.Fatal("missing netWorth board")
	}
	if nw.Rows[0].AccountID != bID {
		t.Fatalf("netWorth #1 = %s, want B (%s); rows=%+v", nw.Rows[0].AccountID, bID, nw.Rows)
	}
	if nw.Rows[0].DisplayName != "Bob" {
		t.Fatalf("netWorth #1 displayName = %q, want Bob", nw.Rows[0].DisplayName)
	}

	// Market mover: B (|−30000| = 30000) tops A (5000) tops C (0).
	mm, _ := boardByID(lb, "marketMover")
	if mm.Rows[0].AccountID != bID || mm.Rows[0].Value != 30000 {
		t.Fatalf("marketMover #1 = %s/%d, want B/30000", mm.Rows[0].AccountID, mm.Rows[0].Value)
	}

	// Trade volume: A and B each completed 1, C completed 0.
	tv, _ := boardByID(lb, "tradeVolume")
	if tv.Rows[2].AccountID != cID || tv.Rows[2].Value != 0 {
		t.Fatalf("tradeVolume last = %s/%d, want C/0", tv.Rows[2].AccountID, tv.Rows[2].Value)
	}

	// Patron: A invested §50,000.
	pat, _ := boardByID(lb, "patron")
	if pat.Rows[0].AccountID != aID || pat.Rows[0].Value != 5000000 {
		t.Fatalf("patron #1 = %s/%d, want A/5000000", pat.Rows[0].AccountID, pat.Rows[0].Value)
	}

	// Master Builder: A helped complete one Great Work.
	mb, ok := boardByID(lb, "masterBuilder")
	if !ok {
		t.Fatal("missing masterBuilder board")
	}
	if mb.Rows[0].AccountID != aID || mb.Rows[0].Value != 1 {
		t.Fatalf("masterBuilder #1 = %s/%d, want A/1", mb.Rows[0].AccountID, mb.Rows[0].Value)
	}

	// Deadbeat: C missed installments (MissedCount > 0) and tops the shame board.
	db, _ := boardByID(lb, "deadbeat")
	if db.HigherIsBetter != true {
		t.Fatal("deadbeat board should be higherIsBetter (more misses = top of shame)")
	}
	if db.Rows[0].AccountID != cID || db.Rows[0].Value <= 0 {
		t.Fatalf("deadbeat #1 = %s/%d, want C with >0 misses", db.Rows[0].AccountID, db.Rows[0].Value)
	}

	// Population: B (50000) tops A (10000) tops C (0).
	pop, _ := boardByID(lb, "population")
	if pop.Rows[0].AccountID != bID || pop.Rows[0].Value != 50000 {
		t.Fatalf("population #1 = %s/%d, want B/50000", pop.Rows[0].AccountID, pop.Rows[0].Value)
	}

	// --- Titles ---
	if !hasTitle(lb, bID, "Market Baron") {
		t.Fatalf("B should hold Market Baron; titles=%+v", lb.Titles)
	}
	if !hasTitle(lb, aID, "Patron") {
		t.Fatalf("A should hold Patron; titles=%+v", lb.Titles)
	}
	if !hasTitle(lb, aID, "Master Builder") {
		t.Fatalf("A should hold Master Builder; titles=%+v", lb.Titles)
	}
	if !hasTitle(lb, cID, "Deadbeat") {
		t.Fatalf("C should hold Deadbeat; titles=%+v", lb.Titles)
	}
	if !hasTitle(lb, cID, "Bankrupt") {
		t.Fatalf("C (in austerity) should hold Bankrupt; titles=%+v", lb.Titles)
	}
	if !hasTitle(lb, bID, "Metropolis") {
		t.Fatalf("B (top population) should hold Metropolis; titles=%+v", lb.Titles)
	}

	// Member-only auth: an outsider is 403; a missing league is 400.
	outsider := createMember(t, ts)
	if code := do(t, ts, "GET", "/leaderboards?league="+lg.LeagueID, outsider, "", nil); code != http.StatusForbidden {
		t.Fatalf("outsider = %d, want 403", code)
	}
	if code := do(t, ts, "GET", "/leaderboards", a, "", nil); code != http.StatusBadRequest {
		t.Fatalf("missing league = %d, want 400", code)
	}
}

// TestLeaderboards_Phoenix verifies the comeback board: a member who escaped a defaulted debt (bond cleared via
// garnishment) AND is no longer in austerity earns Phoenix.
func TestLeaderboards_Phoenix(t *testing.T) {
	ts, st := lbServer(t)
	a := createMember(t, ts)
	b := createMember(t, ts)
	aID, bID := idOf(a), idOf(b)
	var lg struct{ LeagueID, JoinCode string }
	do(t, ts, "POST", "/leagues", a, `{"name":"Comeback"}`, &lg)
	do(t, ts, "POST", "/leagues/join", b, `{"joinCode":"`+lg.JoinCode+`"}`, nil)

	// B defaults on a small bond owed to A, then garnishment clears it → B escaped austerity.
	bondID := makeDefaultedBond(t, st, lg.LeagueID, aID, bID, 100000)
	for i := 0; i < 50; i++ { // garnish until cleared (50k/tick floor clears 100k principal+interest quickly)
		b2, _, _, err := st.GarnishBond(bondID)
		if err != nil {
			t.Fatalf("GarnishBond: %v", err)
		}
		if b2.Status == store.BondCleared || b2.Status == store.BondWrittenOff {
			break
		}
	}
	got, _ := st.GetBond(bondID)
	if got.Status != store.BondCleared {
		t.Fatalf("bond status = %q, want cleared", got.Status)
	}
	if aust, _, _ := st.CityState(lg.LeagueID, bID); aust {
		t.Fatal("B should no longer be in austerity after the bond cleared")
	}

	var lb leaderboardsDTO
	if code := do(t, ts, "GET", "/leaderboards?league="+lg.LeagueID, b, "", &lb); code != http.StatusOK {
		t.Fatalf("leaderboards: %d", code)
	}
	ph, _ := boardByID(lb, "phoenix")
	var bVal int64 = -1
	for _, row := range ph.Rows {
		if row.AccountID == bID {
			bVal = row.Value
		}
	}
	if bVal != 1 {
		t.Fatalf("B phoenix value = %d, want 1 (one escaped bond)", bVal)
	}
	if !hasTitle(lb, bID, "Phoenix") {
		t.Fatalf("B should hold Phoenix; titles=%+v", lb.Titles)
	}
	if hasTitle(lb, bID, "Bankrupt") {
		t.Fatalf("B escaped austerity and must NOT be Bankrupt; titles=%+v", lb.Titles)
	}
}

// globalLeaderboardsDTO mirrors GET /global-leaderboards.
type globalLeaderboardsDTO struct {
	Boards []struct {
		ID   string `json:"id"`
		Rows []struct {
			Rank        int    `json:"rank"`
			DisplayName string `json:"displayName"`
			Value       int64  `json:"value"`
			Percentile  int    `json:"percentile"`
			Tier        string `json:"tier"`
			You         bool   `json:"you"`
		} `json:"rows"`
	} `json:"boards"`
}

func TestGlobalLeaderboards_PercentileTierAndCallerRow(t *testing.T) {
	ts, _ := lbServer(t)
	a := createMember(t, ts)
	b := createMember(t, ts)
	bID := idOf(b)

	// A league where A invests in B → B has the higher global net worth.
	var lg struct{ LeagueID, JoinCode string }
	do(t, ts, "POST", "/leagues", a, `{"name":"G"}`, &lg)
	do(t, ts, "POST", "/leagues/join", b, `{"joinCode":"`+lg.JoinCode+`"}`, nil)
	invBody := fmt.Sprintf(`{"granteeId":%q,"costCents":5000000,"days":7,"demandKind":"com"}`, bID)
	if code := do(t, ts, "POST", "/investment-office?league="+lg.LeagueID, a, invBody, nil); code != http.StatusOK {
		t.Fatalf("invest: %d", code)
	}

	// A queries the global boards — no league param, any authenticated account.
	var gl globalLeaderboardsDTO
	if code := do(t, ts, "GET", "/global-leaderboards", a, "", &gl); code != http.StatusOK {
		t.Fatalf("global leaderboards: %d", code)
	}

	var nw *struct {
		ID   string `json:"id"`
		Rows []struct {
			Rank        int    `json:"rank"`
			DisplayName string `json:"displayName"`
			Value       int64  `json:"value"`
			Percentile  int    `json:"percentile"`
			Tier        string `json:"tier"`
			You         bool   `json:"you"`
		} `json:"rows"`
	}
	for i := range gl.Boards {
		if gl.Boards[i].ID == "globalNetWorth" {
			nw = &gl.Boards[i]
		}
	}
	if nw == nil {
		t.Fatal("missing globalNetWorth board")
	}
	// Rank 1 should be the top percentile with a Diamond/Platinum-ish tier; ranks present and percentile bounded.
	if nw.Rows[0].Rank != 1 || nw.Rows[0].Percentile != 100 {
		t.Fatalf("rank-1 percentile = %d, want 100 (rows=%+v)", nw.Rows[0].Percentile, nw.Rows)
	}
	if nw.Rows[0].Tier == "" {
		t.Fatal("rank-1 tier should be set")
	}
	// The caller (A) must appear with you=true somewhere in the board.
	var sawYou bool
	for _, row := range nw.Rows {
		if row.You {
			sawYou = true
		}
		if row.Percentile < 0 || row.Percentile > 100 {
			t.Fatalf("percentile out of range: %d", row.Percentile)
		}
	}
	if !sawYou {
		t.Fatalf("caller's own row (you=true) missing: %+v", nw.Rows)
	}

	// Privacy: other players are anonymized — B set no name, so it shows as "Mayor-<id4>", never the raw id.
	for _, row := range nw.Rows {
		if row.DisplayName == bID {
			t.Fatalf("raw account id leaked into global board: %q", row.DisplayName)
		}
	}

	// Auth required.
	if code := do(t, ts, "GET", "/global-leaderboards", "", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("unauth global = %d, want 401", code)
	}
}

// TestLeaderboards_SuspectExcludedFromClientBoards verifies a member whose latest city profile is flagged Suspect
// (implausible population jump) is treated as value 0 on the client-reported population/happiness boards and is NOT
// awarded the Metropolis title — while a legit member with a real (lower) population wins both.
func TestLeaderboards_SuspectExcludedFromClientBoards(t *testing.T) {
	ts, _ := lbServer(t)
	a := createMember(t, ts) // legit, modest population
	b := createMember(t, ts) // spoofer: absurd one-post jump → Suspect
	aID, bID := idOf(a), idOf(b)

	var lg struct{ LeagueID, JoinCode string }
	if code := do(t, ts, "POST", "/leagues", a, `{"name":"Sus"}`, &lg); code != http.StatusCreated {
		t.Fatalf("create league: %d", code)
	}
	if code := do(t, ts, "POST", "/leagues/join", b, `{"joinCode":"`+lg.JoinCode+`"}`, nil); code != http.StatusOK {
		t.Fatalf("join: %d", code)
	}

	// A: a single, plausible profile (never delta-flagged).
	do(t, ts, "POST", "/cityprofile", a, `{"population":40000,"happiness":80}`, nil)
	// B: a small baseline, then an absurd jump → the latest snapshot is Suspect.
	do(t, ts, "POST", "/cityprofile", b, `{"population":10000,"happiness":99}`, nil)
	do(t, ts, "POST", "/cityprofile", b, `{"population":9000000,"happiness":99}`, nil)

	var lb leaderboardsDTO
	if code := do(t, ts, "GET", "/leaderboards?league="+lg.LeagueID, a, "", &lb); code != http.StatusOK {
		t.Fatalf("leaderboards: %d", code)
	}

	pop, _ := boardByID(lb, "population")
	// B is suspect → value 0; A (40000) tops the board and gets Metropolis.
	if pop.Rows[0].AccountID != aID || pop.Rows[0].Value != 40000 {
		t.Fatalf("population #1 = %s/%d, want %s/40000", pop.Rows[0].AccountID, pop.Rows[0].Value, aID)
	}
	for _, row := range pop.Rows {
		if row.AccountID == bID && row.Value != 0 {
			t.Fatalf("suspect B population = %d, want 0", row.Value)
		}
	}
	if !hasTitle(lb, aID, "Metropolis") {
		t.Fatalf("A should hold Metropolis; titles=%+v", lb.Titles)
	}
	if hasTitle(lb, bID, "Metropolis") {
		t.Fatal("suspect B must not hold Metropolis")
	}
}
