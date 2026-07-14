// Package store defines the persistence boundary for accounts, leagues, memberships, and reports.
// The Store interface is the seam BACKEND.md calls for: the in-memory + JSON implementation here is
// the dev/hobby default, and a Postgres implementation can drop in for production without touching
// the API layer.
package store

import (
	"errors"
	"time"

	"openmarkets/server/internal/market"
)

// Sentinel errors the API layer maps to HTTP status codes.
var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyMember = errors.New("already a member")
	ErrConflict      = errors.New("conflict")
)

// Account is an opaque identity. The plaintext secret is returned only once (at creation) and never
// stored — only Salt + SecretHash are persisted.
type Account struct {
	ID         string    `json:"id"`
	Salt       string    `json:"salt"`
	SecretHash string    `json:"secretHash"`
	Created    time.Time `json:"created"`
	// DisplayName is an optional, player-chosen friendly name shown to leaguemates instead of the opaque ID.
	// Append-only field: older snapshots without it decode to "" and behave exactly as before.
	DisplayName string `json:"displayName,omitempty"`
	// Reputation counters (append-only): on-time vs missed installments across all this account's
	// trades/bonds, used to derive a reliability score shown to leaguemates. Informational, not auto-priced.
	OnTimeCount int `json:"onTimeCount,omitempty"`
	MissedCount int `json:"missedCount,omitempty"`
}

// Reliability is a 0..100 on-time percentage (100 when there's no history yet).
func (a Account) Reliability() int {
	total := a.OnTimeCount + a.MissedCount
	if total == 0 {
		return 100
	}
	return a.OnTimeCount * 100 / total
}

// CityProfile is a leaguemate-visible snapshot of an account's city, reported once per in-game day. It is
// city-level (keyed by account, not league) — the same city shows to every league the account is in. All numbers
// are cheap field-reads the client gathers from the game's District(0) aggregate + EconomyManager. Every field is
// append-only/omitempty so older snapshots decode cleanly (save-safe). ReportedAt is stamped server-side on receipt
// and doubles as the persisted "last seen" signal (survives a restart, unlike the runtime lastActive map).
type CityProfile struct {
	AccountID  string    `json:"accountId"`
	ReportedAt time.Time `json:"reportedAt"`
	CityName   string    `json:"cityName,omitempty"`
	// Vitals
	Population     int   `json:"population,omitempty"`
	Happiness      int   `json:"happiness,omitempty"`      // 0..100 (city popularity)
	Attractiveness int   `json:"attractiveness,omitempty"` // global attractiveness index
	CashCents      int64 `json:"cashCents,omitempty"`      // treasury, cents
	WeeklyIncome   int64 `json:"weeklyIncomeCents,omitempty"`
	WeeklyExpenses int64 `json:"weeklyExpensesCents,omitempty"`
	// Sector building counts (private zones)
	ResBuildings int `json:"resBuildings,omitempty"`
	ComBuildings int `json:"comBuildings,omitempty"`
	OffBuildings int `json:"offBuildings,omitempty"`
	IndBuildings int `json:"indBuildings,omitempty"`
	IndWorkers   int `json:"indWorkers,omitempty"`
	// Specialized industry workers (Industries DLC; 0 without it)
	FarmWorkers   int `json:"farmWorkers,omitempty"`
	ForestWorkers int `json:"forestWorkers,omitempty"`
	OreWorkers    int `json:"oreWorkers,omitempty"`
	OilWorkers    int `json:"oilWorkers,omitempty"`
	// Bonus
	Unemployment  int `json:"unemployment,omitempty"` // 0..100
	BuildingCount int `json:"buildingCount,omitempty"`
	Tourists      int `json:"tourists,omitempty"`
	LandValue     int `json:"landValue,omitempty"`
	Crime         int `json:"crime,omitempty"` // 0..100
	// Reliability is the account's on-time reputation (0..100) AT THE TIME this snapshot was taken. Stamped
	// server-side in PutCityProfile (append-only/omitempty — older snapshots decode to 0 and behave as before).
	Reliability int `json:"reliability,omitempty"`
	// Suspect marks a snapshot whose client-reported numbers tripped a plausibility check in PutCityProfile (an
	// implausible per-day delta). Set server-side; FLAG-not-reject by design (the snapshot is still stored) — the
	// client-reported leaderboards (population/happiness) zero a suspect member so spoofed stats can't top a board.
	// Append-only/omitempty: older snapshots decode to false (not suspect) and behave exactly as before.
	Suspect bool `json:"suspect,omitempty"`
}

// NetPoint is one point on an account's cumulative net-§ curve in a league: the running settlement total
// (cents) as of a settlement event's timestamp. Derived on read from the event log — never stored.
type NetPoint struct {
	TS    time.Time `json:"ts"`
	Cents int64     `json:"cents"`
}

// ChronicleEntry is one narrated line in a league's persistent saga (social slice 2). Unlike the activity
// feed (which re-derives its text on every read), a chronicle entry's Text is the FROZEN narration rendered
// once at append time — names are resolved then, so the chronicle is a permanent record that survives a
// rename or a member leaving. Seq is a monotonic, league-shared sequence (its own meta('chronicle_seq')
// counter, distinct from the settlement event seq). Kind ∈ {founded,joined,bailout,austerity,escaped,
// record-trade}. TargetID/Cents are present only for entries that have them (omitempty).
type ChronicleEntry struct {
	Seq      int64     `json:"seq"`
	LeagueID string    `json:"leagueId"`
	Kind     string    `json:"kind"`
	ActorID  string    `json:"actorId"`
	TargetID string    `json:"targetId,omitempty"`
	Text     string    `json:"text"`
	Cents    int64     `json:"cents,omitempty"`
	Created  time.Time `json:"created"`
}

// League is a friend group. JoinCode is the shareable invite; OwnerID created it.
type League struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	JoinCode string    `json:"joinCode"`
	OwnerID  string    `json:"ownerId"`
	Created  time.Time `json:"created"`
}

// Report is a member's latest net supply/demand for one commodity in one league. Only the latest per
// (account, league, commodity) is retained — the feed reflects current standing, not history.
type Report struct {
	AccountID string    `json:"accountId"`
	LeagueID  string    `json:"leagueId"`
	Commodity string    `json:"commodity"`
	NetSupply float64   `json:"netSupply"`
	TS        time.Time `json:"ts"`
}

// Stats is an operator-facing snapshot of aggregate store counts, served by the admin /stats surface. All
// fields are cheap aggregate reads — counts, not lists. TradesByStatus / BondsByStatus key on the canonical
// status strings (TradeOffered/…, BondActive/…); a status with no rows is simply absent from the map.
type Stats struct {
	Accounts              int            `json:"accounts"`
	Leagues               int            `json:"leagues"`
	Members               int            `json:"members"` // total memberships across all leagues (not distinct accounts)
	TradesByStatus        map[string]int `json:"tradesByStatus"`
	BondsByStatus         map[string]int `json:"bondsByStatus"`
	SuspectCityProfiles   int            `json:"suspectCityProfiles"`
	SettlementVolumeCents int64          `json:"settlementVolumeCents"` // sum of |cents| over every settlement event
	SettlementEventCount  int            `json:"settlementEventCount"`
}

// Store is the data boundary. All methods are safe for concurrent use.
type Store interface {
	// CreateAccount mints an account and returns it alongside the one-time plaintext secret.
	CreateAccount() (Account, string, error)
	GetAccount(id string) (Account, error)
	// SetAccountName sets (or clears, when name is "") the account's display name. ErrNotFound if unknown.
	SetAccountName(id, name string) (Account, error)

	// CreateLeague creates a league owned by ownerID, who is auto-joined.
	CreateLeague(ownerID, name string) (League, error)
	LeagueByJoinCode(code string) (League, error)
	GetLeague(id string) (League, error)
	JoinLeague(accountID, leagueID string) error
	IsMember(accountID, leagueID string) bool
	// LeagueMembers returns the account ids in a league, sorted; ErrNotFound if the league is unknown.
	LeagueMembers(leagueID string) ([]string, error)
	// LeaguesForAccount returns every league the account is a member of, sorted by id.
	LeaguesForAccount(accountID string) ([]League, error)

	// ── Admin surface (token-gated) ──
	// AllLeagues returns every league known to the store, sorted by id (operator listing).
	AllLeagues() []League
	// AdminStats returns aggregate operator counts (accounts/leagues/members/trades+bonds by status/…).
	AdminStats() Stats
	// DeleteLeague removes a league and CASCADES its members, reports, trades, bonds, effects, and
	// settlement events — a full "reset" of that league. ErrNotFound for an unknown league.
	DeleteLeague(leagueID string) error
	// RemoveMember drops a single membership (the account stays; its settled history is left intact).
	// ErrNotFound if the league is unknown or the account isn't a member.
	RemoveMember(accountID, leagueID string) error

	// PutReport upserts the caller's latest report for (league, commodity).
	PutReport(r Report) error
	// LeagueReports returns the latest report per (member, commodity) for a league, shaped for
	// market aggregation.
	LeagueReports(leagueID string) ([]market.Report, error)
	// MarketMoverByAccount sums |NetSupply| over each member's latest reports in a league, keyed by accountID
	// (the leaderboard "market mover" signal). Read-only; ErrNotFound for an unknown league.
	MarketMoverByAccount(leagueID string) (map[string]float64, error)
	// AllAccountIDs returns every distinct account id known to the store, sorted — the candidate set the global
	// leaderboards aggregate over. Read-only.
	AllAccountIDs() []string

	// M9 market dynamics: the global active price shocks (folded into the index alongside per-league elasticity),
	// and the rolling effective-index history per commodity (the server-served sparkline).
	EventStates() map[string]market.EventState
	// SetEvent sets the GLOBAL ephemeral price event for a commodity (replacing any active one). The crisis
	// scheduler (social slice 3) uses it to inject a named, narrated crisis that rides the same global event map
	// every league sees; AdvanceEvents then decays it like any other shock (preserving the crisis name).
	SetEvent(commodity string, e market.EventState)
	IndexHistory(leagueID string) map[string][]float64

	// Trades — the two-sided basket (Phase 2a). CreateTrade stores an offered trade; SetTradeStatus does the
	// atomic accept/decline/cancel (accept freezes line values via the injected Pricer).
	CreateTrade(t Trade) (Trade, error)
	GetTrade(id string) (Trade, error)
	TradesFor(leagueID, accountID string) ([]Trade, error)
	// TradeVolumeByLeague counts COMPLETED trades per member in a league (both parties credited), computed once
	// per league for the global leaderboard (cheaper than TradesFor per member).
	TradeVolumeByLeague(leagueID string) map[string]int64
	SetTradeStatus(accountID, tradeID, action string) (Trade, error)
	// SettleTradeInstallment books the net cash transfer for the current installment as a settlement event;
	// MissTradeInstallment (due-clock) auto-bonds the unmet amount.
	SettleTradeInstallment(accountID, tradeID string) (Trade, SettlementEvent, error)
	MissTradeInstallment(tradeID string) (Trade, Bond, error)
	// ReportTradeShortfall (M6) converts a give-side delivery shortfall into a cash-debt bond (caller → other
	// party) at the trade's default rate; idempotent per (trade, installment). Cash settlement is unaffected.
	ReportTradeShortfall(accountID, tradeID string, installment int, reportedCents int64) (Trade, Bond, error)

	// Bonds — the credit layer. Auto-bonds are minted by MissTradeInstallment; manual lending lands later.
	GetBond(id string) (Bond, error)
	BondsFor(leagueID, accountID string) ([]Bond, error)
	SettleBondInstallment(accountID, bondID string) (Bond, SettlementEvent, error)
	MissBondInstallment(bondID string) (Bond, bool, error)

	// Manual loan negotiation (Phase 3): offer → counter/accept/decline/cancel. On accept the principal
	// transfers lender→borrower as a settlement event.
	OfferLoan(b Bond) (Bond, error)
	CounterLoan(accountID, bondID string, principalCents, interestBps int64, installments int) (Bond, error)
	AcceptLoan(accountID, bondID string) (Bond, SettlementEvent, error)
	SetLoanStatus(accountID, bondID, action string) (Bond, error)

	// SettlementsForAccount returns the caller's own settlement events after `since` plus the caller's highest
	// event seq (latestSeq, for server-reset detection). Caller-scoped for privacy + smaller responses.
	SettlementsForAccount(leagueID, accountID string, since int64) (events []SettlementEvent, latestSeq int64, err error)

	// SettlementsSince returns the league's settlement events with Seq>sinceSeq, ascending by Seq, capped at
	// limit (limit<=0 → a sane default). League-wide (every member's transfers) — the activity-feed reader.
	// Returns a copy.
	SettlementsSince(leagueID string, sinceSeq int64, limit int) []SettlementEvent

	// CityState derives a city's austerity status in a league (in austerity while it owes any terminally
	// defaulted bond; outstandingCents is the total garnishable balance still owed).
	CityState(leagueID, accountID string) (austerity bool, outstandingCents int64, defaultedBonds int)

	// GrantInvestment books a symmetric issuer→grantee cash transfer (conserving) and creates a temporary
	// investment-office buff on the grantee (M8 co-op lever). CityEffects returns a city's active buffs.
	// ExpireEffectsTick ages every active buff by one due-cycle tick, removing the expired ones.
	GrantInvestment(leagueID, issuerID, granteeID string, costCents int64, days int, demandKind string) (Effect, SettlementEvent, error)
	CityEffects(leagueID, accountID string) []Effect       // active investments RECEIVED by the account
	CityEffectsIssued(leagueID, accountID string) []Effect // active investments MADE by the account
	LeagueEffects(leagueID string) []Effect                // ALL active investments in the league (transparency)
	InvestmentHistory(leagueID string) []SettlementEvent   // durable record of every investment (incl. expired)
	ExpireEffectsTick() int

	// ── Co-op MEGAPROJECTS (Great Works, social slice 4) ──
	// CreateProject stores a new open project (assigns id, status=open). ProjectsFor lists a league's projects
	// (open + completed), newest first. ContributeProjectGold books accountID → "project:"+id (conserving) for
	// min(cents, remaining §) and completes the project (granting the buff to every builder) when the last
	// requirement is met. ContributeProjectGoods adds capped commodity units (NO settlement event) and likewise
	// completes. A completed project grants each builder an Effect (the lasting buff) with NO settlement event.
	CreateProject(p Project) (Project, error)
	GetProject(id string) (Project, error)
	ProjectsFor(leagueID string) []Project
	CompletedProjectCountsByBuilder(leagueID string) map[string]int64
	ContributeProjectGold(leagueID, accountID, projectID string, cents int64) (Project, SettlementEvent, error)
	// ContributeProjectGoods returns the updated project and creditedQty — the units actually applied this call
	// (the qty capped to the commodity's remaining requirement) — so the client can refund any un-credited units.
	ContributeProjectGoods(leagueID, accountID, projectID, commodity string, qty int64) (project Project, creditedQty int64, err error)
	// LeaguesWithoutOpenProject returns the leagues with no open project — the project generator's work list.
	LeaguesWithoutOpenProject() []string

	// BailoutCity lets bailerID voluntarily pay down debtorID's defaulted bonds (oldest first) up to cents,
	// booking bailer→creditor transfers (conserving) and clearing bonds at zero — co-op rescue from austerity.
	BailoutCity(bailerID, leagueID, debtorID string, cents int64) (applied int64, events []SettlementEvent, err error)

	// Touch records an account's last authenticated activity (runtime online signal for the due-clock).
	Touch(accountID string)
	// LastActive returns when an account was last seen this run (runtime presence signal), and whether ever seen.
	LastActive(accountID string) (time.Time, bool)

	// PutCityProfile stores (replacing) an account's leaguemate-visible city snapshot; CityProfileOf reads it back.
	PutCityProfile(p CityProfile) error
	CityProfileOf(accountID string) (CityProfile, bool)
	// CityProfileHistory returns a copy of an account's retained city snapshots, oldest→newest (empty if none).
	CityProfileHistory(accountID string) []CityProfile
	// NetCentsSeries derives an account's cumulative net-§ curve in a league from the settlement event log:
	// one NetPoint per event touching the account, carrying the running cumulative net (empty if none).
	NetCentsSeries(leagueID, accountID string) []NetPoint

	// ── League Chronicle (social slice 2): a persistent, narrated history of a league's saga ──
	// AppendChronicle assigns a monotonic Seq (from the meta('chronicle_seq') counter), stamps Created from the
	// clock, persists the FROZEN narration, and returns the stored entry. Chronicle returns a league's entries with
	// Seq>sinceSeq, ascending, capped (limit<=0 → 200). ChronicleOnThisDay returns prior-day entries whose
	// (month, day) match now's and that are older than now (the "on this day in league history" recall).
	AppendChronicle(e ChronicleEntry) (ChronicleEntry, error)
	Chronicle(leagueID string, sinceSeq int64, limit int) []ChronicleEntry
	ChronicleOnThisDay(leagueID string, now time.Time) []ChronicleEntry

	// Epoch is a data-tied id (stable across restarts, new after a data wipe) for client server-reset detection.
	Epoch() string

	// AuditLeague re-derives per-account net online cash from the settlement-event log + the conservation total
	// (must be 0 — every event is a zero-sum transfer). A live invariant / sanity check.
	AuditLeague(leagueID string) (net map[string]int64, total int64, err error)

	// Flush persists current state durably (no-op for stores that persist on write).
	Flush() error
}
