package postgres

import "context"

// resetSchema truncates every entity table and reseeds the meta counters, giving each test run an isolated,
// empty store on a shared database (an alternative to creating a throwaway schema per run).
func resetSchema(ctx context.Context, pg *PG) error {
	_, err := pg.pool.Exec(ctx, `TRUNCATE
		accounts, leagues, members, reports, city_profiles, city_profile_history,
		trades, bonds, effects, settlement_events, chronicle, projects`)
	if err != nil {
		return err
	}
	// Reset the monotonic event + chronicle seqs to 0 so the run starts from a known state.
	if _, err := pg.pool.Exec(ctx, `UPDATE meta SET value='0' WHERE key=$1`, metaEventSeqKey); err != nil {
		return err
	}
	if _, err := pg.pool.Exec(ctx, `UPDATE meta SET value='0' WHERE key=$1`, metaChronicleSeqKey); err != nil {
		return err
	}
	return nil
}
