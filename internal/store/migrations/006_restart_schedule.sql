-- A server's scheduled restarts: times of day on the manager's clock, comma
-- separated ("04:00,16:00"), and every so many hours of uptime. Empty and 0
-- are off.
ALTER TABLE instances ADD COLUMN restart_times TEXT NOT NULL DEFAULT '';
ALTER TABLE instances ADD COLUMN restart_hours INTEGER NOT NULL DEFAULT 0;
