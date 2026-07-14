package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"testing"

	"openmarkets/server/internal/config"
	"openmarkets/server/internal/store"
)

// adminServer spins up a server whose admin/console token is set, returning the test server, the token, and the
// underlying store (so a test can seed a league directly).
func adminServer(t *testing.T) (*testServer, string, store.Store) {
	t.Helper()
	cfg := config.Load()
	cfg.RatePerMin = 0
	cfg.ConsoleToken = "s3cr3t"
	st := store.NewMemory("")
	srv := New(cfg, st, log.New(io.Discard, "", 0))
	ts := newHandlerTestServer(t, srv.Handler())
	return ts, cfg.ConsoleToken, st
}

// adminGet issues a GET with the X-Console-Token header.
func adminReq(t *testing.T, ts *testServer, method, path, token string, out any) int {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Console-Token", token)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		raw, _ := io.ReadAll(resp.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, out)
		}
	}
	return resp.StatusCode
}

func TestAdminRequiresToken(t *testing.T) {
	ts, _, _ := adminServer(t)
	// No token → 401.
	if code := adminReq(t, ts, "GET", "/admin/stats", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no-token stats = %d, want 401", code)
	}
	// Wrong token → 401.
	if code := adminReq(t, ts, "GET", "/admin/stats", "nope", nil); code != http.StatusUnauthorized {
		t.Fatalf("wrong-token stats = %d, want 401", code)
	}
}

// TestAdminGateClosedWhenTokenUnset proves the admin surface is 401 when no console token is configured (admin
// must be explicitly enabled, unlike the local-dev console).
func TestAdminGateClosedWhenTokenUnset(t *testing.T) {
	cfg := config.Load()
	cfg.RatePerMin = 0
	cfg.ConsoleToken = "" // unset
	srv := New(cfg, store.NewMemory(""), log.New(io.Discard, "", 0))
	ts := newHandlerTestServer(t, srv.Handler())
	if code := adminReq(t, ts, "GET", "/admin/stats", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("admin with no configured token = %d, want 401", code)
	}
}

func TestAdminStatsAndLeagueLifecycle(t *testing.T) {
	ts, token, st := adminServer(t)

	// Seed: owner + a second member in one league.
	owner, _, _ := st.CreateAccount()
	other, _, _ := st.CreateAccount()
	lg, err := st.CreateLeague(owner.ID, "L")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.JoinLeague(other.ID, lg.ID); err != nil {
		t.Fatal(err)
	}

	// stats reflects the seed.
	var stats store.Stats
	if code := adminReq(t, ts, "GET", "/admin/stats", token, &stats); code != http.StatusOK {
		t.Fatalf("stats = %d", code)
	}
	if stats.Accounts != 2 || stats.Leagues != 1 || stats.Members != 2 {
		t.Fatalf("stats = %+v", stats)
	}

	// leagues list shows the league with a member count.
	var ll struct {
		Leagues []struct {
			ID          string `json:"id"`
			MemberCount int    `json:"memberCount"`
		} `json:"leagues"`
	}
	if code := adminReq(t, ts, "GET", "/admin/leagues", token, &ll); code != http.StatusOK {
		t.Fatalf("leagues = %d", code)
	}
	if len(ll.Leagues) != 1 || ll.Leagues[0].ID != lg.ID || ll.Leagues[0].MemberCount != 2 {
		t.Fatalf("leagues list = %+v", ll.Leagues)
	}

	// league detail lists members.
	var detail struct {
		ID      string `json:"id"`
		Members []struct {
			AccountID string `json:"accountId"`
		} `json:"members"`
	}
	if code := adminReq(t, ts, "GET", "/admin/leagues/"+lg.ID, token, &detail); code != http.StatusOK {
		t.Fatalf("league detail = %d", code)
	}
	if detail.ID != lg.ID || len(detail.Members) != 2 {
		t.Fatalf("league detail = %+v", detail)
	}

	// kick the second member.
	if code := adminReq(t, ts, "POST", "/admin/leagues/"+lg.ID+"/kick?account="+other.ID, token, nil); code != http.StatusOK {
		t.Fatalf("kick = %d", code)
	}
	if st.IsMember(other.ID, lg.ID) {
		t.Fatal("member still present after kick")
	}
	// kicking a non-member → 404.
	if code := adminReq(t, ts, "POST", "/admin/leagues/"+lg.ID+"/kick?account="+other.ID, token, nil); code != http.StatusNotFound {
		t.Fatalf("re-kick = %d, want 404", code)
	}

	// delete the league.
	if code := adminReq(t, ts, "POST", "/admin/leagues/"+lg.ID+"/delete", token, nil); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	if _, err := st.GetLeague(lg.ID); err != store.ErrNotFound {
		t.Fatalf("league survived delete: %v", err)
	}
	// deleting again → 404.
	if code := adminReq(t, ts, "POST", "/admin/leagues/"+lg.ID+"/delete", token, nil); code != http.StatusNotFound {
		t.Fatalf("re-delete = %d, want 404", code)
	}
}
