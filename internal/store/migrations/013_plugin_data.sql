-- What plugins keep: each one's own keys and values, and the boards they
-- rank players on. Both outlive a plugin's removal, so adding it again
-- brings them back.
CREATE TABLE plugin_store (
    plugin TEXT NOT NULL,
    key    TEXT NOT NULL,
    value  BLOB NOT NULL, -- a Lua value, as package luavm encodes it
    PRIMARY KEY (plugin, key)
) WITHOUT ROWID;

-- The plugin that defines a board first owns it, and only it adds to it.
CREATE TABLE boards (
    id     TEXT PRIMARY KEY,
    plugin TEXT NOT NULL,
    title  TEXT NOT NULL,
    unit   TEXT NOT NULL DEFAULT '',
    keep   TEXT NOT NULL,              -- sum, best or fastest
    public INTEGER NOT NULL DEFAULT 0  -- allowed out, once there's a way out
) WITHOUT ROWID;

-- A player's score on a board, on one server, over one period: all time
-- (starts 0), a month, or a week from Monday, UTC. at is when it last
-- changed, so a tie goes to whoever got there first.
CREATE TABLE board_scores (
    board       TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    period      TEXT NOT NULL,    -- all, month or week
    starts      INTEGER NOT NULL, -- unix seconds
    instance_id TEXT NOT NULL,
    steam_id    TEXT NOT NULL,
    value       NUMERIC NOT NULL, -- whole numbers come back as integers
    at          INTEGER NOT NULL,
    PRIMARY KEY (board, period, starts, instance_id, steam_id)
) WITHOUT ROWID;
CREATE INDEX board_scores_player ON board_scores(steam_id);

-- For a board's names, each player's latest.
CREATE INDEX player_history_player ON player_history(steam_id, last_seen);
