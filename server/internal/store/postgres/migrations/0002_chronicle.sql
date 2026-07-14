-- Open Markets — Postgres schema (0002_chronicle): the League Chronicle (social slice 2).
--
-- A persistent, narrated history of a league's saga. Each entry's `text` is the FROZEN narration rendered once
-- at append time (names resolved then), so the chronicle is a permanent record. `seq` is a single monotonic
-- counter shared across all leagues (matching Memory.chronicleSeq), allocated from the meta('chronicle_seq')
-- row, bumped FOR UPDATE inside the same transaction that writes the entry — strictly increasing, never reused.

CREATE TABLE IF NOT EXISTS chronicle (
    seq        bigint PRIMARY KEY,
    league_id  text NOT NULL,
    kind       text NOT NULL,
    actor_id   text NOT NULL,
    target_id  text NOT NULL DEFAULT '',
    text       text NOT NULL,
    cents      bigint NOT NULL DEFAULT 0,
    created    timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS chronicle_league_seq_idx ON chronicle (league_id, seq);
CREATE INDEX IF NOT EXISTS chronicle_league_created_idx ON chronicle (league_id, created);
