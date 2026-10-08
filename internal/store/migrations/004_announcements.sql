-- Chat messages the manager sends on a timer. instance_id '*' sends on every
-- server; interval is in seconds.
CREATE TABLE announcements (
    id          INTEGER PRIMARY KEY,
    instance_id TEXT NOT NULL,
    message     TEXT NOT NULL,
    interval    INTEGER NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL
);
CREATE INDEX announcements_instance ON announcements(instance_id);

-- Existing panels seeded their built-in roles before this permission existed.
UPDATE roles SET permissions = json_insert(permissions, '$[#]', 'announcements.manage')
WHERE builtin = 1 AND name = 'Administrator';
