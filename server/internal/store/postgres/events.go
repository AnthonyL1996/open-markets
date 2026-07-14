package postgres

import (
	"time"

	"openmarkets/server/internal/store"
)

func scanEvent(row interface{ Scan(...any) error }) (store.SettlementEvent, error) {
	var e store.SettlementEvent
	err := row.Scan(&e.Seq, &e.LeagueID, &e.PayerID, &e.ReceiverID, &e.Cents, &e.Ref, &e.Created)
	return e, err
}

const eventCols = `seq, league_id, payer_id, receiver_id, cents, ref, created`

// SettlementsForAccount returns the caller's own events after `since`, plus the caller's highest seq overall.
func (p *PG) SettlementsForAccount(leagueID, accountID string, since int64) (events []store.SettlementEvent, latestSeq int64, err error) {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, qerr := p.pool.Query(ctx,
		`SELECT `+eventCols+` FROM settlement_events
		  WHERE league_id=$1 AND (payer_id=$2 OR receiver_id=$2)
		  ORDER BY seq ASC`,
		leagueID, accountID)
	if qerr != nil {
		return nil, 0, qerr
	}
	defer rows.Close()
	for rows.Next() {
		e, serr := scanEvent(rows)
		if serr != nil {
			return nil, 0, serr
		}
		if e.Seq > latestSeq {
			latestSeq = e.Seq
		}
		if e.Seq > since {
			events = append(events, e)
		}
	}
	return events, latestSeq, rows.Err()
}

// SettlementsSince returns the league's settlement events with Seq>sinceSeq, ascending by Seq, capped at limit
// (limit<=0 → a sane default). League-wide — the activity-feed reader. Mirrors Memory.SettlementsSince.
func (p *PG) SettlementsSince(leagueID string, sinceSeq int64, limit int) []store.SettlementEvent {
	if limit <= 0 {
		limit = 200
	}
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT `+eventCols+` FROM settlement_events
		  WHERE league_id=$1 AND seq>$2 ORDER BY seq ASC LIMIT $3`,
		leagueID, sinceSeq, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]store.SettlementEvent, 0, limit)
	for rows.Next() {
		e, serr := scanEvent(rows)
		if serr != nil {
			return out
		}
		out = append(out, e)
	}
	return out
}

// AuditLeague re-derives per-account net online cash + the conservation total (must be 0).
func (p *PG) AuditLeague(leagueID string) (net map[string]int64, total int64, err error) {
	ctx, cancel := p.ctx()
	defer cancel()
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leagues WHERE id=$1)`, leagueID).Scan(&exists); err != nil {
		return nil, 0, err
	}
	if !exists {
		return nil, 0, store.ErrNotFound
	}
	rows, qerr := p.pool.Query(ctx,
		`SELECT payer_id, receiver_id, cents FROM settlement_events WHERE league_id=$1`, leagueID)
	if qerr != nil {
		return nil, 0, qerr
	}
	defer rows.Close()
	net = map[string]int64{}
	for rows.Next() {
		var payer, receiver string
		var cents int64
		if err := rows.Scan(&payer, &receiver, &cents); err != nil {
			return nil, 0, err
		}
		net[receiver] += cents
		net[payer] -= cents
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	for _, v := range net {
		total += v
	}
	return net, total, nil
}

// NetCentsSeries derives the account's cumulative net-§ curve from the league's events (Seq order), then
// downsamples it identically to Memory via the exported store.DownsampleNetSeries.
func (p *PG) NetCentsSeries(leagueID, accountID string) []store.NetPoint {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT payer_id, receiver_id, cents, created FROM settlement_events
		  WHERE league_id=$1 ORDER BY seq ASC`, leagueID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []store.NetPoint
	var running int64
	for rows.Next() {
		var payer, receiver string
		var cents int64
		var created time.Time
		if err := rows.Scan(&payer, &receiver, &cents, &created); err != nil {
			return out
		}
		touched := false
		if receiver == accountID {
			running += cents
			touched = true
		}
		if payer == accountID {
			running -= cents
			touched = true
		}
		if touched {
			out = append(out, store.NetPoint{TS: created, Cents: running})
		}
	}
	return store.DownsampleNetSeries(out)
}
