ALTER TABLE watchlist_queue
    ADD COLUMN background_job_id TEXT NOT NULL DEFAULT ''
    CHECK (
        background_job_id = '' OR (
            length(background_job_id) = 32
            AND background_job_id NOT GLOB '*[^0-9a-f]*'
        )
    );

CREATE INDEX IF NOT EXISTS watchlist_queue_background_job
    ON watchlist_queue (background_job_id);
