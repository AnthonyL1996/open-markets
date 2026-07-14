package postgres

import (
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"openmarkets/server/internal/store"
)

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// PutCityProfile stamps ReportedAt + Reliability server-side, applies the IDENTICAL clamp + Suspect-flag logic
// as Memory (via the exported store.ClampCityProfile / store.ImplausibleDelta), then replaces the latest
// snapshot AND rewrites the downsampled history (via store.DownsampleHistory) — all in ONE transaction so a
// concurrent put can't interleave a half-rewritten history.
func (p *PG) PutCityProfile(prof store.CityProfile) error {
	prof.ReportedAt = p.clock()
	store.ClampCityProfile(&prof)

	ctx, cancel := p.ctx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Suspect flag: compare against the PREVIOUS stored snapshot (locked for the duration of the rewrite).
	var prevData []byte
	err = tx.QueryRow(ctx, `SELECT data FROM city_profiles WHERE account_id=$1 FOR UPDATE`, prof.AccountID).Scan(&prevData)
	hasPrev := err == nil
	if err != nil && !isNoRows(err) {
		return err // a real error (not a simple miss)
	}
	if hasPrev {
		var prev store.CityProfile
		if err := json.Unmarshal(prevData, &prev); err == nil && store.ImplausibleDelta(prev, prof) {
			prof.Suspect = true
		}
	}

	// Reliability from the account's current on-time reputation (100 if unknown).
	rel := 100
	var onTime, missed int
	if err := tx.QueryRow(ctx, `SELECT on_time_count, missed_count FROM accounts WHERE id=$1`, prof.AccountID).Scan(&onTime, &missed); err == nil {
		a := store.Account{OnTimeCount: onTime, MissedCount: missed}
		rel = a.Reliability()
	}
	prof.Reliability = rel

	data, err := json.Marshal(prof)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO city_profiles(account_id, reported_at, data) VALUES($1,$2,$3)
		 ON CONFLICT (account_id) DO UPDATE SET reported_at=EXCLUDED.reported_at, data=EXCLUDED.data`,
		prof.AccountID, prof.ReportedAt, data); err != nil {
		return err
	}

	// Load the full history (oldest→newest), append, downsample, rewrite. The history is bounded (<=150) so a
	// full read+rewrite per put is cheap and keeps the downsample EXACTLY Memory's (one removal per put).
	rows, err := tx.Query(ctx,
		`SELECT data FROM city_profile_history WHERE account_id=$1 ORDER BY ordinal ASC`, prof.AccountID)
	if err != nil {
		return err
	}
	var hist []store.CityProfile
	for rows.Next() {
		var d []byte
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return err
		}
		var cp store.CityProfile
		if err := json.Unmarshal(d, &cp); err != nil {
			rows.Close()
			return err
		}
		hist = append(hist, cp)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	hist = append(hist, prof)
	hist = store.DownsampleHistory(hist)

	// Rewrite the history rows for this account (delete-all + re-insert with fresh contiguous ordinals). Cheap at
	// <=150 rows; gives a deterministic ordinal that preserves oldest→newest order on read.
	if _, err := tx.Exec(ctx, `DELETE FROM city_profile_history WHERE account_id=$1`, prof.AccountID); err != nil {
		return err
	}
	for i, cp := range hist {
		d, err := json.Marshal(cp)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO city_profile_history(account_id, ordinal, reported_at, data) VALUES($1,$2,$3,$4)`,
			prof.AccountID, int64(i), cp.ReportedAt, d); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (p *PG) CityProfileOf(accountID string) (store.CityProfile, bool) {
	ctx, cancel := p.ctx()
	defer cancel()
	var data []byte
	if err := p.pool.QueryRow(ctx, `SELECT data FROM city_profiles WHERE account_id=$1`, accountID).Scan(&data); err != nil {
		return store.CityProfile{}, false
	}
	var cp store.CityProfile
	if err := json.Unmarshal(data, &cp); err != nil {
		return store.CityProfile{}, false
	}
	return cp, true
}

func (p *PG) CityProfileHistory(accountID string) []store.CityProfile {
	ctx, cancel := p.ctx()
	defer cancel()
	rows, err := p.pool.Query(ctx,
		`SELECT data FROM city_profile_history WHERE account_id=$1 ORDER BY ordinal ASC`, accountID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]store.CityProfile, 0)
	for rows.Next() {
		var d []byte
		if err := rows.Scan(&d); err != nil {
			return out
		}
		var cp store.CityProfile
		if err := json.Unmarshal(d, &cp); err != nil {
			return out
		}
		out = append(out, cp)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
