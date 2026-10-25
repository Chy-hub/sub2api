-- Personal USTC performance observations are independent of billing logs.
CREATE TABLE IF NOT EXISTS ustc_speed_config (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    settings JSONB NOT NULL DEFAULT '{}',
    capture_since TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
INSERT INTO ustc_speed_config (id) VALUES (1) ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS ustc_speed_tasks (
    id BIGSERIAL PRIMARY KEY,
    revision BIGINT NOT NULL DEFAULT 1,
    config JSONB NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_run_at TIMESTAMPTZ,
    last_status TEXT NOT NULL DEFAULT '',
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    lease_revision BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS ustc_speed_tasks_due_idx ON ustc_speed_tasks (next_run_at) WHERE enabled;

CREATE TABLE IF NOT EXISTS ustc_speed_samples (
    id BIGSERIAL PRIMARY KEY,
    legacy_usage_id BIGINT UNIQUE,
    task_id BIGINT REFERENCES ustc_speed_tasks(id) ON DELETE SET NULL,
    round_id TEXT NOT NULL DEFAULT '',
    account_id BIGINT NOT NULL,
    account_name TEXT NOT NULL DEFAULT '',
    key_scope TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL CHECK (source IN ('real', 'probe')),
    status TEXT NOT NULL,
    profile TEXT NOT NULL DEFAULT '',
    reasoning TEXT NOT NULL DEFAULT 'unknown',
    stream BOOLEAN NOT NULL DEFAULT FALSE,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS ustc_speed_samples_time_idx ON ustc_speed_samples (started_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS ustc_speed_samples_account_idx ON ustc_speed_samples (account_id, model, started_at DESC);
CREATE INDEX IF NOT EXISTS ustc_speed_samples_source_idx ON ustc_speed_samples (source, started_at DESC);
CREATE INDEX IF NOT EXISTS ustc_speed_samples_round_idx ON ustc_speed_samples (round_id) WHERE round_id <> '';
CREATE INDEX IF NOT EXISTS ustc_speed_samples_legacy_account_idx ON ustc_speed_samples (account_id, legacy_usage_id) WHERE legacy_usage_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS ustc_speed_runtime (
    key_scope TEXT PRIMARY KEY,
    recent_429 BIGINT[] NOT NULL DEFAULT '{}',
    consecutive_429 INTEGER NOT NULL DEFAULT 0,
    success_streak INTEGER NOT NULL DEFAULT 0,
    last_429_at TIMESTAMPTZ,
    not_before TIMESTAMPTZ,
    paused BOOLEAN NOT NULL DEFAULT FALSE,
    last_status TEXT NOT NULL DEFAULT '',
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS ustc_speed_runtime_lease_idx ON ustc_speed_runtime (lease_until);
CREATE TABLE IF NOT EXISTS ustc_speed_daily_budget (
    day DATE NOT NULL,
    budget_key TEXT NOT NULL,
    requests BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (day, budget_key)
);
