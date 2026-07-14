package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"openmarkets/server/internal/id"
	"openmarkets/server/internal/market"
	"openmarkets/server/internal/store"
)

// ── Accounts ──────────────────────────────────────────────────────────────────

func (p *PG) CreateAccount() (store.Account, string, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	secret := id.Secret()
	salt := id.Salt()
	a := store.Account{
		ID:         id.New(),
		Salt:       salt,
		SecretHash: id.Hash(salt, secret),
		Created:    p.clock(),
	}
	_, err := p.pool.Exec(ctx,
		`INSERT INTO accounts(id, salt, secret_hash, created, display_name, on_time_count, missed_count)
		 VALUES($1,$2,$3,$4,'',0,0)`,
		a.ID, a.Salt, a.SecretHash, a.Created)
	if err != nil {
		return store.Account{}, "", err
	}
	return a, secret, nil
}

func scanAccount(row interface{ Scan(...any) error }) (store.Account, error) {
	var a store.Account
	err := row.Scan(&a.ID, &a.Salt, &a.SecretHash, &a.Created, &a.DisplayName, &a.OnTimeCount, &a.MissedCount)
	return a, err
}

const accountCols = `id, salt, secret_hash, created, display_name, on_time_count, missed_count`

func (p *PG) GetAccount(idStr string) (store.Account, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	a, err := scanAccount(p.pool.QueryRow(ctx, `SELECT `+accountCols+` FROM accounts WHERE id=$1`, idStr))
	if err != nil {
		return store.Account{}, mapErr(err)
	}
	return a, nil
}

func (p *PG) SetAccountName(idStr, name string) (store.Account, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	a, err := scanAccount(p.pool.QueryRow(ctx,
		`UPDATE accounts SET display_name=$1 WHERE id=$2 RETURNING `+accountCols, name, idStr))
	if err != nil {
		return store.Account{}, mapErr(err)
	}
	return a, nil
}

func (p *PG) AllAccountIDs() []string {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx, `SELECT id FROM accounts ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return out
		}
		out = append(out, s)
	}
	return out
}

// ── Leagues / membership ──────────────────────────────────────────────────────

func scanLeague(row interface{ Scan(...any) error }) (store.League, error) {
	var l store.League
	err := row.Scan(&l.ID, &l.Name, &l.JoinCode, &l.OwnerID, &l.Created)
	return l, err
}

const leagueCols = `id, name, join_code, owner_id, created`

func (p *PG) CreateLeague(ownerID, name string) (store.League, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.League{}, err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1)`, ownerID).Scan(&exists); err != nil {
		return store.League{}, err
	}
	if !exists {
		return store.League{}, store.ErrNotFound
	}
	// Mint an unused join code (collisions are astronomically unlikely; loop to be safe like Memory).
	var code string
	for {
		code = id.Code()
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leagues WHERE join_code=$1)`, code).Scan(&taken); err != nil {
			return store.League{}, err
		}
		if !taken {
			break
		}
	}
	l := store.League{ID: id.New(), Name: name, JoinCode: code, OwnerID: ownerID, Created: p.clock()}
	if _, err := tx.Exec(ctx,
		`INSERT INTO leagues(id, name, join_code, owner_id, created) VALUES($1,$2,$3,$4,$5)`,
		l.ID, l.Name, l.JoinCode, l.OwnerID, l.Created); err != nil {
		return store.League{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO members(league_id, account_id) VALUES($1,$2)`, l.ID, ownerID); err != nil {
		return store.League{}, err
	}
	// Chronicle: the league's first line (within the same txn; names frozen now).
	leagueName := l.Name
	if leagueName == "" {
		leagueName = "the league"
	}
	if _, err := p.appendChronicleTx(ctx, tx, store.ChronicleEntry{
		LeagueID: l.ID, Kind: "founded", ActorID: ownerID,
		Text: displayNameTx(ctx, tx, ownerID) + " founded " + leagueName + ".",
	}); err != nil {
		return store.League{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.League{}, err
	}
	return l, nil
}

func (p *PG) LeagueByJoinCode(code string) (store.League, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	l, err := scanLeague(p.pool.QueryRow(ctx, `SELECT `+leagueCols+` FROM leagues WHERE join_code=$1`, code))
	if err != nil {
		return store.League{}, mapErr(err)
	}
	return l, nil
}

func (p *PG) GetLeague(idStr string) (store.League, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	l, err := scanLeague(p.pool.QueryRow(ctx, `SELECT `+leagueCols+` FROM leagues WHERE id=$1`, idStr))
	if err != nil {
		return store.League{}, mapErr(err)
	}
	return l, nil
}

func (p *PG) JoinLeague(accountID, leagueID string) error {
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var aExists, lExists, member bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1)`, accountID).Scan(&aExists); err != nil {
		return err
	}
	if !aExists {
		return store.ErrNotFound
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leagues WHERE id=$1)`, leagueID).Scan(&lExists); err != nil {
		return err
	}
	if !lExists {
		return store.ErrNotFound
	}
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM members WHERE league_id=$1 AND account_id=$2)`, leagueID, accountID).Scan(&member); err != nil {
		return err
	}
	if member {
		return store.ErrAlreadyMember
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO members(league_id, account_id) VALUES($1,$2)`, leagueID, accountID); err != nil {
		return err
	}
	// Chronicle: a new member joins (within the same txn; name frozen now).
	if _, err := p.appendChronicleTx(ctx, tx, store.ChronicleEntry{
		LeagueID: leagueID, Kind: "joined", ActorID: accountID,
		Text: displayNameTx(ctx, tx, accountID) + " joined the league.",
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *PG) IsMember(accountID, leagueID string) bool {
	ctx, cancel := p.ctx()
	defer cancel()
	var member bool
	if err := p.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM members WHERE league_id=$1 AND account_id=$2)`, leagueID, accountID).Scan(&member); err != nil {
		return false
	}
	return member
}

func (p *PG) LeagueMembers(leagueID string) ([]string, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leagues WHERE id=$1)`, leagueID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, store.ErrNotFound
	}
	rows, err := p.pool.Query(ctx, `SELECT account_id FROM members WHERE league_id=$1 ORDER BY account_id`, leagueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *PG) LeaguesForAccount(accountID string) ([]store.League, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT l.id, l.name, l.join_code, l.owner_id, l.created
		   FROM leagues l JOIN members m ON m.league_id = l.id
		  WHERE m.account_id=$1 ORDER BY l.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]store.League, 0)
	for rows.Next() {
		l, err := scanLeague(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// AllLeagues returns every league known to the store, sorted by id (operator listing).
func (p *PG) AllLeagues() []store.League {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx, `SELECT `+leagueCols+` FROM leagues ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]store.League, 0)
	for rows.Next() {
		l, err := scanLeague(rows)
		if err != nil {
			return out
		}
		out = append(out, l)
	}
	return out
}

// AdminStats returns aggregate operator counts. Each count is a cheap aggregate query; no entity bodies are
// loaded. A status with no rows is simply absent from the by-status maps (mirrors Memory).
func (p *PG) AdminStats() store.Stats {
	ctx, cancel := p.ctx()
	defer cancel()
	s := store.Stats{TradesByStatus: map[string]int{}, BondsByStatus: map[string]int{}}
	_ = p.pool.QueryRow(ctx, `SELECT count(*) FROM accounts`).Scan(&s.Accounts)
	_ = p.pool.QueryRow(ctx, `SELECT count(*) FROM leagues`).Scan(&s.Leagues)
	_ = p.pool.QueryRow(ctx, `SELECT count(*) FROM members`).Scan(&s.Members)
	_ = p.pool.QueryRow(ctx, `SELECT count(*) FROM city_profiles WHERE (data->>'suspect')::boolean`).Scan(&s.SuspectCityProfiles)
	_ = p.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(abs(cents)),0), count(*) FROM settlement_events`).Scan(&s.SettlementVolumeCents, &s.SettlementEventCount)
	if rows, err := p.pool.Query(ctx, `SELECT status, count(*) FROM trades GROUP BY status`); err == nil {
		for rows.Next() {
			var st string
			var n int
			if rows.Scan(&st, &n) == nil {
				s.TradesByStatus[st] = n
			}
		}
		rows.Close()
	}
	if rows, err := p.pool.Query(ctx, `SELECT status, count(*) FROM bonds GROUP BY status`); err == nil {
		for rows.Next() {
			var st string
			var n int
			if rows.Scan(&st, &n) == nil {
				s.BondsByStatus[st] = n
			}
		}
		rows.Close()
	}
	return s
}

// DeleteLeague removes a league and cascades all of its league-scoped state in ONE transaction (members,
// reports, trades, bonds, effects, settlement events, projects, then the league row). ErrNotFound for an unknown league.
func (p *PG) DeleteLeague(leagueID string) error {
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leagues WHERE id=$1 FOR UPDATE)`, leagueID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return store.ErrNotFound
	}
	for _, q := range []string{
		`DELETE FROM members WHERE league_id=$1`,
		`DELETE FROM reports WHERE league_id=$1`,
		`DELETE FROM trades WHERE league_id=$1`,
		`DELETE FROM bonds WHERE league_id=$1`,
		`DELETE FROM effects WHERE league_id=$1`,
		`DELETE FROM settlement_events WHERE league_id=$1`,
		`DELETE FROM projects WHERE league_id=$1`,
		`DELETE FROM leagues WHERE id=$1`,
	} {
		if _, err := tx.Exec(ctx, q, leagueID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// RemoveMember drops a single membership (v1: the membership row only; the account + its settled history stay).
// ErrNotFound if the league is unknown or the account isn't a member.
func (p *PG) RemoveMember(accountID, leagueID string) error {
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leagues WHERE id=$1)`, leagueID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return store.ErrNotFound
	}
	tag, err := tx.Exec(ctx, `DELETE FROM members WHERE league_id=$1 AND account_id=$2`, leagueID, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return tx.Commit(ctx)
}

// ── Reports ───────────────────────────────────────────────────────────────────

func (p *PG) PutReport(r store.Report) error {
	ctx, cancel := p.ctx()
	defer cancel()
	if r.TS.IsZero() {
		r.TS = p.clock()
	}
	var member bool
	if err := p.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM members WHERE league_id=$1 AND account_id=$2)`, r.LeagueID, r.AccountID).Scan(&member); err != nil {
		return err
	}
	if !member {
		return store.ErrNotFound
	}
	_, err := p.pool.Exec(ctx,
		`INSERT INTO reports(account_id, league_id, commodity, net_supply, ts)
		 VALUES($1,$2,$3,$4,$5)
		 ON CONFLICT (account_id, league_id, commodity)
		 DO UPDATE SET net_supply=EXCLUDED.net_supply, ts=EXCLUDED.ts`,
		r.AccountID, r.LeagueID, r.Commodity, r.NetSupply, r.TS)
	return err
}

func (p *PG) LeagueReports(leagueID string) ([]market.Report, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leagues WHERE id=$1)`, leagueID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, store.ErrNotFound
	}
	rows, err := p.pool.Query(ctx, `SELECT account_id, commodity, net_supply FROM reports WHERE league_id=$1`, leagueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []market.Report
	for rows.Next() {
		var r market.Report
		if err := rows.Scan(&r.AccountID, &r.Commodity, &r.NetSupply); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *PG) MarketMoverByAccount(leagueID string) (map[string]float64, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leagues WHERE id=$1)`, leagueID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, store.ErrNotFound
	}
	rows, err := p.pool.Query(ctx,
		`SELECT account_id, SUM(abs(net_supply)) FROM reports WHERE league_id=$1 GROUP BY account_id`, leagueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var aid string
		var v float64
		if err := rows.Scan(&aid, &v); err != nil {
			return nil, err
		}
		out[aid] = v
	}
	return out, rows.Err()
}

// isMemberTx is the transactional membership probe used by the booking paths (matches isMemberLocked).
func isMemberTx(ctx context.Context, tx pgx.Tx, accountID, leagueID string) (bool, error) {
	var member bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM members WHERE league_id=$1 AND account_id=$2)`,
		leagueID, accountID).Scan(&member)
	return member, err
}
