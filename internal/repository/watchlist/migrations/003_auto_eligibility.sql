ALTER TABLE watchlist_queue
    ADD COLUMN auto_eligible INTEGER NOT NULL DEFAULT 0
    CHECK (auto_eligible IN (0, 1));

CREATE INDEX IF NOT EXISTS watchlist_queue_auto_eligible
    ON watchlist_queue (auto_eligible, media_type, state, next_attempt_unix_ms, lease_until_unix_ms);
