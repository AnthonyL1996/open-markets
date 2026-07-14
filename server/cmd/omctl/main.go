// Command omctl is a thin operator CLI for the Open Markets backend. It lets you drive a "simulated
// city" by hand — create an account, join a friend's league, post reports, inspect the price index —
// so you can play a counterparty against a real in-game city without any AI. Credentials are stored in
// a small profile JSON (-profile), so you can run several cities side by side:
//
//	omctl -profile cityB.json account
//	omctl -profile cityB.json league-join -code K7Q2-9F3M
//	omctl -profile cityB.json report -commodity Oil -net 2000
//	omctl -profile cityB.json prices
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
)

type profile struct {
	Server    string `json:"server"`
	AccountID string `json:"accountId"`
	Secret    string `json:"secret"`
	LeagueID  string `json:"leagueId"`
}

func main() {
	server := flag.String("server", "http://localhost:8080", "backend base URL")
	profPath := flag.String("profile", "omctl.json", "path to the credentials profile JSON")
	token := flag.String("token", "", "admin/console token (OM_CONSOLE_TOKEN) for the admin commands")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
	}
	p := loadProfile(*profPath)
	if p.Server == "" {
		p.Server = *server
	}
	adminTok := *token
	if adminTok == "" {
		adminTok = os.Getenv("OM_CONSOLE_TOKEN") // fall back to the env so the operator can export it once
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "stats":
		var out map[string]any
		mustAdminReq(p, adminTok, "GET", "/admin/stats", &out)
		printJSON(out)
	case "list-leagues":
		var out map[string]any
		mustAdminReq(p, adminTok, "GET", "/admin/leagues", &out)
		printJSON(out)
	case "league":
		if len(rest) != 1 {
			fatalMsg("usage: omctl league <leagueId>")
		}
		var out map[string]any
		mustAdminReq(p, adminTok, "GET", "/admin/leagues/"+rest[0], &out)
		printJSON(out)
	case "audit":
		// GET /audit is member-scoped (?league=ID), authenticated with the profile credentials — not the admin
		// token. Useful from omctl to sanity-check the conservation invariant for one of the caller's leagues.
		lid := p.LeagueID
		if len(rest) == 1 {
			lid = rest[0]
		}
		if lid == "" {
			fatalMsg("usage: omctl audit <leagueId>  (or set leagueId in the profile)")
		}
		var out map[string]any
		mustReq(p, "GET", "/audit?league="+lid, nil, &out)
		printJSON(out)
	case "account":
		var out struct{ AccountID, Secret string }
		mustReq(p, "POST", "/accounts", nil, &out)
		p.AccountID, p.Secret = out.AccountID, out.Secret
		save(*profPath, p)
		fmt.Printf("account created: %s\n", p.AccountID)
	case "league-create":
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		name := fs.String("name", "My League", "league name")
		fs.Parse(rest)
		var out struct{ LeagueID, JoinCode, Name string }
		mustReq(p, "POST", "/leagues", map[string]any{"name": *name}, &out)
		p.LeagueID = out.LeagueID
		save(*profPath, p)
		fmt.Printf("league %q created\n  leagueId: %s\n  joinCode: %s  (share this)\n", out.Name, out.LeagueID, out.JoinCode)
	case "league-join":
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		code := fs.String("code", "", "join code to enter")
		fs.Parse(rest)
		var out struct {
			LeagueID string `json:"leagueId"`
		}
		mustReq(p, "POST", "/leagues/join", map[string]any{"joinCode": *code}, &out)
		p.LeagueID = out.LeagueID
		save(*profPath, p)
		fmt.Printf("joined league %s\n", out.LeagueID)
	case "report":
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		commodity := fs.String("commodity", "", "commodity name")
		net := fs.Float64("net", 0, "net supply (+export / -import)")
		fs.Parse(rest)
		mustReq(p, "POST", "/report", map[string]any{
			"leagueId": p.LeagueID, "commodity": *commodity, "netSupply": *net,
		}, nil)
		fmt.Printf("reported %s net=%.0f\n", *commodity, *net)
	case "index", "prices":
		var out map[string]any
		mustReq(p, "GET", "/"+cmd+"?league="+p.LeagueID, nil, &out)
		printJSON(out)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: omctl [-server URL] [-profile FILE] [-token TOKEN] <command> [flags]")
	fmt.Fprintln(os.Stderr, "  city commands: account | league-create | league-join | report | index | prices")
	fmt.Fprintln(os.Stderr, "  admin commands (need -token / OM_CONSOLE_TOKEN): stats | list-leagues | league <id>")
	fmt.Fprintln(os.Stderr, "  audit <leagueId>  (member-scoped; uses profile credentials)")
	os.Exit(2)
}

// printJSON pretty-prints a decoded JSON value to stdout.
func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func fatalMsg(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(2)
}

// mustAdminReq performs an admin (console-token) request and decodes into out. The admin surface is gated by
// OM_CONSOLE_TOKEN, sent here as the X-Console-Token header (the server also accepts ?token= / Authorization).
func mustAdminReq(p profile, token, method, path string, out any) {
	if token == "" {
		fatalMsg("admin command requires -token or OM_CONSOLE_TOKEN")
	}
	req, err := http.NewRequest(method, p.Server+path, nil)
	if err != nil {
		fatal(err)
	}
	req.Header.Set("X-Console-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "error: %s %s -> %d: %s\n", method, path, resp.StatusCode, raw)
		os.Exit(1)
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			fatal(err)
		}
	}
}

// mustReq performs an authenticated JSON request, decodes into out (if non-nil), and exits on error or
// non-2xx status.
func mustReq(p profile, method, path string, body any, out any) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, p.Server+path, rdr)
	if err != nil {
		fatal(err)
	}
	if p.AccountID != "" && p.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+p.AccountID+"."+p.Secret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "error: %s %s -> %d: %s\n", method, path, resp.StatusCode, raw)
		os.Exit(1)
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			fatal(err)
		}
	}
}

func loadProfile(path string) profile {
	var p profile
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	return p
}

func save(path string, p profile) {
	b, _ := json.MarshalIndent(p, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
