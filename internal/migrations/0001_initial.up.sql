CREATE TABLE IF NOT EXISTS synced_items (
    id                TEXT PRIMARY KEY,
    remote_id         TEXT NOT NULL,
    jellyfin_item_id  TEXT NOT NULL,
    provider_ids      TEXT NOT NULL,
    resolution        TEXT NOT NULL,
    encoding          TEXT NOT NULL,
    strm_path         TEXT NOT NULL,
    created_at        DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at        DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS sync_runs (
    id            TEXT PRIMARY KEY,
    started_at    DATETIME NOT NULL,
    completed_at  DATETIME,
    status        TEXT NOT NULL DEFAULT 'running',
    items_added   INTEGER NOT NULL DEFAULT 0,
    items_removed INTEGER NOT NULL DEFAULT 0,
    error         TEXT
);
