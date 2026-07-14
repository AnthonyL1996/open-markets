// Package postgres is a Postgres-backed implementation of store.Store, a drop-in alternative to the in-memory
// store.Memory for production deployments. It is a DUAL STORE: the same store.Store interface, the SAME pure
// entity logic (Trade.NextTradeStatus / FreezeValues / InstallmentSchedule, Bond.Activate / RegisterMiss /
// ApplyGarnish / …, money.Amortize, store.ClampCityProfile / ImplausibleDelta / DownsampleHistory), only the
// persistence differs. Memory is the SEMANTIC REFERENCE — every observable behavior here matches it.
//
// Money-critical methods (settle/miss/garnish/grant/bailout/loan-accept/create-trade/set-status) each run in
// ONE pgx transaction: load the entity FOR UPDATE, run the pure logic, write the entity back, append the
// settlement event(s) with the monotonic seq, then COMMIT. Any error rolls back — no partial money state.
//
// EPHEMERAL state (the global price-shock map + the per-league index-history sparkline ring) is NOT persisted —
// it lives only on the PG struct in process memory, exactly as it does on Memory. lastActive (the runtime
// online signal) is likewise an in-memory map, not a table.
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"openmarkets/server/internal/duecycle"
	"openmarkets/server/internal/id"
	"openmarkets/server/internal/market"
	"openmarkets/server/internal/store"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// metaEpochKey / metaEventSeqKey are the meta-table rows backing the data epoch and the monotonic event seq.
const (
	metaEpochKey        = "epoch"
	metaEventSeqKey     = "event_seq"
	metaChronicleSeqKey = "chronicle_seq"
)

// opTimeout bounds a single store operation against a slow/unreachable DB (the store.Store methods take no
// context, so we synthesize one per call).
const opTimeout = 15 * time.Second

// PG is the Postgres-backed Store. It satisfies store.Store plus the concrete setup/market methods main.go,
// duecycle, and the pricer call (SetPricer/SetMarketParams/EventMultipliers/EffectiveIndices/AdvanceEvents/…).
type PG struct {
	pool *pgxpool.Pool

	// mu guards ONLY the in-process, non-DB fields below (market dynamics, knobs, clock, lastActive). DB state
	// is guarded by Postgres row locks (SELECT … FOR UPDATE) inside transactions, never by mu.
	mu          sync.RWMutex
	priceEvents map[string]market.EventState // EPHEMERAL global price-shock map (not persisted)
	indexHist   map[string][]float64         // EPHEMERAL per-"league|commodity" sparkline ring (not persisted)
	eventParams market.EventParams
	mktParams   market.Params
	commodities []string
	rng         *rand.Rand
	pricer      store.Pricer
	econ        store.EconParams
	lastActive  map[string]time.Time // RUNTIME online signal (not persisted), == Memory.lastActive
	now         func() time.Time
}

// indexHistoryLen mirrors the Memory store's sparkline ring length.
const indexHistoryLen = 16

// New opens a pool, runs the embedded migrations idempotently, and seeds the epoch + event-seq meta rows.
func New(ctx context.Context, dbURL string) (*PG, error) {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: open pool: %w", err)
	}
	p := &PG{
		pool:        pool,
		priceEvents: map[string]market.EventState{},
		indexHist:   map[string][]float64{},
		eventParams: market.DefaultEventParams(),
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
		econ:        store.DefaultEconParams(),
		lastActive:  map[string]time.Time{},
		now:         time.Now,
	}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := p.seedMeta(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

// Close releases the pool.
func (p *PG) Close() { p.pool.Close() }

// ctx returns a bounded context for one operation.
func (p *PG) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opTimeout)
}

func (p *PG) clock() time.Time {
	p.mu.RLock()
	now := p.now
	p.mu.RUnlock()
	if now != nil {
		return now().UTC()
	}
	return time.Now().UTC()
}

// migrate applies any unapplied embedded migration in version order, idempotently. The schema bodies use
// CREATE TABLE IF NOT EXISTS, and schema_migrations records which versions ran, so re-running is a no-op.
func (p *PG) migrate(ctx context.Context) error {
	// schema_migrations itself is created by 0001, but we need it to exist before checking — so create it first
	// (idempotent), matching the body in 0001.
	if _, err := p.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("postgres: ensure schema_migrations: %w", err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // 0001_, 0002_, … lexical == version order
	for i, name := range names {
		version := i + 1
		var applied bool
		if err := p.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := p.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("postgres: migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// seedMeta inserts the epoch (a fresh id on first run) and the event-seq counter (starting at 0) if absent.
// ON CONFLICT DO NOTHING keeps a restart from minting a new epoch (matching Memory.Open keeping its epoch).
func (p *PG) seedMeta(ctx context.Context) error {
	if _, err := p.pool.Exec(ctx,
		`INSERT INTO meta(key, value) VALUES($1, $2) ON CONFLICT (key) DO NOTHING`,
		metaEpochKey, id.New()); err != nil {
		return err
	}
	if _, err := p.pool.Exec(ctx,
		`INSERT INTO meta(key, value) VALUES($1, '0') ON CONFLICT (key) DO NOTHING`,
		metaEventSeqKey); err != nil {
		return err
	}
	// chronicle_seq: its OWN monotonic counter (distinct from event_seq), seeded to 0.
	if _, err := p.pool.Exec(ctx,
		`INSERT INTO meta(key, value) VALUES($1, '0') ON CONFLICT (key) DO NOTHING`,
		metaChronicleSeqKey); err != nil {
		return err
	}
	return nil
}

// nextSeqTx allocates the next monotonic settlement seq inside a transaction. It bumps the meta('event_seq')
// row UNDER A ROW LOCK (FOR UPDATE), so two concurrent booking txns serialize and never reuse a seq; a
// rolled-back txn never commits the bump, so seqs are gap-free per commit and strictly increasing — exactly
// Memory.eventSeq's contract.
func nextSeqTx(ctx context.Context, tx pgx.Tx) (int64, error) {
	var seq int64
	if err := tx.QueryRow(ctx,
		`SELECT value::bigint FROM meta WHERE key=$1 FOR UPDATE`, metaEventSeqKey).Scan(&seq); err != nil {
		return 0, err
	}
	seq++
	// value is a text column (read back via ::bigint); bind the seq as a STRING — pgx v5 won't encode an int64
	// into a text param ("cannot find encode plan"), which the conformance suite caught against real Postgres.
	if _, err := tx.Exec(ctx, `UPDATE meta SET value=$1 WHERE key=$2`, strconv.FormatInt(seq, 10), metaEventSeqKey); err != nil {
		return 0, err
	}
	return seq, nil
}

// appendEventTx writes one settlement event with the next monotonic seq inside tx, mirroring
// Memory.appendEventLocked (same field set, same seq semantics).
func (p *PG) appendEventTx(ctx context.Context, tx pgx.Tx, leagueID, payer, receiver string, cents int64, ref string) (store.SettlementEvent, error) {
	seq, err := nextSeqTx(ctx, tx)
	if err != nil {
		return store.SettlementEvent{}, err
	}
	ev := store.SettlementEvent{
		Seq: seq, LeagueID: leagueID, PayerID: payer, ReceiverID: receiver,
		Cents: cents, Ref: ref, Created: p.clock(),
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO settlement_events(seq, league_id, payer_id, receiver_id, cents, ref, created)
		 VALUES($1,$2,$3,$4,$5,$6,$7)`,
		ev.Seq, ev.LeagueID, ev.PayerID, ev.ReceiverID, ev.Cents, ev.Ref, ev.Created); err != nil {
		return store.SettlementEvent{}, err
	}
	return ev, nil
}

// nextChronicleSeqTx allocates the next monotonic chronicle seq inside a transaction, bumping the
// meta('chronicle_seq') row UNDER A ROW LOCK (FOR UPDATE) — its own counter, the exact pattern of nextSeqTx but
// for the chronicle. NOTE the pgx v5 int→text trap: value is a TEXT column, so the bumped seq is bound back as a
// STRING (strconv.FormatInt), never as an int64 (which pgx refuses to encode into a text param).
func nextChronicleSeqTx(ctx context.Context, tx pgx.Tx) (int64, error) {
	var seq int64
	if err := tx.QueryRow(ctx,
		`SELECT value::bigint FROM meta WHERE key=$1 FOR UPDATE`, metaChronicleSeqKey).Scan(&seq); err != nil {
		return 0, err
	}
	seq++
	if _, err := tx.Exec(ctx, `UPDATE meta SET value=$1 WHERE key=$2`,
		strconv.FormatInt(seq, 10), metaChronicleSeqKey); err != nil {
		return 0, err
	}
	return seq, nil
}

// appendChronicleTx writes one frozen chronicle entry with the next monotonic chronicle seq inside tx,
// mirroring Memory.appendChronicleLocked. Created is stamped from the clock here (the caller passes the rest).
func (p *PG) appendChronicleTx(ctx context.Context, tx pgx.Tx, e store.ChronicleEntry) (store.ChronicleEntry, error) {
	seq, err := nextChronicleSeqTx(ctx, tx)
	if err != nil {
		return store.ChronicleEntry{}, err
	}
	e.Seq = seq
	e.Created = p.clock()
	if _, err := tx.Exec(ctx,
		`INSERT INTO chronicle(seq, league_id, kind, actor_id, target_id, text, cents, created)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		e.Seq, e.LeagueID, e.Kind, e.ActorID, e.TargetID, e.Text, e.Cents, e.Created); err != nil {
		return store.ChronicleEntry{}, err
	}
	return e, nil
}

// displayNameTx resolves an account's player-facing name inside tx (DisplayName, short-id fallback), to FREEZE
// names into chronicle narration at append time. Mirrors Memory.displayNameLocked.
func displayNameTx(ctx context.Context, tx pgx.Tx, accountID string) string {
	var name string
	err := tx.QueryRow(ctx, `SELECT display_name FROM accounts WHERE id=$1`, accountID).Scan(&name)
	if err == nil && name != "" {
		return name
	}
	return shortChronicleID(accountID)
}

// shortChronicleID trims an opaque id to a short display fallback (mirrors store.shortChronID).
func shortChronicleID(idStr string) string {
	if len(idStr) > 6 {
		return idStr[:6]
	}
	if idStr == "" {
		return "someone"
	}
	return idStr
}

// bumpReliabilityTx records an on-time/missed installment for an account's reputation inside tx, mirroring
// Memory.bumpReliabilityLocked (silently no-ops for an unknown account).
func bumpReliabilityTx(ctx context.Context, tx pgx.Tx, accountID string, onTime bool) error {
	col := "missed_count"
	if onTime {
		col = "on_time_count"
	}
	_, err := tx.Exec(ctx, `UPDATE accounts SET `+col+` = `+col+` + 1 WHERE id=$1`, accountID)
	return err
}

// mapErr maps pgx's no-rows sentinel to store.ErrNotFound; other errors pass through.
func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}

// Epoch returns the persisted data epoch.
func (p *PG) Epoch() string {
	ctx, cancel := p.ctx()
	defer cancel()
	var epoch string
	if err := p.pool.QueryRow(ctx, `SELECT value FROM meta WHERE key=$1`, metaEpochKey).Scan(&epoch); err != nil {
		return ""
	}
	return epoch
}

// Flush is a no-op: Postgres persists on commit.
func (p *PG) Flush() error { return nil }

// Compile-time guarantees: PG implements the full Store interface AND the duecycle work-list/tick interface
// (so the due-clock drives either backend), matching the methods main.go's `backend` interface requires.
var (
	_ store.Store    = (*PG)(nil)
	_ duecycle.Store = (*PG)(nil)
)
