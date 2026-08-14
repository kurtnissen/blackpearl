CREATE TABLE IF NOT EXISTS watchlist_settings (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    acquisition_enabled INTEGER NOT NULL CHECK (acquisition_enabled IN (0, 1))
);
