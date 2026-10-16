-- Throwdowns that finished, from the server's [throwdown] lines: one row
-- each, and one per player in its results. One a map change or a stop ends
-- has no results line, and isn't kept. Times are unix seconds.
CREATE TABLE throwdowns (
    id          INTEGER PRIMARY KEY,
    instance_id TEXT NOT NULL,
    mode        TEXT NOT NULL,            -- jam, spot_battle or skate; '' for one the server didn't name
    leader_id   TEXT NOT NULL DEFAULT '', -- SteamID64; '' when the line's name was nobody in particular
    leader      TEXT NOT NULL,            -- the name the line had
    finished_at INTEGER NOT NULL
);
CREATE INDEX throwdowns_finished ON throwdowns(instance_id, finished_at);

CREATE TABLE throwdown_players (
    throwdown_id INTEGER NOT NULL REFERENCES throwdowns(id) ON DELETE CASCADE,
    seat         INTEGER NOT NULL,            -- 1, 2, ... in the results' order
    steam_id     TEXT NOT NULL DEFAULT '',
    name         TEXT NOT NULL,
    place        INTEGER,                     -- by points; NULL for S.K.A.T.E., which the server can't rank
    points       INTEGER,
    landed       INTEGER,                     -- S.K.A.T.E.'s tries
    missed       INTEGER,
    quit         INTEGER NOT NULL DEFAULT 0,
    won          INTEGER NOT NULL DEFAULT 0,  -- never for a quit; see store.AddThrowdown
    PRIMARY KEY (throwdown_id, seat)
) WITHOUT ROWID;
CREATE INDEX throwdown_players_player ON throwdown_players(steam_id);
