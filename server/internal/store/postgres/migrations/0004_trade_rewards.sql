-- Open Markets — Postgres schema (0004_trade_rewards): Great Works themed trade rewards.

ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS trade_reward_kind text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS trade_reward_commodity text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS trade_reward_pct_bips integer NOT NULL DEFAULT 0;

ALTER TABLE effects
    ADD COLUMN IF NOT EXISTS commodity text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS trade_pct_bips integer NOT NULL DEFAULT 0;
