CREATE TABLE IF NOT EXISTS watchlist_queue (
    source TEXT NOT NULL,
    external_id TEXT NOT NULL,
    media_type TEXT NOT NULL CHECK (media_type IN ('movie', 'show')),
    title TEXT NOT NULL,
    release_year INTEGER NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'acquiring', 'succeeded', 'not_cached', 'retryable', 'manual_review')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    lease_version INTEGER NOT NULL DEFAULT 0 CHECK (lease_version >= 0),
    lease_until_unix_ms INTEGER NOT NULL DEFAULT 0,
    next_attempt_unix_ms INTEGER NOT NULL DEFAULT 0,
    published_object_id TEXT NOT NULL DEFAULT '',
    first_observed_unix_ms INTEGER NOT NULL,
    last_observed_unix_ms INTEGER NOT NULL,
    updated_unix_ms INTEGER NOT NULL,
    PRIMARY KEY (source, external_id)
);

CREATE INDEX IF NOT EXISTS watchlist_queue_claimable
    ON watchlist_queue (media_type, state, next_attempt_unix_ms, lease_until_unix_ms);
