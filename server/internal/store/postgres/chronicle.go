package postgres

import (
	"time"

	"openmarkets/server/internal/store"
)

const chronicleCols = `seq, league_id, kind, actor_id, target_id, text, cents, created`

func scanChronicle(row interface{ Scan(...any) error }) (store.ChronicleEntry, error) {
	var e store.ChronicleEntry
	err := row.Scan(&e.Seq, &e.LeagueID, &e.Kind, &e.ActorID, &e.TargetID, &e.Text, &e.Cents, &e.Created)
	return e, err
}

// AppendChronicle assigns the next monotonic chronicle seq (meta('chronicle_seq'), FOR UPDATE), stamps Created,
// and writes the frozen narration entry — all in ONE transaction. Mirrors Memory.AppendChronicle.
func (p *PG) AppendChronicle(e store.ChronicleEntry) (store.ChronicleEntry, error) {
	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return store.ChronicleEntry{}, err
	}
	defer tx.Rollback(ctx)
	out, err := p.appendChronicleTx(ctx, tx, e)
	if err != nil {
		return store.ChronicleEntry{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.ChronicleEntry{}, err
	}
	return out, nil
}

// Chronicle returns the league's chronicle entries with Seq>sinceSeq, ascending, capped at limit (limit<=0 →
// 200). Mirrors Memory.Chronicle.
func (p *PG) Chronicle(leagueID string, sinceSeq int64, limit int) []store.ChronicleEntry {
	if limit <= 0 {
		limit = 200
	}
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT `+chronicleCols+` FROM chronicle
		  WHERE league_id=$1 AND seq>$2 ORDER BY seq ASC LIMIT $3`,
		leagueID, sinceSeq, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]store.ChronicleEntry, 0, limit)
	for rows.Next() {
		e, serr := scanChronicle(rows)
		if serr != nil {
			return out
		}
		out = append(out, e)
	}
	return out
}

// ChronicleOnThisDay returns the league's prior-day chronicle entries whose (month, day) match now's, ascending
// by Seq. Filters by EXTRACT(MONTH/DAY) and requires created < now AND not today's date. Mirrors
// Memory.ChronicleOnThisDay.
func (p *PG) ChronicleOnThisDay(leagueID string, now time.Time) []store.ChronicleEntry {
	now = now.UTC()
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT `+chronicleCols+` FROM chronicle
		  WHERE league_id=$1
		    AND EXTRACT(MONTH FROM created AT TIME ZONE 'UTC') = $2
		    AND EXTRACT(DAY   FROM created AT TIME ZONE 'UTC') = $3
		    AND created < $4
		    AND (created AT TIME ZONE 'UTC')::date <> ($4::timestamptz AT TIME ZONE 'UTC')::date
		  ORDER BY seq ASC`,
		leagueID, int(now.Month()), now.Day(), now)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []store.ChronicleEntry
	for rows.Next() {
		e, serr := scanChronicle(rows)
		if serr != nil {
			return out
		}
		out = append(out, e)
	}
	return out
}
