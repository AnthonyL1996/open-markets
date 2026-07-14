package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openmarkets/server/internal/config"
	"openmarkets/server/internal/store"
)

type testServer struct {
	URL     string
	handler http.Handler
	client  *http.Client
}

type localRoundTripper struct {
	handler http.Handler
}

func newHandlerTestServer(t *testing.T, h http.Handler) *testServer {
	t.Helper()
	ts := &testServer{URL: "http://openmarkets.test", handler: h}
	ts.client = &http.Client{Transport: localRoundTripper{handler: h}}
	t.Cleanup(ts.Close)
	return ts
}

func (ts *testServer) Client() *http.Client { return ts.client }
func (ts *testServer) Close()               {}

func (rt localRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.RequestURI = ""
	r.RemoteAddr = "127.0.0.1:12345"
	if r.Body == nil {
		r.Body = http.NoBody
	}
	rr := httptest.NewRecorder()
	rt.handler.ServeHTTP(rr, r)
	resp := rr.Result()
	resp.Request = req
	return resp, nil
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	cfg := config.Load()
	cfg.RatePerMin = 0 // disable limiting in tests
	cfg.Version = "test"
	srv := New(cfg, store.NewMemory(""), log.New(io.Discard, "", 0))
	return newHandlerTestServer(t, srv.Handler())
}

// do issues a request and decodes a JSON body into out (if non-nil). bearer "" sends no auth.
func do(t *testing.T, ts *testServer, method, path, bearer, body string, out any) int {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		raw, _ := io.ReadAll(resp.Body)
		if len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatalf("decode %s %s: %v (body=%s)", method, path, err, raw)
			}
		}
	}
	return resp.StatusCode
}

// createMember spins up an account and returns its bearer token "<id>.<secret>".
func createMember(t *testing.T, ts *testServer) string {
	t.Helper()
	var acc struct{ AccountID, Secret string }
	if code := do(t, ts, "POST", "/accounts", "", "", &acc); code != http.StatusCreated {
		t.Fatalf("create account: %d", code)
	}
	return acc.AccountID + "." + acc.Secret
}

func TestConsoleServedWhenEnabled(t *testing.T) {
	ts := newTestServer(t) // newTestServer uses config.Load() defaults → Console on
	resp, err := ts.Client().Get(ts.URL + "/console")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/console = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("/console content-type = %q, want text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("operator console")) {
		t.Fatal("/console body missing expected marker")
	}
}

func TestConsoleDisabled(t *testing.T) {
	cfg := config.Load()
	cfg.Console = false
	cfg.RatePerMin = 0
	srv := New(cfg, store.NewMemory(""), log.New(io.Discard, "", 0))
	ts := newHandlerTestServer(t, srv.Handler())
	resp, err := ts.Client().Get(ts.URL + "/console")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/console disabled = %d, want 404", resp.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	ts := newTestServer(t)
	var body struct {
		OK bool `json:"ok"`
	}
	if code := do(t, ts, "GET", "/healthz", "", "", &body); code != http.StatusOK {
		t.Fatalf("healthz code %d", code)
	}
	if !body.OK {
		t.Fatalf("healthz body %+v, want ok=true", body)
	}
}

// The account-creation endpoint has its own stricter per-IP limiter. With a low cap it returns 429 once exceeded.
func TestAccountCreationRateLimited(t *testing.T) {
	cfg := config.Load()
	cfg.RatePerMin = 0 // general limiter off so only the dedicated account limiter is exercised
	cfg.AcctPerHour = 3
	srv := New(cfg, store.NewMemory(""), log.New(io.Discard, "", 0))
	ts := newHandlerTestServer(t, srv.Handler())

	for i := 0; i < 3; i++ {
		if code := do(t, ts, "POST", "/accounts", "", "", nil); code != http.StatusCreated {
			t.Fatalf("account %d = %d, want 201", i, code)
		}
	}
	if code := do(t, ts, "POST", "/accounts", "", "", nil); code != http.StatusTooManyRequests {
		t.Fatalf("4th account = %d, want 429", code)
	}
}

// AcctPerHour=0 disables the account limiter (local-testing path) — many creations all succeed.
func TestAccountCreationUnlimitedWhenZero(t *testing.T) {
	cfg := config.Load()
	cfg.RatePerMin = 0
	cfg.AcctPerHour = 0
	srv := New(cfg, store.NewMemory(""), log.New(io.Discard, "", 0))
	ts := newHandlerTestServer(t, srv.Handler())
	for i := 0; i < 25; i++ {
		if code := do(t, ts, "POST", "/accounts", "", "", nil); code != http.StatusCreated {
			t.Fatalf("account %d = %d, want 201 (limiter disabled)", i, code)
		}
	}
}

// With OM_CONSOLE_TOKEN set, the console requires a matching token; without the token it 401s, with it (query or
// header) it 200s. No token configured keeps the dev console open (covered by TestConsoleServedWhenEnabled).
func TestConsoleTokenGate(t *testing.T) {
	cfg := config.Load()
	cfg.RatePerMin = 0
	cfg.ConsoleToken = "s3cr3t"
	srv := New(cfg, store.NewMemory(""), log.New(io.Discard, "", 0))
	ts := newHandlerTestServer(t, srv.Handler())

	// No token → 401.
	resp, err := ts.Client().Get(ts.URL + "/console")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("console no-token = %d, want 401", resp.StatusCode)
	}

	// Correct token via query param → 200.
	resp, err = ts.Client().Get(ts.URL + "/console?token=s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("console query-token = %d, want 200", resp.StatusCode)
	}

	// Correct token via header → 200.
	req, _ := http.NewRequest("GET", ts.URL+"/console", nil)
	req.Header.Set("X-Console-Token", "s3cr3t")
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("console header-token = %d, want 200", resp.StatusCode)
	}

	// Wrong token → 401.
	resp, err = ts.Client().Get(ts.URL + "/console?token=nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("console wrong-token = %d, want 401", resp.StatusCode)
	}
}

func TestHealth(t *testing.T) {
	ts := newTestServer(t)
	var body struct{ Status, Version string }
	if code := do(t, ts, "GET", "/health", "", "", &body); code != http.StatusOK {
		t.Fatalf("health code %d", code)
	}
	if body.Status != "ok" || body.Version != "test" {
		t.Fatalf("health body %+v", body)
	}
}

func TestFullFlow_IndexReflectsReports(t *testing.T) {
	ts := newTestServer(t)
	owner := createMember(t, ts)

	// Owner creates a league.
	var lg struct{ LeagueID, JoinCode string }
	if code := do(t, ts, "POST", "/leagues", owner, `{"name":"Friends"}`, &lg); code != http.StatusCreated {
		t.Fatalf("create league: %d", code)
	}

	// A friend joins by code.
	friend := createMember(t, ts)
	if code := do(t, ts, "POST", "/leagues/join", friend, `{"joinCode":"`+lg.JoinCode+`"}`, nil); code != http.StatusOK {
		t.Fatalf("join: %d", code)
	}

	// Both report on Oil: owner exports 10000, friend exports 10000 → sum 20000 → index 0.0 clamped to 0.5.
	for _, who := range []string{owner, friend} {
		if code := do(t, ts, "POST", "/report", who, `{"leagueId":"`+lg.LeagueID+`","commodity":"Oil","netSupply":10000}`, nil); code != http.StatusNoContent {
			t.Fatalf("report: %d", code)
		}
	}

	// Legacy single-float feed (the shipping client contract).
	var feed struct {
		Version string  `json:"version"`
		Index   float64 `json:"index"`
		TS      int64   `json:"ts"`
	}
	if code := do(t, ts, "GET", "/index?league="+lg.LeagueID, owner, "", &feed); code != http.StatusOK {
		t.Fatalf("index: %d", code)
	}
	if feed.Index != 0.5 || feed.TS == 0 || feed.Version != "test" {
		t.Fatalf("feed %+v, want index 0.5", feed)
	}

	// Richer per-commodity feed — array form (so net35 JsonUtility can parse it).
	var prices struct {
		Commodities []struct {
			Commodity string  `json:"commodity"`
			Index     float64 `json:"index"`
		} `json:"commodities"`
	}
	if code := do(t, ts, "GET", "/prices?league="+lg.LeagueID, owner, "", &prices); code != http.StatusOK {
		t.Fatalf("prices: %d", code)
	}
	if len(prices.Commodities) != 1 || prices.Commodities[0].Commodity != "Oil" || prices.Commodities[0].Index != 0.5 {
		t.Fatalf("prices %+v, want [{Oil 0.5}]", prices.Commodities)
	}
}

func TestLeagueMembers(t *testing.T) {
	ts := newTestServer(t)
	owner := createMember(t, ts)
	var lg struct{ LeagueID, JoinCode string }
	if code := do(t, ts, "POST", "/leagues", owner, `{"name":"Roster"}`, &lg); code != http.StatusCreated {
		t.Fatalf("create league: %d", code)
	}
	friend := createMember(t, ts)
	if code := do(t, ts, "POST", "/leagues/join", friend, `{"joinCode":"`+lg.JoinCode+`"}`, nil); code != http.StatusOK {
		t.Fatalf("join: %d", code)
	}

	var roster struct {
		OwnerID string `json:"ownerId"`
		Members []struct {
			AccountID string `json:"accountId"`
			IsOwner   bool   `json:"isOwner"`
		} `json:"members"`
	}
	if code := do(t, ts, "GET", "/leagues/members?league="+lg.LeagueID, friend, "", &roster); code != http.StatusOK {
		t.Fatalf("members: %d", code)
	}
	if len(roster.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(roster.Members))
	}
	// Owner is flagged and sorted first.
	if !roster.Members[0].IsOwner || roster.Members[0].AccountID != roster.OwnerID {
		t.Fatalf("owner not first/flagged: %+v", roster.Members)
	}
	if roster.Members[1].IsOwner {
		t.Fatalf("non-owner flagged as owner: %+v", roster.Members)
	}

	// An outsider can't read the roster.
	outsider := createMember(t, ts)
	if code := do(t, ts, "GET", "/leagues/members?league="+lg.LeagueID, outsider, "", nil); code != http.StatusForbidden {
		t.Fatalf("outsider roster = %d, want 403", code)
	}
	// Missing league param → 400.
	if code := do(t, ts, "GET", "/leagues/members", owner, "", nil); code != http.StatusBadRequest {
		t.Fatalf("missing league = %d, want 400", code)
	}
}

// A member's posted city profile is flattened into the /leagues/members rows so leaguemates can see it, and the
// poster shows online + a last-seen timestamp. The body's identity is ignored (server uses the credential).
func TestCityProfileVisibleToLeaguemates(t *testing.T) {
	ts := newTestServer(t)
	owner := createMember(t, ts)
	var lg struct{ LeagueID, JoinCode string }
	if code := do(t, ts, "POST", "/leagues", owner, `{"name":"Roster"}`, &lg); code != http.StatusCreated {
		t.Fatalf("create league: %d", code)
	}
	friend := createMember(t, ts)
	if code := do(t, ts, "POST", "/leagues/join", friend, `{"joinCode":"`+lg.JoinCode+`"}`, nil); code != http.StatusOK {
		t.Fatalf("join: %d", code)
	}

	// The friend reports a city profile (note the spoofed accountId, which must be ignored).
	profile := `{"accountId":"SPOOF","cityName":"Springfield","population":12345,"happiness":78,"cashCents":500000,"indBuildings":9,"farmWorkers":40}`
	if code := do(t, ts, "POST", "/cityprofile", friend, profile, nil); code != http.StatusOK {
		t.Fatalf("post cityprofile: %d", code)
	}

	var roster struct {
		Members []struct {
			AccountID   string `json:"accountId"`
			Online      bool   `json:"online"`
			LastSeenSec int64  `json:"lastSeenSec"`
			CityName    string `json:"cityName"`
			Population  int    `json:"population"`
			Happiness   int    `json:"happiness"`
			CashCents   int64  `json:"cashCents"`
		} `json:"members"`
	}
	if code := do(t, ts, "GET", "/leagues/members?league="+lg.LeagueID, owner, "", &roster); code != http.StatusOK {
		t.Fatalf("members: %d", code)
	}
	friendID := strings.SplitN(friend, ".", 2)[0]
	var got *struct {
		AccountID   string `json:"accountId"`
		Online      bool   `json:"online"`
		LastSeenSec int64  `json:"lastSeenSec"`
		CityName    string `json:"cityName"`
		Population  int    `json:"population"`
		Happiness   int    `json:"happiness"`
		CashCents   int64  `json:"cashCents"`
	}
	for i := range roster.Members {
		if roster.Members[i].AccountID == friendID {
			got = &roster.Members[i]
		}
	}
	if got == nil {
		t.Fatalf("friend not in roster: %+v", roster.Members)
	}
	if got.CityName != "Springfield" || got.Population != 12345 || got.Happiness != 78 || got.CashCents != 500000 {
		t.Fatalf("profile not flattened into member row: %+v", got)
	}
	if !got.Online || got.LastSeenSec == 0 {
		t.Fatalf("poster should be online with a last-seen time: online=%v lastSeen=%d", got.Online, got.LastSeenSec)
	}
	// The spoofed accountId in the body must not have created a second profile under "SPOOF".
	if got.AccountID == "SPOOF" {
		t.Fatal("server trusted the body's accountId")
	}
}

// An investment can target a specific demand channel; a valid kind is stored on the effect, an invalid one is rejected.
func TestInvestmentDemandKind(t *testing.T) {
	ts := newTestServer(t)
	owner := createMember(t, ts)
	var lg struct{ LeagueID, JoinCode string }
	if code := do(t, ts, "POST", "/leagues", owner, `{"name":"Co"}`, &lg); code != http.StatusCreated {
		t.Fatalf("create league: %d", code)
	}
	friend := createMember(t, ts)
	if code := do(t, ts, "POST", "/leagues/join", friend, `{"joinCode":"`+lg.JoinCode+`"}`, nil); code != http.StatusOK {
		t.Fatalf("join: %d", code)
	}
	friendID := strings.SplitN(friend, ".", 2)[0]
	url := "/investment-office?league=" + lg.LeagueID

	// A valid targeted kind → 200, and the returned effect carries the chosen channel.
	var res struct {
		Effect struct {
			DemandKind string `json:"demandKind"`
		} `json:"effect"`
	}
	// Include the redundant "league" field in the BODY too — the real client/console send it alongside the ?league=
	// query. The strict (DisallowUnknownFields) decoder must accept it (regression: this 400'd "invalid body").
	body := `{"league":"` + lg.LeagueID + `","granteeId":"` + friendID + `","costCents":5000000,"days":7,"demandKind":"com"}`
	if code := do(t, ts, "POST", url, owner, body, &res); code != http.StatusOK {
		t.Fatalf("invest com: %d", code)
	}
	if res.Effect.DemandKind != "com" {
		t.Fatalf("effect demandKind = %q, want com", res.Effect.DemandKind)
	}

	// An invalid demand kind is rejected (before the cooldown check, so it's a clean 400).
	bad := `{"granteeId":"` + friendID + `","costCents":1000000,"days":3,"demandKind":"bogus"}`
	if code := do(t, ts, "POST", url, owner, bad, nil); code != http.StatusBadRequest {
		t.Fatalf("bogus kind = %d, want 400", code)
	}
}

// Transparency: after an investment, the issuer sees it under /citystate "investmentsMade", and /investments shows it
// league-wide as active + in the durable history (which survives expiry).
func TestInvestmentTransparency(t *testing.T) {
	ts := newTestServer(t)
	owner := createMember(t, ts)
	var lg struct{ LeagueID, JoinCode string }
	if code := do(t, ts, "POST", "/leagues", owner, `{"name":"T"}`, &lg); code != http.StatusCreated {
		t.Fatalf("league: %d", code)
	}
	friend := createMember(t, ts)
	if code := do(t, ts, "POST", "/leagues/join", friend, `{"joinCode":"`+lg.JoinCode+`"}`, nil); code != http.StatusOK {
		t.Fatalf("join: %d", code)
	}
	ownerID := strings.SplitN(owner, ".", 2)[0]
	friendID := strings.SplitN(friend, ".", 2)[0]
	body := `{"granteeId":"` + friendID + `","costCents":5000000,"days":7,"demandKind":"com"}`
	if code := do(t, ts, "POST", "/investment-office?league="+lg.LeagueID, owner, body, nil); code != http.StatusOK {
		t.Fatalf("invest: %d", code)
	}

	// The issuer's /citystate lists the investment it MADE.
	var cs struct {
		InvestmentsMade []struct {
			GranteeID string `json:"granteeId"`
		} `json:"investmentsMade"`
	}
	if code := do(t, ts, "GET", "/citystate?league="+lg.LeagueID, owner, "", &cs); code != http.StatusOK {
		t.Fatalf("citystate: %d", code)
	}
	if len(cs.InvestmentsMade) != 1 || cs.InvestmentsMade[0].GranteeID != friendID {
		t.Fatalf("issuer should see 1 investment made to the friend: %+v", cs.InvestmentsMade)
	}

	// /investments shows it league-wide as active + in the history. Any member can read it.
	var inv struct {
		Active []struct {
			IssuerID  string `json:"issuerId"`
			GranteeID string `json:"granteeId"`
		} `json:"active"`
		History []struct {
			PayerID    string `json:"payerId"`
			ReceiverID string `json:"receiverId"`
			Cents      int64  `json:"cents"`
		} `json:"history"`
	}
	if code := do(t, ts, "GET", "/investments?league="+lg.LeagueID, friend, "", &inv); code != http.StatusOK {
		t.Fatalf("investments: %d", code)
	}
	if len(inv.Active) != 1 || inv.Active[0].IssuerID != ownerID || inv.Active[0].GranteeID != friendID {
		t.Fatalf("league-wide active wrong: %+v", inv.Active)
	}
	if len(inv.History) != 1 || inv.History[0].PayerID != ownerID || inv.History[0].ReceiverID != friendID || inv.History[0].Cents != 5000000 {
		t.Fatalf("history wrong: %+v", inv.History)
	}
}

func TestListMyLeagues(t *testing.T) {
	ts := newTestServer(t)
	owner := createMember(t, ts)

	// A fresh account belongs to no leagues.
	var mine struct {
		Leagues []struct {
			LeagueID string `json:"leagueId"`
			Name     string `json:"name"`
			JoinCode string `json:"joinCode"`
			IsOwner  bool   `json:"isOwner"`
		} `json:"leagues"`
	}
	if code := do(t, ts, "GET", "/leagues", owner, "", &mine); code != http.StatusOK {
		t.Fatalf("list leagues: %d", code)
	}
	if len(mine.Leagues) != 0 {
		t.Fatalf("new account leagues = %d, want 0", len(mine.Leagues))
	}

	// Create two leagues as owner, and join a third created by a friend.
	var a, b struct{ LeagueID, JoinCode string }
	if code := do(t, ts, "POST", "/leagues", owner, `{"name":"Alpha"}`, &a); code != http.StatusCreated {
		t.Fatalf("create Alpha: %d", code)
	}
	if code := do(t, ts, "POST", "/leagues", owner, `{"name":"Beta"}`, &b); code != http.StatusCreated {
		t.Fatalf("create Beta: %d", code)
	}
	friend := createMember(t, ts)
	var c struct{ LeagueID, JoinCode string }
	if code := do(t, ts, "POST", "/leagues", friend, `{"name":"Gamma"}`, &c); code != http.StatusCreated {
		t.Fatalf("create Gamma: %d", code)
	}
	if code := do(t, ts, "POST", "/leagues/join", owner, `{"joinCode":"`+c.JoinCode+`"}`, nil); code != http.StatusOK {
		t.Fatalf("owner joins Gamma: %d", code)
	}

	// Owner now sees all three; owned leagues carry a join code, the joined one does not.
	if code := do(t, ts, "GET", "/leagues", owner, "", &mine); code != http.StatusOK {
		t.Fatalf("list leagues 2: %d", code)
	}
	if len(mine.Leagues) != 3 {
		t.Fatalf("owner leagues = %d, want 3", len(mine.Leagues))
	}
	owned, joinedWithoutCode := 0, 0
	for _, lg := range mine.Leagues {
		if lg.IsOwner {
			owned++
			if lg.JoinCode == "" {
				t.Fatalf("owned league %q has no join code", lg.Name)
			}
		} else if lg.JoinCode == "" {
			joinedWithoutCode++
		}
	}
	if owned != 2 || joinedWithoutCode != 1 {
		t.Fatalf("owned=%d joinedWithoutCode=%d, want 2 and 1", owned, joinedWithoutCode)
	}

	// Auth is required.
	if code := do(t, ts, "GET", "/leagues", "", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("unauth list = %d, want 401", code)
	}
}

func TestSetDisplayName(t *testing.T) {
	ts := newTestServer(t)
	owner := createMember(t, ts)
	var lg struct{ LeagueID, JoinCode string }
	if code := do(t, ts, "POST", "/leagues", owner, `{"name":"Names"}`, &lg); code != http.StatusCreated {
		t.Fatalf("create league: %d", code)
	}
	friend := createMember(t, ts)
	if code := do(t, ts, "POST", "/leagues/join", friend, `{"joinCode":"`+lg.JoinCode+`"}`, nil); code != http.StatusOK {
		t.Fatalf("join: %d", code)
	}

	// Auth is required.
	if code := do(t, ts, "POST", "/accounts/name", "", `{"name":"Nobody"}`, nil); code != http.StatusUnauthorized {
		t.Fatalf("unauth set-name = %d, want 401", code)
	}

	// Owner names themselves; surrounding space is trimmed and the embedded newline (a control char, sent as
	// the JSON escape \n) is stripped — so "  A\nB  " becomes "AB".
	var named struct{ AccountID, DisplayName string }
	if code := do(t, ts, "POST", "/accounts/name", owner, `{"name":"  A\nB  "}`, &named); code != http.StatusOK {
		t.Fatalf("set-name: %d", code)
	}
	if named.DisplayName != "AB" {
		t.Fatalf("sanitized name = %q, want %q", named.DisplayName, "AB")
	}

	// An over-long name is truncated to maxDisplayName (24) runes, not rejected.
	long := strings.Repeat("x", 30)
	if code := do(t, ts, "POST", "/accounts/name", owner, `{"name":"`+long+`"}`, &named); code != http.StatusOK {
		t.Fatalf("set long name: %d", code)
	}
	if want := strings.Repeat("x", 24); named.DisplayName != want {
		t.Fatalf("capped name = %q (len %d), want len 24", named.DisplayName, len(named.DisplayName))
	}

	// Reset to a stable name, then assert the roster carries it for the owner and leaves the friend blank.
	if code := do(t, ts, "POST", "/accounts/name", owner, `{"name":"AB"}`, &named); code != http.StatusOK {
		t.Fatalf("reset name: %d", code)
	}
	var roster struct {
		Members []struct {
			AccountID   string `json:"accountId"`
			DisplayName string `json:"displayName"`
			IsOwner     bool   `json:"isOwner"`
		} `json:"members"`
	}
	if code := do(t, ts, "GET", "/leagues/members?league="+lg.LeagueID, friend, "", &roster); code != http.StatusOK {
		t.Fatalf("members: %d", code)
	}
	for _, m := range roster.Members {
		if m.IsOwner && m.DisplayName != "AB" {
			t.Fatalf("owner display name = %q, want %q", m.DisplayName, "AB")
		}
		if !m.IsOwner && m.DisplayName != "" {
			t.Fatalf("unnamed friend has name %q, want empty", m.DisplayName)
		}
	}

	// Clearing the name (empty string) is allowed.
	if code := do(t, ts, "POST", "/accounts/name", owner, `{"name":""}`, &named); code != http.StatusOK {
		t.Fatalf("clear name: %d", code)
	}
	if named.DisplayName != "" {
		t.Fatalf("cleared name = %q, want empty", named.DisplayName)
	}
}

func TestReportBatch(t *testing.T) {
	ts := newTestServer(t)
	owner := createMember(t, ts)
	var lg struct{ LeagueID string }
	do(t, ts, "POST", "/leagues", owner, `{"name":"Batch"}`, &lg)

	body := `{"leagueId":"` + lg.LeagueID + `","reports":[` +
		`{"commodity":"Oil","netSupply":10000},{"commodity":"Grain","netSupply":-20000},{"commodity":"","netSupply":5}]}`
	if code := do(t, ts, "POST", "/report/batch", owner, body, nil); code != http.StatusNoContent {
		t.Fatalf("batch report = %d, want 204", code)
	}

	var prices struct {
		Commodities []struct {
			Commodity string  `json:"commodity"`
			Index     float64 `json:"index"`
		} `json:"commodities"`
	}
	do(t, ts, "GET", "/prices?league="+lg.LeagueID, owner, "", &prices)
	// Oil supplied (10000 → 0.5), Grain demanded (-20000 → 2.0); blank commodity skipped.
	got := map[string]float64{}
	for _, c := range prices.Commodities {
		got[c.Commodity] = c.Index
	}
	if got["Oil"] != 0.5 || got["Grain"] != 2.0 || len(prices.Commodities) != 2 {
		t.Fatalf("batch prices = %+v, want Oil 0.5 / Grain 2.0, 2 entries", prices.Commodities)
	}
}

func TestAuthRequired(t *testing.T) {
	ts := newTestServer(t)
	if code := do(t, ts, "POST", "/leagues", "", `{"name":"x"}`, nil); code != http.StatusUnauthorized {
		t.Fatalf("no-auth league create = %d, want 401", code)
	}
	if code := do(t, ts, "POST", "/leagues", "bogus.creds", `{"name":"x"}`, nil); code != http.StatusUnauthorized {
		t.Fatalf("bad-auth league create = %d, want 401", code)
	}
}

func TestNonMemberCannotRead(t *testing.T) {
	ts := newTestServer(t)
	owner := createMember(t, ts)
	var lg struct{ LeagueID string }
	do(t, ts, "POST", "/leagues", owner, `{"name":"Private"}`, &lg)

	outsider := createMember(t, ts)
	if code := do(t, ts, "GET", "/index?league="+lg.LeagueID, outsider, "", nil); code != http.StatusForbidden {
		t.Fatalf("outsider read = %d, want 403", code)
	}
}

func TestReportByNonMemberForbidden(t *testing.T) {
	ts := newTestServer(t)
	owner := createMember(t, ts)
	var lg struct{ LeagueID string }
	do(t, ts, "POST", "/leagues", owner, `{"name":"L"}`, &lg)

	outsider := createMember(t, ts)
	if code := do(t, ts, "POST", "/report", outsider, `{"leagueId":"`+lg.LeagueID+`","commodity":"Oil","netSupply":1}`, nil); code != http.StatusForbidden {
		t.Fatalf("outsider report = %d, want 403", code)
	}
}

func TestIndexAuthViaQueryParams(t *testing.T) {
	// A bare UnityWebRequest.Get can authenticate via ?account=&secret= instead of a header.
	ts := newTestServer(t)
	var acc struct{ AccountID, Secret string }
	do(t, ts, "POST", "/accounts", "", "", &acc)
	var lg struct{ LeagueID string }
	do(t, ts, "POST", "/leagues", acc.AccountID+"."+acc.Secret, `{"name":"Q"}`, &lg)

	url := "/index?league=" + lg.LeagueID + "&account=" + acc.AccountID + "&secret=" + acc.Secret
	if code := do(t, ts, "GET", url, "", "", nil); code != http.StatusOK {
		t.Fatalf("query-param auth = %d, want 200", code)
	}
}
