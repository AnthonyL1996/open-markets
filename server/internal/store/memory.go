package store

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"openmarkets/server/internal/id"
	"openmarkets/server/internal/market"
)

// Memory is an in-memory Store with optional atomic JSON persistence. It is the dev/hobby default;
// for a friend-group-scale service it is plenty. All public methods take the lock; file writes use a
// separate mutex so request handling never blocks on disk while holding shared state.
type Memory struct {
	mu       sync.RWMutex
	accounts map[string]Account
	leagues  map[string]League
	byJoin   map[string]string          // joinCode -> leagueID
	members  map[string]map[string]bool // leagueID -> set of accountID
	// reports keyed by accountID|leagueID|commodity -> latest report.
	reports map[string]Report
	// cityProfiles keyed by accountID -> latest leaguemate-visible city snapshot (population/happiness/etc.).
	// City-level (not per-league); replaced on each report. Persisted (its ReportedAt is the durable "last seen").
	cityProfiles map[string]CityProfile
	// cityProfileHist keyed by accountID -> time-ordered (oldest→newest) ring of retained city snapshots. Bounded
	// and downsampled (recent-dense/old-sparse) on each PutCityProfile. Persisted so history survives a restart.
	cityProfileHist map[string][]CityProfile
	// trades and bonds keyed by id; events is an append-only league-scoped settlement log.
	trades   map[string]Trade
	bonds    map[string]Bond
	events   []SettlementEvent
	eventSeq int64 // monotonic settlement-event sequence (shared; filtered per league on read)
	// chronicle is the append-only league saga (social slice 2); chronicleSeq is its own monotonic sequence
	// (shared across leagues, filtered per league on read — its own counter, NOT eventSeq). Persisted.
	chronicle    []ChronicleEntry
	chronicleSeq int64
	// effects keyed by effect id: temporary city buffs (M8 investment-office + completed-megaproject). Expired via
	// the due-clock.
	effects map[string]Effect
	// projects keyed by project id: co-op MEGAPROJECTS (Great Works, social slice 4). Persisted.
	projects map[string]Project
	// M9 market dynamics (EPHEMERAL — not persisted): priceEvents = the GLOBAL per-commodity price-shock map
	// (shared across all leagues); indexHist = a short rolling effective-index ring per "leagueID|commodity" for the
	// dashboard sparkline. Both advanced by the due-clock (AdvanceEvents / SampleHistory).
	priceEvents map[string]market.EventState
	indexHist   map[string][]float64
	eventParams market.EventParams
	mktParams   market.Params // index aggregation params (VolumeRef/Min/Max); set at startup, sane()-defaulted
	commodities []string      // the known commodity set (base-price table keys) the shock generator rolls over
	rng         *rand.Rand    // shock RNG; seeded from time at construction, overridable for deterministic tests
	pricer      Pricer        // resolves accept-time commodity unit prices; nil → trades can't be accepted
	econ        EconParams    // league economy knobs (default: harsh) governing default rates / auto-bonds
	// lastActive is a RUNTIME (not persisted) online signal: the last time each account made an authenticated
	// request. The due-clock grants an offline obligor extra grace before auto-bonding (bounded). Resets on
	// restart → everyone is briefly "offline" (lenient), which is fine.
	lastActive map[string]time.Time

	// epoch is a data-tied id generated ONCE when the store is fresh and persisted in the snapshot. A normal
	// restart reloads it (unchanged); a wiped/recreated data file gets a new one. Clients compare it to detect a
	// genuine server reset (the only time replaying settlements from 0 is safe — no old events to double-book).
	epoch string

	// now is injectable so tests get deterministic timestamps; defaults to time.Now.
	now func() time.Time

	path   string // JSON snapshot path; "" disables persistence
	saveMu sync.Mutex
}

// NewMemory returns an empty store. path "" disables persistence (handy for tests).
func NewMemory(path string) *Memory {
	return &Memory{
		accounts:        map[string]Account{},
		leagues:         map[string]League{},
		byJoin:          map[string]string{},
		members:         map[string]map[string]bool{},
		reports:         map[string]Report{},
		cityProfiles:    map[string]CityProfile{},
		cityProfileHist: map[string][]CityProfile{},
		trades:          map[string]Trade{},
		bonds:           map[string]Bond{},
		effects:         map[string]Effect{},
		projects:        map[string]Project{},
		priceEvents:     map[string]market.EventState{},
		indexHist:       map[string][]float64{},
		eventParams:     market.DefaultEventParams(),
		rng:             rand.New(rand.NewSource(time.Now().UnixNano())),
		lastActive:      map[string]time.Time{},
		econ:            DefaultEconParams(),
		epoch:           id.New(), // fresh store → new epoch; Open overwrites it from a persisted snapshot
		now:             time.Now,
		path:            path,
	}
}

// Epoch returns the store's data epoch (stable across restarts, new after a wipe).
func (m *Memory) Epoch() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.epoch
}

// Pricer resolves a commodity's frozen unit price (cents per whole unit) for a league at accept time
// (typically base price × league index). Injected so the store stays decoupled from the market/pricing layer.
//
// INVARIANT: a Pricer MUST be lock-free with respect to the store's m.mu. SetTradeStatus calls it AFTER
// releasing m.mu (so the real pricer can read LeagueReports under RLock without deadlock); a Pricer that takes
// m's write lock would deadlock at runtime with no compile-time signal.
type Pricer func(leagueID, commodity string) (unitPriceCents int64, ok bool)

// SetPricer installs the accept-time price source. Safe to call at startup before serving.
func (m *Memory) SetPricer(p Pricer) {
	m.mu.Lock()
	m.pricer = p
	m.mu.Unlock()
}

// SetClock overrides the time source. Startup/test only (deterministic timestamps, due-clock tests).
func (m *Memory) SetClock(now func() time.Time) {
	m.mu.Lock()
	m.now = now
	m.mu.Unlock()
}

// snapshot is the on-disk shape. Exported fields so encoding/json can round-trip it.
type snapshot struct {
	Accounts []Account `json:"accounts"`
	Leagues  []League  `json:"leagues"`
	Members  []member  `json:"members"`
	Reports  []Report  `json:"reports"`
	// The legacy single-commodity contract system is RETIRED — its snapshot section is no longer written, and an
	// older data file's `contracts` key is harmlessly ignored on load (json.Unmarshal drops unknown fields).
	// Append-only sections — older snapshots without them decode to empty and behave as before (save-safe).
	Trades       []Trade           `json:"trades,omitempty"`
	Bonds        []Bond            `json:"bonds,omitempty"`
	Events       []SettlementEvent `json:"events,omitempty"`
	EventSeq     int64             `json:"eventSeq,omitempty"`
	Effects      []Effect          `json:"effects,omitempty"`      // temporary city buffs (M8 investment-office)
	Epoch        string            `json:"epoch,omitempty"`        // data-tied id for client server-reset detection
	CityProfiles []CityProfile     `json:"cityProfiles,omitempty"` // leaguemate-visible city snapshots (population/happiness/…)
	// CityProfileHist is the retained time-series per account (accountID → oldest→newest). Append-only section:
	// an older snapshot without this key decodes to nil → history just starts fresh (save-safe).
	CityProfileHist map[string][]CityProfile `json:"cityProfileHist,omitempty"`
	// Chronicle is the durable league saga (social slice 2). Append-only section — an older snapshot without
	// these keys decodes to empty and behaves as before (save-safe).
	Chronicle    []ChronicleEntry `json:"chronicle,omitempty"`
	ChronicleSeq int64            `json:"chronicleSeq,omitempty"`
	// Projects is the co-op MEGAPROJECTS section (social slice 4). Append-only — an older snapshot without this key
	// decodes to empty and behaves as before (save-safe).
	Projects []Project `json:"projects,omitempty"`
}

type member struct {
	LeagueID  string `json:"leagueId"`
	AccountID string `json:"accountId"`
}

// Open loads a store from path if the file exists, otherwise returns a fresh one bound to path.
func Open(path string) (*Memory, error) {
	m := NewMemory(path)
	if path == "" {
		return m, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	var snap snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, err
	}
	for _, a := range snap.Accounts {
		m.accounts[a.ID] = a
	}
	for _, l := range snap.Leagues {
		m.leagues[l.ID] = l
		m.byJoin[l.JoinCode] = l.ID
	}
	for _, mm := range snap.Members {
		if m.members[mm.LeagueID] == nil {
			m.members[mm.LeagueID] = map[string]bool{}
		}
		m.members[mm.LeagueID][mm.AccountID] = true
	}
	for _, r := range snap.Reports {
		m.reports[reportKey(r.AccountID, r.LeagueID, r.Commodity)] = r
	}
	for _, t := range snap.Trades {
		m.trades[t.ID] = t
	}
	for _, b := range snap.Bonds {
		m.bonds[b.ID] = b
	}
	m.events = append(m.events, snap.Events...)
	m.eventSeq = snap.EventSeq
	for _, e := range snap.Effects {
		m.effects[e.ID] = e
	}
	for _, p := range snap.CityProfiles {
		m.cityProfiles[p.AccountID] = p
	}
	for aid, hist := range snap.CityProfileHist {
		// Copy out of the decoded snapshot so the store owns its own backing arrays.
		m.cityProfileHist[aid] = append([]CityProfile(nil), hist...)
	}
	m.chronicle = append(m.chronicle, snap.Chronicle...)
	m.chronicleSeq = snap.ChronicleSeq
	for _, pr := range snap.Projects {
		m.projects[pr.ID] = pr
	}
	if snap.Epoch != "" {
		m.epoch = snap.Epoch // existing data → keep its epoch (a restart looks unchanged to clients)
		return m, nil
	}
	// Migration: an older snapshot without an epoch. Persist the freshly-minted one NOW, so a restart before
	// the next flush can't mint a DIFFERENT epoch and look like a wipe to clients (Codex HIGH).
	if err := m.persist(); err != nil {
		return nil, err
	}
	return m, nil
}

func reportKey(account, league, commodity string) string {
	return account + "|" + league + "|" + commodity
}

func (m *Memory) CreateAccount() (Account, string, error) {
	secret := id.Secret()
	salt := id.Salt()
	a := Account{
		ID:         id.New(),
		Salt:       salt,
		SecretHash: id.Hash(salt, secret),
		Created:    m.clock(),
	}
	m.mu.Lock()
	m.accounts[a.ID] = a
	m.mu.Unlock()
	return a, secret, m.persist()
}

func (m *Memory) GetAccount(idStr string) (Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.accounts[idStr]
	if !ok {
		return Account{}, ErrNotFound
	}
	return a, nil
}

func (m *Memory) SetAccountName(idStr, name string) (Account, error) {
	m.mu.Lock()
	a, ok := m.accounts[idStr]
	if !ok {
		m.mu.Unlock()
		return Account{}, ErrNotFound
	}
	a.DisplayName = name
	m.accounts[idStr] = a
	m.mu.Unlock()
	return a, m.persist()
}

func (m *Memory) CreateLeague(ownerID, name string) (League, error) {
	m.mu.Lock()
	if _, ok := m.accounts[ownerID]; !ok {
		m.mu.Unlock()
		return League{}, ErrNotFound
	}
	// Mint a join code that isn't already in use (collisions are astronomically unlikely, but cheap
	// to rule out).
	var code string
	for {
		code = id.Code()
		if _, taken := m.byJoin[code]; !taken {
			break
		}
	}
	l := League{ID: id.New(), Name: name, JoinCode: code, OwnerID: ownerID, Created: m.clock()}
	m.leagues[l.ID] = l
	m.byJoin[code] = l.ID
	m.members[l.ID] = map[string]bool{ownerID: true}
	// Chronicle: the league's first line (frozen narration, names resolved now).
	leagueName := l.Name
	if leagueName == "" {
		leagueName = "the league"
	}
	m.appendChronicleLocked(ChronicleEntry{
		LeagueID: l.ID, Kind: "founded", ActorID: ownerID,
		Text: m.displayNameLocked(ownerID) + " founded " + leagueName + ".",
	})
	m.mu.Unlock()
	return l, m.persist()
}

func (m *Memory) LeagueByJoinCode(code string) (League, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	lid, ok := m.byJoin[code]
	if !ok {
		return League{}, ErrNotFound
	}
	return m.leagues[lid], nil
}

func (m *Memory) GetLeague(idStr string) (League, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	l, ok := m.leagues[idStr]
	if !ok {
		return League{}, ErrNotFound
	}
	return l, nil
}

func (m *Memory) JoinLeague(accountID, leagueID string) error {
	m.mu.Lock()
	if _, ok := m.accounts[accountID]; !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if _, ok := m.leagues[leagueID]; !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if m.members[leagueID] == nil {
		m.members[leagueID] = map[string]bool{}
	}
	if m.members[leagueID][accountID] {
		m.mu.Unlock()
		return ErrAlreadyMember
	}
	m.members[leagueID][accountID] = true
	m.appendChronicleLocked(ChronicleEntry{
		LeagueID: leagueID, Kind: "joined", ActorID: accountID,
		Text: m.displayNameLocked(accountID) + " joined the league.",
	})
	m.mu.Unlock()
	return m.persist()
}

func (m *Memory) LeagueMembers(leagueID string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.leagues[leagueID]; !ok {
		return nil, ErrNotFound
	}
	set := m.members[leagueID]
	out := make([]string, 0, len(set))
	for aid := range set {
		out = append(out, aid)
	}
	sort.Strings(out)
	return out, nil
}

// LeaguesForAccount returns every league the account belongs to, sorted by league id. Derived from the
// forward members map (no reverse index to persist): friend-group scale means few leagues to scan.
func (m *Memory) LeaguesForAccount(accountID string) ([]League, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]League, 0)
	for lid, set := range m.members {
		if !set[accountID] {
			continue
		}
		if lg, ok := m.leagues[lid]; ok {
			out = append(out, lg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// AllLeagues returns every league known to the store, sorted by id (operator listing).
func (m *Memory) AllLeagues() []League {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]League, 0, len(m.leagues))
	for _, l := range m.leagues {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AdminStats returns aggregate operator counts. Read-only; a single pass over each map.
func (m *Memory) AdminStats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := Stats{
		Accounts:       len(m.accounts),
		Leagues:        len(m.leagues),
		TradesByStatus: map[string]int{},
		BondsByStatus:  map[string]int{},
	}
	for _, set := range m.members {
		s.Members += len(set)
	}
	for _, t := range m.trades {
		s.TradesByStatus[t.Status]++
	}
	for _, b := range m.bonds {
		s.BondsByStatus[b.Status]++
	}
	for _, p := range m.cityProfiles {
		if p.Suspect {
			s.SuspectCityProfiles++
		}
	}
	for _, e := range m.events {
		c := e.Cents
		if c < 0 {
			c = -c
		}
		s.SettlementVolumeCents += c
	}
	s.SettlementEventCount = len(m.events)
	return s
}

// DeleteLeague removes a league and cascades all of its league-scoped state (members, reports, trades, bonds,
// effects, settlement events). Trades/bonds are keyed by id, so we scan and drop any belonging to the league.
func (m *Memory) DeleteLeague(leagueID string) error {
	m.mu.Lock()
	l, ok := m.leagues[leagueID]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	delete(m.leagues, leagueID)
	delete(m.byJoin, l.JoinCode)
	delete(m.members, leagueID)
	for k, r := range m.reports {
		if r.LeagueID == leagueID {
			delete(m.reports, k)
		}
	}
	for id, t := range m.trades {
		if t.LeagueID == leagueID {
			delete(m.trades, id)
		}
	}
	for id, b := range m.bonds {
		if b.LeagueID == leagueID {
			delete(m.bonds, id)
		}
	}
	for id, e := range m.effects {
		if e.LeagueID == leagueID {
			delete(m.effects, id)
		}
	}
	for id, pr := range m.projects {
		if pr.LeagueID == leagueID {
			delete(m.projects, id)
		}
	}
	kept := m.events[:0]
	for _, e := range m.events {
		if e.LeagueID != leagueID {
			kept = append(kept, e)
		}
	}
	m.events = kept
	m.mu.Unlock()
	return m.persist()
}

// RemoveMember drops a single membership (v1: the membership row only; the account and its settled history
// stay). ErrNotFound if the league is unknown or the account isn't a member.
func (m *Memory) RemoveMember(accountID, leagueID string) error {
	m.mu.Lock()
	if _, ok := m.leagues[leagueID]; !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	set := m.members[leagueID]
	if set == nil || !set[accountID] {
		m.mu.Unlock()
		return ErrNotFound
	}
	delete(set, accountID)
	m.mu.Unlock()
	return m.persist()
}

func (m *Memory) IsMember(accountID, leagueID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	set := m.members[leagueID]
	return set != nil && set[accountID]
}

func (m *Memory) PutReport(r Report) error {
	if r.TS.IsZero() {
		r.TS = m.clock()
	}
	m.mu.Lock()
	if !m.isMemberLocked(r.AccountID, r.LeagueID) {
		m.mu.Unlock()
		return ErrNotFound
	}
	m.reports[reportKey(r.AccountID, r.LeagueID, r.Commodity)] = r
	m.mu.Unlock()
	return m.persist()
}

func (m *Memory) LeagueReports(leagueID string) ([]market.Report, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.leagues[leagueID]; !ok {
		return nil, ErrNotFound
	}
	var out []market.Report
	for _, r := range m.reports {
		if r.LeagueID == leagueID {
			out = append(out, market.Report{AccountID: r.AccountID, Commodity: r.Commodity, NetSupply: r.NetSupply})
		}
	}
	return out, nil
}

// MarketMoverByAccount sums |NetSupply| over each member's latest reports in a league, keyed by accountID
// (the "market mover" leaderboard signal — how much net supply/demand a city moves through the market). Unlike
// LeagueReports (which drops the reporter for market aggregation), this retains the account so the board can
// rank members. ErrNotFound if the league is unknown. Read-only.
func (m *Memory) MarketMoverByAccount(leagueID string) (map[string]float64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.leagues[leagueID]; !ok {
		return nil, ErrNotFound
	}
	out := map[string]float64{}
	for _, r := range m.reports {
		if r.LeagueID != leagueID {
			continue
		}
		v := r.NetSupply
		if v < 0 {
			v = -v
		}
		out[r.AccountID] += v
	}
	return out, nil
}

// AllAccountIDs returns every distinct account id known to the store (the union of league members is a subset
// of the account map keys), sorted. Used by the global leaderboards to enumerate the candidate player set across
// all leagues. Read-only.
func (m *Memory) AllAccountIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.accounts))
	for id := range m.accounts {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (m *Memory) isMemberLocked(accountID, leagueID string) bool {
	set := m.members[leagueID]
	return set != nil && set[accountID]
}

// Touch records that accountID just made an authenticated request (a runtime online signal for the due-clock's
// offline grace). Not persisted. Safe for concurrent use.
func (m *Memory) Touch(accountID string) {
	if accountID == "" {
		return
	}
	m.mu.Lock()
	m.lastActive[accountID] = m.clock()
	m.mu.Unlock()
}

// LastActive returns the last time accountID was seen, and whether it has ever been seen this run.
func (m *Memory) LastActive(accountID string) (time.Time, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.lastActive[accountID]
	return t, ok
}

// bumpReliabilityLocked records an on-time (or missed) installment for an account's reputation. Caller holds m.mu.
func (m *Memory) bumpReliabilityLocked(accountID string, onTime bool) {
	a, ok := m.accounts[accountID]
	if !ok {
		return
	}
	if onTime {
		a.OnTimeCount++
	} else {
		a.MissedCount++
	}
	m.accounts[accountID] = a
}

// displayNameLocked resolves an account's player-facing name (its DisplayName, falling back to a short id
// prefix). Caller holds m.mu. Used to FREEZE names into chronicle narration at append time.
func (m *Memory) displayNameLocked(accountID string) string {
	if a, ok := m.accounts[accountID]; ok && a.DisplayName != "" {
		return a.DisplayName
	}
	return shortChronID(accountID)
}

// shortChronID trims an opaque id to a short, human-glanceable prefix (the chronicle's display fallback).
func shortChronID(idStr string) string {
	if len(idStr) > 6 {
		return idStr[:6]
	}
	if idStr == "" {
		return "someone"
	}
	return idStr
}

func (m *Memory) clock() time.Time {
	if m.now != nil {
		return m.now().UTC()
	}
	return time.Now().UTC()
}

// persist atomically writes the current state to m.path (temp file + rename). A no-op when path is
// empty. Snapshotting takes the read lock briefly; the disk write happens outside it under saveMu so
// concurrent persists serialise without blocking request handling on shared state.
func (m *Memory) persist() error {
	if m.path == "" {
		return nil
	}
	// saveMu serialises the whole snapshot+write as one critical section, so two concurrent persists
	// can't capture snapshots and then write them out of order (which would let an older snapshot land
	// on disk last and lose a newer in-memory state across a restart). Taken BEFORE the read lock; no
	// caller holds mu when calling persist, so there is no lock-ordering hazard.
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	m.mu.RLock()
	snap := snapshot{}
	for _, a := range m.accounts {
		snap.Accounts = append(snap.Accounts, a)
	}
	for _, l := range m.leagues {
		snap.Leagues = append(snap.Leagues, l)
	}
	for lid, set := range m.members {
		for aid := range set {
			snap.Members = append(snap.Members, member{LeagueID: lid, AccountID: aid})
		}
	}
	for _, r := range m.reports {
		snap.Reports = append(snap.Reports, r)
	}
	for _, t := range m.trades {
		snap.Trades = append(snap.Trades, t)
	}
	for _, b := range m.bonds {
		snap.Bonds = append(snap.Bonds, b)
	}
	snap.Events = append(snap.Events, m.events...)
	snap.EventSeq = m.eventSeq
	for _, e := range m.effects {
		snap.Effects = append(snap.Effects, e)
	}
	for _, p := range m.cityProfiles {
		snap.CityProfiles = append(snap.CityProfiles, p)
	}
	if len(m.cityProfileHist) > 0 {
		snap.CityProfileHist = make(map[string][]CityProfile, len(m.cityProfileHist))
		for aid, hist := range m.cityProfileHist {
			snap.CityProfileHist[aid] = append([]CityProfile(nil), hist...)
		}
	}
	snap.Epoch = m.epoch
	snap.Chronicle = append(snap.Chronicle, m.chronicle...)
	snap.ChronicleSeq = m.chronicleSeq
	for _, pr := range m.projects {
		snap.Projects = append(snap.Projects, pr)
	}
	m.mu.RUnlock()

	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}

	if dir := filepath.Dir(m.path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// Flush forces a durable write of current state.
func (m *Memory) Flush() error { return m.persist() }
