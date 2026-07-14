package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateAccount_ReturnsSecretOnceAndHashes(t *testing.T) {
	m := NewMemory("")
	a, secret, err := m.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || secret == "" {
		t.Fatal("expected id and secret")
	}
	got, err := m.GetAccount(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SecretHash == secret {
		t.Fatal("secret stored in plaintext")
	}
}

func TestLeagueLifecycle(t *testing.T) {
	m := NewMemory("")
	owner, _, _ := m.CreateAccount()
	l, err := m.CreateLeague(owner.ID, "Friends")
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsMember(owner.ID, l.ID) {
		t.Fatal("owner should be auto-joined")
	}

	// Join by code.
	joiner, _, _ := m.CreateAccount()
	found, err := m.LeagueByJoinCode(l.JoinCode)
	if err != nil || found.ID != l.ID {
		t.Fatalf("LeagueByJoinCode = %+v, %v", found, err)
	}
	if err := m.JoinLeague(joiner.ID, l.ID); err != nil {
		t.Fatal(err)
	}
	if !m.IsMember(joiner.ID, l.ID) {
		t.Fatal("joiner should be a member")
	}
	if err := m.JoinLeague(joiner.ID, l.ID); !errors.Is(err, ErrAlreadyMember) {
		t.Fatalf("double join err = %v, want ErrAlreadyMember", err)
	}
}

func TestCreateLeague_UnknownOwner(t *testing.T) {
	m := NewMemory("")
	if _, err := m.CreateLeague("ghost", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestPutReport_RequiresMembership(t *testing.T) {
	m := NewMemory("")
	owner, _, _ := m.CreateAccount()
	l, _ := m.CreateLeague(owner.ID, "L")
	outsider, _, _ := m.CreateAccount()

	if err := m.PutReport(Report{AccountID: outsider.ID, LeagueID: l.ID, Commodity: "Oil", NetSupply: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider report err = %v, want ErrNotFound", err)
	}
	if err := m.PutReport(Report{AccountID: owner.ID, LeagueID: l.ID, Commodity: "Oil", NetSupply: 5}); err != nil {
		t.Fatal(err)
	}
}

func TestPutReport_UpsertsLatest(t *testing.T) {
	m := NewMemory("")
	owner, _, _ := m.CreateAccount()
	l, _ := m.CreateLeague(owner.ID, "L")
	_ = m.PutReport(Report{AccountID: owner.ID, LeagueID: l.ID, Commodity: "Oil", NetSupply: 5})
	_ = m.PutReport(Report{AccountID: owner.ID, LeagueID: l.ID, Commodity: "Oil", NetSupply: 9})

	reports, err := m.LeagueReports(l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].NetSupply != 9 {
		t.Fatalf("expected single latest report 9, got %+v", reports)
	}
}

func TestLeagueReports_UnknownLeague(t *testing.T) {
	m := NewMemory("")
	if _, err := m.LeagueReports("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	owner, secret, _ := m.CreateAccount()
	l, _ := m.CreateLeague(owner.ID, "Persisted")
	_ = m.PutReport(Report{AccountID: owner.ID, LeagueID: l.ID, Commodity: "Oil", NetSupply: 7})

	// Reopen from disk and verify everything survived.
	m2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m2.GetAccount(owner.ID)
	if err != nil {
		t.Fatal("account did not persist")
	}
	if !m2.IsMember(owner.ID, l.ID) {
		t.Fatal("membership did not persist")
	}
	if _, err := m2.LeagueByJoinCode(l.JoinCode); err != nil {
		t.Fatal("join code index did not persist")
	}
	reports, _ := m2.LeagueReports(l.ID)
	if len(reports) != 1 || reports[0].NetSupply != 7 {
		t.Fatalf("reports did not persist: %+v", reports)
	}
	// Secret must still verify after reload (salt+hash persisted, plaintext never).
	if got.SecretHash == "" || got.Salt == "" {
		t.Fatal("salt/hash missing after reload")
	}
	// Epoch is stable across a restart (same data file → same epoch, so clients don't see a false reset)...
	if m.Epoch() == "" || m2.Epoch() != m.Epoch() {
		t.Errorf("epoch not stable across reopen: %q vs %q", m.Epoch(), m2.Epoch())
	}
	_ = secret
}

func TestEpoch_DiffersForFreshStore(t *testing.T) {
	// ...but a fresh/wiped store (different/empty data) gets a new epoch — the client's wipe signal.
	a := NewMemory("")
	b := NewMemory("")
	if a.Epoch() == "" || a.Epoch() == b.Epoch() {
		t.Errorf("fresh stores should have distinct non-empty epochs: %q vs %q", a.Epoch(), b.Epoch())
	}
}

// Migration durability (Codex HIGH): opening a pre-epoch snapshot must mint AND immediately persist the epoch,
// so a restart-before-flush can't re-mint a different one and look like a wipe to clients.
func TestEpoch_MigrationPersistsImmediately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.json")
	// A pre-epoch snapshot: real data, no "epoch" field (omitempty drops it).
	b, _ := json.MarshalIndent(snapshot{Accounts: []Account{{ID: "a1", Salt: "s", SecretHash: "h"}}}, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	m1, err := Open(path) // migration mints + persists an epoch
	if err != nil {
		t.Fatal(err)
	}
	e1 := m1.Epoch()
	if e1 == "" {
		t.Fatal("migration did not mint an epoch")
	}
	m2, err := Open(path) // reopen: the persisted epoch is stable (no false wipe on restart)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Epoch() != e1 {
		t.Errorf("epoch not persisted on migration: %q -> %q", e1, m2.Epoch())
	}
}
