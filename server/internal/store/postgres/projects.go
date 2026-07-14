package postgres

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jackc/pgx/v5"

	"openmarkets/server/internal/id"
	"openmarkets/server/internal/store"
)

// projects.go is the Postgres backing for the co-op MEGAPROJECTS (Great Works, social slice 4). It mirrors
// store.project.go's Memory semantics EXACTLY: the contribution caps (remaining requirement), the conserving
// § transfer (member → "project:"+id), the goods-have-no-event rule, and completion granting each builder an
// Effect with NO settlement event. Reqs/Goods/By are jsonb columns round-tripped through the Go maps/slices.

const projectCols = `id, league_id, name, description, reqs, gold_req_cents, goods, gold, by_score,
	buff_kind, buff_magnitude_cents, buff_days, trade_reward_kind, trade_reward_commodity, trade_reward_pct_bips,
	status, created`

// scanProject reads a project row, decoding the jsonb reqs/goods/by columns into the Go fields.
func scanProject(row interface{ Scan(...any) error }) (store.Project, error) {
	var p store.Project
	var reqsJSON, goodsJSON, byJSON []byte
	if err := row.Scan(&p.ID, &p.LeagueID, &p.Name, &p.Description, &reqsJSON, &p.GoldReqCents,
		&goodsJSON, &p.Gold, &byJSON, &p.BuffKind, &p.BuffMagnitudeCents, &p.BuffDays,
		&p.TradeRewardKind, &p.TradeRewardCommodity, &p.TradeRewardPctBips, &p.Status, &p.Created); err != nil {
		return store.Project{}, err
	}
	if len(reqsJSON) > 0 {
		if err := json.Unmarshal(reqsJSON, &p.Reqs); err != nil {
			return store.Project{}, err
		}
	}
	if len(goodsJSON) > 0 {
		if err := json.Unmarshal(goodsJSON, &p.Goods); err != nil {
			return store.Project{}, err
		}
	}
	if len(byJSON) > 0 {
		if err := json.Unmarshal(byJSON, &p.By); err != nil {
			return store.Project{}, err
		}
	}
	if p.Goods == nil {
		p.Goods = map[string]int64{}
	}
	if p.By == nil {
		p.By = map[string]int64{}
	}
	return p, nil
}

// writeProjectTx upserts a project row inside tx (marshaling the jsonb columns). Used by CreateProject and the
// contribution paths (the contribution UPDATEs the whole row, which is simplest + cheap at friend scale).
func writeProjectTx(ctx context.Context, tx pgx.Tx, p store.Project) error {
	reqsJSON, err := json.Marshal(p.Reqs)
	if err != nil {
		return err
	}
	if p.Goods == nil {
		p.Goods = map[string]int64{}
	}
	if p.By == nil {
		p.By = map[string]int64{}
	}
	goodsJSON, err := json.Marshal(p.Goods)
	if err != nil {
		return err
	}
	byJSON, err := json.Marshal(p.By)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO projects(id, league_id, name, description, reqs, gold_req_cents, goods, gold, by_score,
			buff_kind, buff_magnitude_cents, buff_days, trade_reward_kind, trade_reward_commodity, trade_reward_pct_bips,
			status, created)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		 ON CONFLICT (id) DO UPDATE SET
			goods=EXCLUDED.goods, gold=EXCLUDED.gold, by_score=EXCLUDED.by_score, status=EXCLUDED.status`,
		p.ID, p.LeagueID, p.Name, p.Description, reqsJSON, p.GoldReqCents, goodsJSON, p.Gold, byJSON,
		p.BuffKind, p.BuffMagnitudeCents, p.BuffDays, p.TradeRewardKind, p.TradeRewardCommodity,
		p.TradeRewardPctBips, p.Status, p.Created)
	return err
}

// CreateProject stores a new open project (assigns id, status=open, zeroes totals). The league must exist.
func (p *PG) CreateProject(pr store.Project) (store.Project, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Project{}, err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leagues WHERE id=$1)`, pr.LeagueID).Scan(&exists); err != nil {
		return store.Project{}, err
	}
	if !exists {
		return store.Project{}, store.ErrNotFound
	}
	pr.ID = id.New()
	pr.Status = store.ProjectOpen
	pr.Goods = map[string]int64{}
	pr.Gold = 0
	pr.By = map[string]int64{}
	pr.Created = p.clock()
	if err := writeProjectTx(ctx, tx, pr); err != nil {
		return store.Project{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Project{}, err
	}
	return pr, nil
}

// GetProject returns a project by id (ErrNotFound if unknown).
func (p *PG) GetProject(idStr string) (store.Project, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	pr, err := scanProject(p.pool.QueryRow(ctx, `SELECT `+projectCols+` FROM projects WHERE id=$1`, idStr))
	if err != nil {
		return store.Project{}, mapErr(err)
	}
	return pr, nil
}

// ProjectsFor lists a league's projects (open + completed), newest first.
func (p *PG) ProjectsFor(leagueID string) []store.Project {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT `+projectCols+` FROM projects WHERE league_id=$1 ORDER BY created DESC`, leagueID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []store.Project
	for rows.Next() {
		pr, serr := scanProject(rows)
		if serr != nil {
			return out
		}
		out = append(out, pr)
	}
	return out
}

// CompletedProjectCountsByBuilder returns, per account, how many completed Great Works they helped build.
func (p *PG) CompletedProjectCountsByBuilder(leagueID string) map[string]int64 {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT by_score FROM projects WHERE league_id=$1 AND status=$2`, leagueID, store.ProjectCompleted)
	if err != nil {
		return map[string]int64{}
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var byJSON []byte
		if err := rows.Scan(&byJSON); err != nil {
			return out
		}
		var by map[string]int64
		if len(byJSON) > 0 {
			if err := json.Unmarshal(byJSON, &by); err != nil {
				return out
			}
		}
		for builder, score := range by {
			if score > 0 {
				out[builder]++
			}
		}
	}
	return out
}

// LeaguesWithoutOpenProject returns leagues with no open project (the generator's work list).
func (p *PG) LeaguesWithoutOpenProject() []string {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT l.id FROM leagues l
		  WHERE NOT EXISTS(SELECT 1 FROM projects pr WHERE pr.league_id=l.id AND pr.status=$1)
		  ORDER BY l.id`, store.ProjectOpen)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var lid string
		if err := rows.Scan(&lid); err != nil {
			return out
		}
		out = append(out, lid)
	}
	return out
}

// loadProjectForUpdateTx loads + row-locks a project inside tx (FOR UPDATE), returning ErrNotFound if it's unknown
// or not in the given league. The lock serializes concurrent contributions so completion fires exactly once.
func loadProjectForUpdateTx(ctx context.Context, tx pgx.Tx, projectID, leagueID string) (store.Project, error) {
	pr, err := scanProject(tx.QueryRow(ctx, `SELECT `+projectCols+` FROM projects WHERE id=$1 FOR UPDATE`, projectID))
	if err != nil {
		return store.Project{}, mapErr(err)
	}
	if pr.LeagueID != leagueID {
		return store.Project{}, store.ErrNotFound
	}
	return pr, nil
}

// ContributeProjectGoods adds capped commodity units to an open project (NO settlement event), credits the
// builder, and completes the project (granting the buff) when the last requirement is met — all in ONE txn.
func (p *PG) ContributeProjectGoods(leagueID, accountID, projectID, commodity string, qty int64) (store.Project, int64, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Project{}, 0, err
	}
	defer tx.Rollback(ctx)

	ok, err := isMemberTx(ctx, tx, accountID, leagueID)
	if err != nil {
		return store.Project{}, 0, err
	}
	if !ok {
		return store.Project{}, 0, store.ErrNotFound
	}
	pr, err := loadProjectForUpdateTx(ctx, tx, projectID, leagueID)
	if err != nil {
		return store.Project{}, 0, err
	}
	if pr.Status != store.ProjectOpen || qty <= 0 {
		return store.Project{}, 0, store.ErrConflict
	}
	rem := store.ProjectRemainingGoods(pr, commodity)
	if rem <= 0 {
		return store.Project{}, 0, store.ErrConflict
	}
	if qty > rem {
		qty = rem
	}
	pr.Goods[commodity] += qty
	pr.By[accountID] += store.ProjectContributionScore(qty, 0)
	if store.ProjectIsComplete(pr) {
		if err := p.completeProjectTx(ctx, tx, &pr); err != nil {
			return store.Project{}, 0, err
		}
	}
	if err := writeProjectTx(ctx, tx, pr); err != nil {
		return store.Project{}, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Project{}, 0, err
	}
	return pr, qty, nil
}

// ContributeProjectGold books accountID → "project:"+id for min(cents, remaining §) via ONE balanced settlement
// event (conserving), credits the builder, and completes the project (granting the buff) when the last
// requirement is met — all in ONE txn.
func (p *PG) ContributeProjectGold(leagueID, accountID, projectID string, cents int64) (store.Project, store.SettlementEvent, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.Project{}, store.SettlementEvent{}, err
	}
	defer tx.Rollback(ctx)

	ok, err := isMemberTx(ctx, tx, accountID, leagueID)
	if err != nil {
		return store.Project{}, store.SettlementEvent{}, err
	}
	if !ok {
		return store.Project{}, store.SettlementEvent{}, store.ErrNotFound
	}
	pr, err := loadProjectForUpdateTx(ctx, tx, projectID, leagueID)
	if err != nil {
		return store.Project{}, store.SettlementEvent{}, err
	}
	if pr.Status != store.ProjectOpen || cents <= 0 {
		return store.Project{}, store.SettlementEvent{}, store.ErrConflict
	}
	rem := store.ProjectRemainingGold(pr)
	if rem <= 0 {
		return store.Project{}, store.SettlementEvent{}, store.ErrConflict
	}
	if cents > rem {
		cents = rem
	}
	// Conserving transfer: contributor → the project pseudo-counterparty (the § is spent on the Great Work).
	ev, err := p.appendEventTx(ctx, tx, leagueID, accountID, store.ProjectCounterparty(projectID), cents, "project:"+projectID)
	if err != nil {
		return store.Project{}, store.SettlementEvent{}, err
	}
	pr.Gold += cents
	pr.By[accountID] += store.ProjectContributionScore(0, cents)
	if store.ProjectIsComplete(pr) {
		if err := p.completeProjectTx(ctx, tx, &pr); err != nil {
			return store.Project{}, store.SettlementEvent{}, err
		}
	}
	if err := writeProjectTx(ctx, tx, pr); err != nil {
		return store.Project{}, store.SettlementEvent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Project{}, store.SettlementEvent{}, err
	}
	return pr, ev, nil
}

// completeProjectTx marks a project completed, grants each builder the lasting buff-effect (NO settlement event),
// and appends the "project-complete" chronicle line — all inside tx. Mirrors Memory.completeProjectLocked.
func (p *PG) completeProjectTx(ctx context.Context, tx pgx.Tx, pr *store.Project) error {
	if pr.Status == store.ProjectCompleted {
		return nil
	}
	pr.Status = store.ProjectCompleted
	maxBy := store.ProjectMaxBuilderScore(*pr)
	for builder := range pr.By {
		e := store.NewProjectBuffEffect(*pr, builder, maxBy, p.clock())
		if err := writeEffectTx(ctx, tx, e); err != nil {
			return err
		}
		if tr, ok := store.NewProjectTradeRewardEffect(*pr, builder, p.clock()); ok {
			if err := writeEffectTx(ctx, tx, tr); err != nil {
				return err
			}
		}
	}
	leagueName := pr.Name
	if leagueName == "" {
		leagueName = "the Great Work"
	}
	builderCount := store.ProjectBuilderCount(*pr)
	cityWord := "cities"
	if builderCount == 1 {
		cityWord = "city"
	}
	topBuilder, _ := store.ProjectTopBuilder(*pr)
	if _, err := p.appendChronicleTx(ctx, tx, store.ChronicleEntry{
		LeagueID: pr.LeagueID, Kind: "project-complete",
		Text: "🏛️ " + leagueName + " is complete — built by " + strconv.Itoa(builderCount) + " " + cityWord +
			", led by " + displayNameTx(ctx, tx, topBuilder) + ".",
	}); err != nil {
		return err
	}
	return nil
}
