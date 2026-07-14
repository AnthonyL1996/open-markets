-- Open Markets — Postgres schema (0001_init).
--
-- This is the durable mirror of the in-memory store.Memory reference. Every entity that Memory keeps in a
-- map (and persists in its JSON snapshot) gets a table here; the EPHEMERAL market-dynamics state
-- (priceEvents / indexHist) is intentionally NOT persisted — it lives only in process memory on the PG
-- struct, exactly as it does on Memory (a restart clears it and it regenerates).
--
-- Money-conservation note: settlement_events.seq is a single monotonic counter shared across all leagues
-- (matching Memory.eventSeq). It is allocated from the meta('event_seq') row, bumped FOR UPDATE inside the
-- same booking transaction that writes the event — strictly increasing, never reused, even across rollbacks
-- (a rolled-back txn never commits the bump). Every settlement-booking method runs in ONE transaction.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    integer PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

-- Single-row-per-key bag for process-global scalars: the data epoch and the monotonic event sequence.
CREATE TABLE IF NOT EXISTS meta (
    key   text PRIMARY KEY,
    value text NOT NULL
);

CREATE TABLE IF NOT EXISTS accounts (
    id           text PRIMARY KEY,
    salt         text NOT NULL,
    secret_hash  text NOT NULL,
    created      timestamptz NOT NULL,
    display_name text NOT NULL DEFAULT '',
    on_time_count integer NOT NULL DEFAULT 0,
    missed_count  integer NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS leagues (
    id        text PRIMARY KEY,
    name      text NOT NULL,
    join_code text NOT NULL UNIQUE,
    owner_id  text NOT NULL,
    created   timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS members (
    league_id  text NOT NULL,
    account_id text NOT NULL,
    PRIMARY KEY (league_id, account_id)
);
CREATE INDEX IF NOT EXISTS members_account_idx ON members (account_id);

-- One row per (account, league, commodity); the latest report (upsert replaces).
CREATE TABLE IF NOT EXISTS reports (
    account_id text NOT NULL,
    league_id  text NOT NULL,
    commodity  text NOT NULL,
    net_supply double precision NOT NULL,
    ts         timestamptz NOT NULL,
    PRIMARY KEY (account_id, league_id, commodity)
);
CREATE INDEX IF NOT EXISTS reports_league_idx ON reports (league_id);

-- Latest city snapshot per account (city-level, not per-league). The full CityProfile is stored as jsonb so
-- the mapping stays faithful to the Go struct's many append-only/omitempty fields without a wide column list.
CREATE TABLE IF NOT EXISTS city_profiles (
    account_id  text PRIMARY KEY,
    reported_at timestamptz NOT NULL,
    data        jsonb NOT NULL
);

-- Retained per-account city time-series (oldest→newest by reported_at, broken by ordinal for stable order).
CREATE TABLE IF NOT EXISTS city_profile_history (
    account_id  text NOT NULL,
    ordinal     bigint NOT NULL,           -- monotonic within an account; preserves insertion order for ties
    reported_at timestamptz NOT NULL,
    data        jsonb NOT NULL,
    PRIMARY KEY (account_id, ordinal)
);
CREATE INDEX IF NOT EXISTS city_profile_history_acct_time_idx
    ON city_profile_history (account_id, reported_at);

CREATE TABLE IF NOT EXISTS trades (
    id                text PRIMARY KEY,
    league_id         text NOT NULL,
    offered_by        text NOT NULL,
    counterparty      text NOT NULL,
    items             jsonb NOT NULL,       -- []LineItem (variable-length, append-only line fields)
    default_rate_bps  bigint NOT NULL,
    installments      integer NOT NULL,
    status            text NOT NULL,
    settled           integer NOT NULL,
    created           timestamptz NOT NULL,
    accepted_day      bigint NOT NULL DEFAULT 0,
    shortfall_installments jsonb NOT NULL DEFAULT '[]'::jsonb  -- []int
);
CREATE INDEX IF NOT EXISTS trades_league_idx ON trades (league_id);
CREATE INDEX IF NOT EXISTS trades_offered_idx ON trades (offered_by);
CREATE INDEX IF NOT EXISTS trades_counter_idx ON trades (counterparty);

CREATE TABLE IF NOT EXISTS bonds (
    id               text PRIMARY KEY,
    league_id        text NOT NULL,
    creditor_id      text NOT NULL,
    debtor_id        text NOT NULL,
    principal_cents  bigint NOT NULL,
    interest_bps     bigint NOT NULL,
    installments     integer NOT NULL,
    settled          integer NOT NULL,
    missed_count     integer NOT NULL,
    total_due_cents  bigint NOT NULL,
    status           text NOT NULL,
    origin           text NOT NULL,
    proposed_by      text NOT NULL DEFAULT '',
    created          timestamptz NOT NULL,
    defaulted_remaining_cents bigint NOT NULL DEFAULT 0,
    garnished_cents  bigint NOT NULL DEFAULT 0,
    garnish_ticks    integer NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS bonds_league_idx ON bonds (league_id);
CREATE INDEX IF NOT EXISTS bonds_creditor_idx ON bonds (creditor_id);
CREATE INDEX IF NOT EXISTS bonds_debtor_idx ON bonds (debtor_id);
CREATE INDEX IF NOT EXISTS bonds_status_idx ON bonds (status);

CREATE TABLE IF NOT EXISTS effects (
    id              text PRIMARY KEY,
    league_id       text NOT NULL,
    issuer_id       text NOT NULL,
    grantee_id      text NOT NULL,
    kind            text NOT NULL,
    cost_cents      bigint NOT NULL,
    demand_boost    integer NOT NULL,
    demand_kind     text NOT NULL,
    attract_rate    integer NOT NULL,
    ticks_remaining integer NOT NULL,
    created         timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS effects_league_idx ON effects (league_id);
CREATE INDEX IF NOT EXISTS effects_grantee_idx ON effects (grantee_id);
CREATE INDEX IF NOT EXISTS effects_issuer_idx ON effects (issuer_id);

-- Append-only settlement log. seq is the monotonic, league-shared sequence (== Memory.eventSeq).
CREATE TABLE IF NOT EXISTS settlement_events (
    seq         bigint PRIMARY KEY,
    league_id   text NOT NULL,
    payer_id    text NOT NULL,
    receiver_id text NOT NULL,
    cents       bigint NOT NULL,
    ref         text NOT NULL,
    created     timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS settlement_events_league_idx ON settlement_events (league_id);
CREATE INDEX IF NOT EXISTS settlement_events_payer_idx ON settlement_events (league_id, payer_id);
CREATE INDEX IF NOT EXISTS settlement_events_receiver_idx ON settlement_events (league_id, receiver_id);
CREATE INDEX IF NOT EXISTS settlement_events_ref_idx ON settlement_events (ref);
