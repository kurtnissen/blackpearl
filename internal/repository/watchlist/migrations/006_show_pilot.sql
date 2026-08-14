ALTER TABLE watchlist_settings
    ADD COLUMN show_policy TEXT NOT NULL DEFAULT 'off'
    CHECK (show_policy IN ('off', 'pilot'));

ALTER TABLE watchlist_queue
    ADD COLUMN intent_season INTEGER NOT NULL DEFAULT 0
    CHECK (intent_season BETWEEN 0 AND 99);

ALTER TABLE watchlist_queue
    ADD COLUMN intent_episode INTEGER NOT NULL DEFAULT 0
    CHECK (intent_episode BETWEEN 0 AND 999);
