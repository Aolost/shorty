CREATE TABLE IF NOT EXISTS links (
    code TEXT PRIMARY KEY,
    destination TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_links_expires_at ON links (expires_at) WHERE expires_at > 0;
