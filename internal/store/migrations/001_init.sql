CREATE TABLE instances (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    dir          TEXT NOT NULL UNIQUE,
    auto_start   INTEGER NOT NULL DEFAULT 0,
    auto_restart INTEGER NOT NULL DEFAULT 1,
    auto_update  INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL
);

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT,
    steam_id      TEXT UNIQUE,
    is_owner      INTEGER NOT NULL DEFAULT 0,
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER
);

-- permissions: JSON array of permission keys.
CREATE TABLE roles (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
    permissions TEXT NOT NULL DEFAULT '[]',
    builtin     INTEGER NOT NULL DEFAULT 0
);

-- instance_id '*' grants the role on every instance.
CREATE TABLE user_roles (
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id     INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    instance_id TEXT NOT NULL DEFAULT '*',
    PRIMARY KEY (user_id, role_id, instance_id)
);

CREATE TABLE sessions (
    token_hash   TEXT PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    ip           TEXT,
    user_agent   TEXT
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY,
    at          INTEGER NOT NULL,
    user_id     INTEGER,
    username    TEXT,
    instance_id TEXT,
    action      TEXT NOT NULL,
    detail      TEXT,
    ip          TEXT
);
CREATE INDEX audit_log_at ON audit_log(at);

CREATE TABLE player_history (
    instance_id TEXT NOT NULL,
    steam_id    TEXT NOT NULL,
    name        TEXT NOT NULL,
    first_seen  INTEGER NOT NULL,
    last_seen   INTEGER NOT NULL,
    sessions    INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (instance_id, steam_id)
);

CREATE TABLE kv (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
