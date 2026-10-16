-- One row per visit: a player on a server from joining to leaving, in unix
-- seconds. left_at is NULL while they're on. reason is the server's, from its
-- leave line ("Disconnected."), or the manager's when the roster emptied
-- without one ("Server stopped.").
CREATE TABLE player_sessions (
    instance_id TEXT NOT NULL,
    steam_id    TEXT NOT NULL,
    joined_at   INTEGER NOT NULL,
    left_at     INTEGER,
    reason      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (instance_id, steam_id, joined_at)
) WITHOUT ROWID;
CREATE INDEX player_sessions_player ON player_sessions(steam_id);
CREATE INDEX player_sessions_open ON player_sessions(instance_id) WHERE left_at IS NULL;
