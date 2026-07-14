package store

import (
	"sort"
	"strconv"
	"time"

	"openmarkets/server/internal/id"
)

// Project is a co-op MEGAPROJECT (a Great Work): an AI-curated league goal that requires RESOURCES — commodity
// units plus an optional § sum — contributed by members, and on completion grants a LASTING BUFF to every member
// who helped build it (a "builder"). It is the social-slice-4 entity: the buff rides the SAME Effect/citystate
// path as the M8 investment-office grant (CoopBuff applies it on the client unchanged), but the grant carries NO
// settlement event — the buff is a side reward, not a cash transfer.
//
// CONSERVATION: a § contribution moves member → the pseudo-counterparty "project:"+ID via one balanced
// appendEvent, so AuditLeague's total stays 0 (the § sits in the project account — it's "spent on the Great
// Work"). GOODS contributions are tracked counts (no event); BUFF grants are side records (no event).
type Project struct {
	ID           string       `json:"id"`
	LeagueID     string       `json:"leagueId"`
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	Reqs         []ProjectReq `json:"reqs"`         // per-commodity unit requirements
	GoldReqCents int64        `json:"goldReqCents"` // optional § requirement (0 = none)

	Goods map[string]int64 `json:"goods"` // commodity → units contributed so far (across all members)
	Gold  int64            `json:"gold"`  // § contributed so far (cents)
	By    map[string]int64 `json:"by"`    // accountID → contribution score (units + cents/100); who counts as a builder

	// The lasting reward. BuffMagnitudeCents is a SYNTHETIC CostCents fed to InvestBuffMagnitude (so the buff
	// magnitude derives + caps EXACTLY as the investment-office buff does); BuffDays is the Effect's TicksRemaining.
	BuffKind             string `json:"buffKind"`
	BuffMagnitudeCents   int64  `json:"buffMagnitudeCents"`
	BuffDays             int    `json:"buffDays"`
	TradeRewardKind      string `json:"tradeRewardKind,omitempty"`
	TradeRewardCommodity string `json:"tradeRewardCommodity,omitempty"`
	TradeRewardPctBips   int    `json:"tradeRewardPctBips,omitempty"`

	Status  string    `json:"status"` // open | completed
	Created time.Time `json:"created"`
}

// ProjectReq is one commodity requirement: Qty units of Commodity must be contributed.
type ProjectReq struct {
	Commodity string `json:"commodity"`
	Qty       int64  `json:"qty"`
}

// Project status values.
const (
	ProjectOpen      = "open"
	ProjectCompleted = "completed"
)

// EffectProjectBuff is the Effect.Kind tag for a completed-megaproject buff (distinct from the investment-office
// kind so the two are never confused in transparency/cooldown checks).
const EffectProjectBuff = "projectBuff"

// Project buff strength is contribution-scaled with pure integer math so Memory and Postgres grant identical
// effects: scaled = advertised * (floorNum*maxBy + (floorDen-floorNum)*mine) / (floorDen*maxBy). With the current
// 1/2 floor, the top builder receives 100% of the advertised buff and a tiny contributor receives at least 50%.
const (
	ProjectRewardFloorNum int64 = 1
	ProjectRewardFloorDen int64 = 2
)

// projectCounterparty is the pseudo-account a § contribution is booked TO, so the transfer balances (AuditLeague
// total stays 0). It is a real counterparty id in the event log — the § is "spent on the Great Work".
func projectCounterparty(projectID string) string { return "project:" + projectID }

// contributionScore is how a contribution adds to the builder score By[account]: each unit counts 1, each §
// (100 cents) counts 1. Shared by goods + gold so a builder is anyone who moved the project forward.
func contributionScore(units, cents int64) int64 { return units + cents/100 }

// remainingGoods returns how many more units of a commodity the project still needs (0 if met / not required).
func (p Project) remainingGoods(commodity string) int64 {
	var req int64
	for _, r := range p.Reqs {
		if r.Commodity == commodity {
			req = r.Qty
			break
		}
	}
	rem := req - p.Goods[commodity]
	if rem < 0 {
		return 0
	}
	return rem
}

// remainingGold returns how many more cents the project still needs (0 if met / not required).
func (p Project) remainingGold() int64 {
	rem := p.GoldReqCents - p.Gold
	if rem < 0 {
		return 0
	}
	return rem
}

// isComplete reports whether every commodity Req is met AND the § requirement is met. A project with no reqs and
// no gold req is trivially complete — but the generator always produces at least one req, so that's defensive.
func (p Project) isComplete() bool {
	for _, r := range p.Reqs {
		if p.Goods[r.Commodity] < r.Qty {
			return false
		}
	}
	return p.Gold >= p.GoldReqCents
}

// ── Exported pure helpers (shared by the Postgres backend so it derives the IDENTICAL caps/completion/buff) ──

// ProjectCounterparty is the pseudo-account a § contribution is booked TO (conserving). Exported for Postgres.
func ProjectCounterparty(projectID string) string { return projectCounterparty(projectID) }

// ProjectContributionScore is the builder-score contribution of (units, cents). Exported for Postgres.
func ProjectContributionScore(units, cents int64) int64 { return contributionScore(units, cents) }

// ProjectRemainingGoods returns how many more units of a commodity a project still needs. Exported for Postgres.
func ProjectRemainingGoods(p Project, commodity string) int64 { return p.remainingGoods(commodity) }

// ProjectRemainingGold returns how many more cents a project still needs. Exported for Postgres.
func ProjectRemainingGold(p Project) int64 { return p.remainingGold() }

// ProjectIsComplete reports whether every Req + the § req is met. Exported for Postgres.
func ProjectIsComplete(p Project) bool { return p.isComplete() }

// ProjectMaxBuilderScore returns the highest contribution score in p.By. Exported so every backend feeds the same
// maxBy into NewProjectBuffEffect.
func ProjectMaxBuilderScore(p Project) int64 {
	var maxBy int64
	for _, score := range p.By {
		if score > maxBy {
			maxBy = score
		}
	}
	return maxBy
}

// ProjectBuilderCount returns how many accounts have a positive contribution score on the project.
func ProjectBuilderCount(p Project) int {
	var n int
	for _, score := range p.By {
		if score > 0 {
			n++
		}
	}
	return n
}

// ProjectTopBuilder returns the highest-scoring builder, using account id as a deterministic tie-break.
func ProjectTopBuilder(p Project) (accountID string, score int64) {
	for aid, s := range p.By {
		if s <= 0 {
			continue
		}
		if s > score || (s == score && (accountID == "" || aid < accountID)) {
			accountID, score = aid, s
		}
	}
	return accountID, score
}

// ProjectScaledBuffMagnitude applies the contribution-proportional completion reward formula. The advertised
// magnitude is a synthetic CostCents input to InvestBuffMagnitude; scaling happens before that conversion/cap.
func ProjectScaledBuffMagnitude(advertisedCents, mine, maxBy int64) int64 {
	if advertisedCents <= 0 || mine <= 0 {
		return 0
	}
	if maxBy <= 0 || mine >= maxBy {
		return advertisedCents
	}
	scaleNum := ProjectRewardFloorNum*maxBy + (ProjectRewardFloorDen-ProjectRewardFloorNum)*mine
	scaleDen := ProjectRewardFloorDen * maxBy
	return advertisedCents * scaleNum / scaleDen
}

// NewProjectBuffEffect builds the completed-megaproject buff Effect for one builder (NO settlement event — the
// caller just inserts it). The builder's contribution share scales BuffMagnitudeCents before it goes through
// InvestBuffMagnitude (same cap/floor as the investment-office buff) and BuffDays → TicksRemaining. Exported so
// Postgres grants the IDENTICAL buff.
func NewProjectBuffEffect(p Project, builder string, maxBy int64, now time.Time) Effect {
	kind := p.BuffKind
	if !ValidDemandKind(kind) {
		kind = DemandResidential
	}
	days := p.BuffDays
	if days < InvestMinDays {
		days = InvestMinDays
	} else if days > InvestMaxDays {
		days = InvestMaxDays
	}
	scaledCents := ProjectScaledBuffMagnitude(p.BuffMagnitudeCents, p.By[builder], maxBy)
	demand, attract := InvestBuffMagnitude(scaledCents)
	return Effect{
		ID: id.New(), LeagueID: p.LeagueID, IssuerID: projectCounterparty(p.ID), GranteeID: builder,
		Kind: EffectProjectBuff, CostCents: scaledCents, DemandBoost: demand, DemandKind: kind,
		AttractRate: attract, TicksRemaining: days, Created: now,
	}
}

// NewProjectTradeRewardEffect builds the themed completion trade reward for one builder. Two kinds are granted:
// marketShield (server-applied, conservation-neutral — dampens the grantee's net-supply influence on the shared
// index) and priceEdge (CLIENT-applied — the client books a better §/truck on exports of the themed commodity vs.
// the OUTSIDE world; that void-sourced income has no counterparty, so scaling it conserves nothing-that-needs-
// conserving, and peer contract settlement is a separate path the edge never touches). The server is authoritative
// for who/what/duration; the client only multiplies. Like projectBuff, this carries NO settlement event.
func NewProjectTradeRewardEffect(p Project, builder string, now time.Time) (Effect, bool) {
	if (p.TradeRewardKind != EffectMarketShield && p.TradeRewardKind != EffectPriceEdge) ||
		p.TradeRewardCommodity == "" || p.TradeRewardPctBips <= 0 {
		return Effect{}, false
	}
	days := p.BuffDays
	if days < InvestMinDays {
		days = InvestMinDays
	} else if days > InvestMaxDays {
		days = InvestMaxDays
	}
	return Effect{
		ID: id.New(), LeagueID: p.LeagueID, IssuerID: projectCounterparty(p.ID), GranteeID: builder,
		Kind: p.TradeRewardKind, Commodity: p.TradeRewardCommodity, TradePctBips: p.TradeRewardPctBips,
		TicksRemaining: days, Created: now,
	}, true
}

// CreateProject stores a new open project (assigns id, status=open, zeroes the running totals). The league must
// exist. Returns the stored project.
func (m *Memory) CreateProject(p Project) (Project, error) {
	m.mu.Lock()
	if _, ok := m.leagues[p.LeagueID]; !ok {
		m.mu.Unlock()
		return Project{}, ErrNotFound
	}
	p.ID = id.New()
	p.Status = ProjectOpen
	p.Goods = map[string]int64{}
	p.Gold = 0
	p.By = map[string]int64{}
	p.Created = m.clock()
	if m.projects == nil {
		m.projects = map[string]Project{}
	}
	m.projects[p.ID] = p
	m.mu.Unlock()
	return p, m.persist()
}

// copyProject returns p with its Goods/By maps DEEP-COPIED, so a caller mutating those maps can't corrupt the
// stored project (the value-type Project still aliases its map fields after a struct copy). Mirrors how the
// history reads copy their backing slice. Reqs is an immutable slice (never mutated after create) so it's shared.
func copyProject(p Project) Project {
	if p.Goods != nil {
		g := make(map[string]int64, len(p.Goods))
		for k, v := range p.Goods {
			g[k] = v
		}
		p.Goods = g
	}
	if p.By != nil {
		by := make(map[string]int64, len(p.By))
		for k, v := range p.By {
			by[k] = v
		}
		p.By = by
	}
	return p
}

// GetProject returns a project by id (ErrNotFound if unknown).
func (m *Memory) GetProject(idStr string) (Project, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.projects[idStr]
	if !ok {
		return Project{}, ErrNotFound
	}
	return copyProject(p), nil
}

// ProjectsFor returns a league's projects (open + completed), newest first. The generator keeps at most one OPEN
// project per league; completed ones linger as the league's record of Great Works built.
func (m *Memory) ProjectsFor(leagueID string) []Project {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Project
	for _, p := range m.projects {
		if p.LeagueID == leagueID {
			out = append(out, copyProject(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// CompletedProjectCountsByBuilder returns, per account, how many completed Great Works they helped build.
func (m *Memory) CompletedProjectCountsByBuilder(leagueID string) map[string]int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]int64{}
	for _, p := range m.projects {
		if p.LeagueID != leagueID || p.Status != ProjectCompleted {
			continue
		}
		for builder, score := range p.By {
			if score > 0 {
				out[builder]++
			}
		}
	}
	return out
}

// hasOpenProjectLocked reports whether a league already has an open project. Caller holds m.mu.
func (m *Memory) hasOpenProjectLocked(leagueID string) bool {
	for _, p := range m.projects {
		if p.LeagueID == leagueID && p.Status == ProjectOpen {
			return true
		}
	}
	return false
}

// LeaguesWithoutOpenProject returns the ids of leagues that have NO open project — the generator's work list.
func (m *Memory) LeaguesWithoutOpenProject() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for lid := range m.leagues {
		if !m.hasOpenProjectLocked(lid) {
			out = append(out, lid)
		}
	}
	sort.Strings(out)
	return out
}

// ContributeProjectGoods adds qty units of a commodity to an open project (NO settlement event — goods are
// tracked counts). qty is capped at the commodity's remaining requirement, so over-contribution is impossible.
// The contributor is credited in By, and if this contribution COMPLETES the project the buff is granted to every
// builder (see completeProjectLocked). Returns the updated project. ErrNotFound for an unknown project / non-member;
// ErrConflict if the project is not open or the commodity isn't required / already met (capped to 0).
func (m *Memory) ContributeProjectGoods(leagueID, accountID, projectID, commodity string, qty int64) (Project, int64, error) {
	m.mu.Lock()
	defer m.persistAfter()
	p, ok := m.projects[projectID]
	if !ok || p.LeagueID != leagueID {
		m.mu.Unlock()
		return Project{}, 0, ErrNotFound
	}
	if !m.isMemberLocked(accountID, leagueID) {
		m.mu.Unlock()
		return Project{}, 0, ErrNotFound
	}
	if p.Status != ProjectOpen {
		m.mu.Unlock()
		return Project{}, 0, ErrConflict
	}
	if qty <= 0 {
		m.mu.Unlock()
		return Project{}, 0, ErrConflict
	}
	rem := p.remainingGoods(commodity)
	if rem <= 0 {
		m.mu.Unlock()
		return Project{}, 0, ErrConflict // not required, or already met
	}
	if qty > rem {
		qty = rem // cap at the remaining requirement
	}
	if p.Goods == nil {
		p.Goods = map[string]int64{}
	}
	if p.By == nil {
		p.By = map[string]int64{}
	}
	p.Goods[commodity] += qty
	p.By[accountID] += contributionScore(qty, 0)
	if p.isComplete() {
		m.completeProjectLocked(&p)
	}
	m.projects[projectID] = p
	m.mu.Unlock()
	return p, qty, nil
}

// ContributeProjectGold books accountID → "project:"+ID for min(cents, remaining §) via ONE balanced settlement
// event (conserving — the § sits in the project account, AuditLeague total stays 0), credits the contributor in
// By, and completes the project (granting the buff) if this meets the last requirement. Returns the updated
// project + the booked event (zero-value if nothing was due). ErrNotFound for an unknown project / non-member;
// ErrConflict if the project is not open or there's no § left to contribute.
func (m *Memory) ContributeProjectGold(leagueID, accountID, projectID string, cents int64) (Project, SettlementEvent, error) {
	m.mu.Lock()
	defer m.persistAfter()
	p, ok := m.projects[projectID]
	if !ok || p.LeagueID != leagueID {
		m.mu.Unlock()
		return Project{}, SettlementEvent{}, ErrNotFound
	}
	if !m.isMemberLocked(accountID, leagueID) {
		m.mu.Unlock()
		return Project{}, SettlementEvent{}, ErrNotFound
	}
	if p.Status != ProjectOpen {
		m.mu.Unlock()
		return Project{}, SettlementEvent{}, ErrConflict
	}
	if cents <= 0 {
		m.mu.Unlock()
		return Project{}, SettlementEvent{}, ErrConflict
	}
	rem := p.remainingGold()
	if rem <= 0 {
		m.mu.Unlock()
		return Project{}, SettlementEvent{}, ErrConflict // § requirement already met (or none)
	}
	if cents > rem {
		cents = rem // cap at the remaining § requirement
	}
	if p.By == nil {
		p.By = map[string]int64{}
	}
	// Conserving transfer: contributor → the project pseudo-counterparty. The § is spent on the Great Work and
	// stays in that account, so AuditLeague's total is unaffected.
	ev := m.appendEventLocked(leagueID, accountID, projectCounterparty(projectID), cents, "project:"+projectID)
	p.Gold += cents
	p.By[accountID] += contributionScore(0, cents)
	if p.isComplete() {
		m.completeProjectLocked(&p)
	}
	m.projects[projectID] = p
	m.mu.Unlock()
	return p, ev, nil
}

// completeProjectLocked marks a project completed and grants the lasting buff to every builder (everyone in By),
// then appends a "project-complete" chronicle line. Caller holds m.mu and writes p back afterward. The buff is an
// Effect carrying NO settlement event (the cash already moved at gold-contribution time; goods carry no cash) —
// it rides the existing /citystate effect path unchanged. Idempotent-by-caller: only invoked when isComplete()
// flips true, and it sets Status=completed so a later contribution can't re-grant.
func (m *Memory) completeProjectLocked(p *Project) {
	if p.Status == ProjectCompleted {
		return
	}
	p.Status = ProjectCompleted
	maxBy := ProjectMaxBuilderScore(*p)
	for builder := range p.By {
		m.grantProjectBuffLocked(*p, builder, maxBy)
		m.grantProjectTradeRewardLocked(*p, builder)
	}
	leagueName := p.Name
	if leagueName == "" {
		leagueName = "the Great Work"
	}
	builderCount := ProjectBuilderCount(*p)
	cityWord := "cities"
	if builderCount == 1 {
		cityWord = "city"
	}
	topBuilder, _ := ProjectTopBuilder(*p)
	m.appendChronicleLocked(ChronicleEntry{
		LeagueID: p.LeagueID, Kind: "project-complete",
		Text: "🏛️ " + leagueName + " is complete — built by " + strconv.Itoa(builderCount) + " " + cityWord +
			", led by " + m.displayNameLocked(topBuilder) + ".",
	})
}

// grantProjectBuffLocked creates the completed-megaproject Effect for one builder — NO settlement event (the buff
// is a side reward, not a cash transfer; booking one would break conservation by crediting against the market).
// The buff magnitude derives from BuffMagnitudeCents via InvestBuffMagnitude (the SAME cap/floor as the
// investment-office buff), and BuffDays becomes TicksRemaining. IssuerID is the project pseudo-id so the grant is
// self-describing in transparency views. Caller holds m.mu.
func (m *Memory) grantProjectBuffLocked(p Project, builder string, maxBy int64) {
	e := NewProjectBuffEffect(p, builder, maxBy, m.clock())
	if m.effects == nil {
		m.effects = map[string]Effect{}
	}
	m.effects[e.ID] = e
}

func (m *Memory) grantProjectTradeRewardLocked(p Project, builder string) {
	e, ok := NewProjectTradeRewardEffect(p, builder, m.clock())
	if !ok {
		return
	}
	if m.effects == nil {
		m.effects = map[string]Effect{}
	}
	m.effects[e.ID] = e
}
