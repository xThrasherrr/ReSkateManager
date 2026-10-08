-- In-game admins the manager added for panel users holding ingame.admin. Only
-- these are removed again when a user loses the permission; admins added by
-- hand are left alone.
CREATE TABLE managed_admins (
    instance_id TEXT NOT NULL,
    steam_id    TEXT NOT NULL,
    PRIMARY KEY (instance_id, steam_id)
);
