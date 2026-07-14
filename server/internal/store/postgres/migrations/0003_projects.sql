-- Open Markets — Postgres schema (0003_projects): co-op MEGAPROJECTS (Great Works, social slice 4).
--
-- An AI-curated league goal requiring commodity units + an optional § sum contributed by members, granting a
-- LASTING BUFF to every builder on completion. Reqs/Goods/By are stored as jsonb so the variable-shape maps and
-- slices mirror the Go struct faithfully (Goods/By are commodity→units / account→score maps).
--
-- Money-conservation note: a § contribution is booked member → the pseudo-counterparty 'project:'+id via the
-- shared settlement_events log (one balanced event), so AuditLeague's total stays 0 — the § "sits in" the
-- project account. Goods contributions and the completion buff-effect carry NO settlement event. The gold
-- contribution + completion (buff-effect inserts) run in ONE transaction.

CREATE TABLE IF NOT EXISTS projects (
    id                   text PRIMARY KEY,
    league_id            text NOT NULL,
    name                 text NOT NULL,
    description          text NOT NULL DEFAULT '',
    reqs                 jsonb NOT NULL DEFAULT '[]'::jsonb,  -- []ProjectReq {commodity, qty}
    gold_req_cents       bigint NOT NULL DEFAULT 0,
    goods                jsonb NOT NULL DEFAULT '{}'::jsonb,  -- map commodity → units contributed
    gold                 bigint NOT NULL DEFAULT 0,           -- § contributed so far (cents)
    by_score             jsonb NOT NULL DEFAULT '{}'::jsonb,  -- map accountID → builder score
    buff_kind            text NOT NULL DEFAULT '',
    buff_magnitude_cents bigint NOT NULL DEFAULT 0,
    buff_days            integer NOT NULL DEFAULT 0,
    status               text NOT NULL,
    created              timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS projects_league_idx ON projects (league_id);
CREATE INDEX IF NOT EXISTS projects_league_status_idx ON projects (league_id, status);
