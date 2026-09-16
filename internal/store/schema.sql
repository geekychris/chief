-- Chief schema. Applied once per PRAGMA user_version bump.
-- Session/lease/notification tables land in later milestones.

CREATE TABLE IF NOT EXISTS projects (
    id                  TEXT PRIMARY KEY,
    path                TEXT NOT NULL UNIQUE,
    name                TEXT NOT NULL,
    spawn_mode          TEXT NOT NULL DEFAULT 'attach',
    cmux_workspace_id   TEXT,
    state               TEXT NOT NULL DEFAULT 'idle',
    last_seen           TEXT,
    created_at          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
    id                  TEXT PRIMARY KEY,
    project_id          TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title               TEXT NOT NULL,
    body                TEXT NOT NULL DEFAULT '',
    source_file         TEXT NOT NULL,
    source_line_hash    TEXT NOT NULL,
    status              TEXT NOT NULL,           -- pending|active|blocked|deferred|done
    priority            INTEGER NOT NULL DEFAULT 0,
    category            TEXT NOT NULL DEFAULT '',
    required_resources  TEXT NOT NULL DEFAULT '[]', -- JSON array
    -- source_line, blocks, blocked_by added by later migrations (v2, v4).
    due                 TEXT,
    claimed_at          TEXT,
    claimed_by_session  TEXT,
    revive_count        INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL,
    completed_at        TEXT
);

CREATE INDEX IF NOT EXISTS tasks_project_status
    ON tasks(project_id, status);
CREATE INDEX IF NOT EXISTS tasks_claim_order
    ON tasks(project_id, status, priority DESC, created_at ASC);

CREATE TABLE IF NOT EXISTS events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          TEXT NOT NULL,
    project_id  TEXT,
    session_id  TEXT,
    kind        TEXT NOT NULL,
    payload     TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS events_ts         ON events(ts);
CREATE INDEX IF NOT EXISTS events_project_ts ON events(project_id, ts);

-- Attention flags: questions/prompts a session raises for the human.
-- Two flavors distinguished by `kind`:
--   'question'    — Claude asked me something (chief_flag_for_human).
--   'next_task'   — Chief-generated suggestion after a completion.
-- `agg_key` groups near-duplicates within the rate-limit window so we can
-- collapse "3 new questions in Project X" into a single notification.
-- `ack_at` NULL means unresolved; ack_reply carries the human's answer.
CREATE TABLE IF NOT EXISTS flags (
    id            TEXT PRIMARY KEY,
    project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id    TEXT,
    kind          TEXT NOT NULL DEFAULT 'question',       -- question|next_task
    urgency       TEXT NOT NULL DEFAULT 'attention',      -- info|attention|urgent
    question      TEXT NOT NULL DEFAULT '',
    suggested_id  TEXT,                                   -- populated for kind=next_task
    agg_key       TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    ack_at        TEXT,
    ack_reply     TEXT NOT NULL DEFAULT '',
    resolution    TEXT NOT NULL DEFAULT ''                -- answered|approved|skipped|snoozed|dismissed
);
CREATE INDEX IF NOT EXISTS flags_open_by_urgency
    ON flags(ack_at, urgency, created_at);
CREATE INDEX IF NOT EXISTS flags_project_open
    ON flags(project_id, ack_at);
