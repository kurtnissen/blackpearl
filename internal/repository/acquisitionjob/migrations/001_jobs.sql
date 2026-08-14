CREATE TABLE IF NOT EXISTS acquisition_jobs (
    id TEXT PRIMARY KEY,
    intent_key TEXT NOT NULL,
    media_type TEXT NOT NULL CHECK (media_type IN ('movie', 'episode')),
    title TEXT NOT NULL,
    release_year INTEGER NOT NULL,
    season INTEGER NOT NULL DEFAULT 0,
    episode INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL CHECK (state IN ('queued', 'selected', 'preparing', 'succeeded', 'failed', 'manual_review')),
    selected_provider TEXT NOT NULL DEFAULT '',
    selected_title TEXT NOT NULL DEFAULT '',
    selected_size INTEGER NOT NULL DEFAULT 0,
    selected_indexer TEXT NOT NULL DEFAULT '',
    selected_info_hash TEXT NOT NULL DEFAULT '',
    selected_seeders INTEGER NOT NULL DEFAULT 0,
    selected_has_seeders INTEGER NOT NULL DEFAULT 0 CHECK (selected_has_seeders IN (0, 1)),
    created_provider TEXT NOT NULL DEFAULT '',
    created_object_id TEXT NOT NULL DEFAULT '',
    published_object_id TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    progress INTEGER NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    lease_version INTEGER NOT NULL DEFAULT 0 CHECK (lease_version >= 0),
    lease_until_unix_ms INTEGER NOT NULL DEFAULT 0,
    next_attempt_unix_ms INTEGER NOT NULL DEFAULT 0,
    created_unix_ms INTEGER NOT NULL,
    updated_unix_ms INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS acquisition_jobs_active_intent
    ON acquisition_jobs (intent_key)
    WHERE state IN ('queued', 'selected', 'preparing');

CREATE INDEX IF NOT EXISTS acquisition_jobs_claimable
    ON acquisition_jobs (state, next_attempt_unix_ms, lease_until_unix_ms, created_unix_ms);

CREATE INDEX IF NOT EXISTS acquisition_jobs_recent
    ON acquisition_jobs (updated_unix_ms DESC, id DESC);
