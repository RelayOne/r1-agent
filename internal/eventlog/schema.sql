PRAGMA journal_mode = WAL;
PRAGMA synchronous  = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS events (
    id           TEXT    PRIMARY KEY,             -- ULID (sortable, monotonic per process)
    sequence     INTEGER NOT NULL UNIQUE,         -- monotonic per DB (autoincrement via max(sequence)+1)
    type         TEXT    NOT NULL,                -- dotted namespace, e.g. task.dispatch
    session_id   TEXT,
    mission_id   TEXT,
    task_id      TEXT,
    loop_id      TEXT,
    timestamp    TEXT    NOT NULL,                -- RFC3339Nano UTC
    emitter_id   TEXT,
    payload      BLOB,                            -- canonical JSON bytes (sorted keys)
    parent_hash  TEXT,                            -- sha256(prev.id || prev.payload || prev.parent_hash), hex; "" at root
    causal_ref   TEXT                             -- optional FK-style pointer into events.id
);

CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id);
CREATE INDEX IF NOT EXISTS idx_events_task    ON events(task_id);
CREATE INDEX IF NOT EXISTS idx_events_mission ON events(mission_id);
CREATE INDEX IF NOT EXISTS idx_events_seq     ON events(sequence);
CREATE INDEX IF NOT EXISTS idx_events_type    ON events(type);

-- Single-row head pointer table makes chain-append a single read.
CREATE TABLE IF NOT EXISTS event_chain_head (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    head_id     TEXT    NOT NULL DEFAULT '',
    head_hash   TEXT    NOT NULL DEFAULT '',
    head_seq    INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO event_chain_head (id) VALUES (1);
