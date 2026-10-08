-- One row per sample of the machine the manager runs on, taken with the
-- servers' perf_samples. Times are unix seconds, memory in bytes.
CREATE TABLE host_samples (
    at          INTEGER PRIMARY KEY,
    cpu         REAL NOT NULL,    -- percent of the whole machine
    mem_total   INTEGER NOT NULL,
    mem_used    INTEGER NOT NULL, -- total minus available
    mem_cached  INTEGER NOT NULL, -- cache the system can hand back
    limit_max   INTEGER NOT NULL, -- the manager's cgroup memory limit; 0 without one
    limit_used  INTEGER NOT NULL,
    servers_cpu REAL NOT NULL,    -- every server's process, summed
    servers_mem INTEGER NOT NULL,
    manager_mem INTEGER NOT NULL,
    running     INTEGER NOT NULL, -- servers running, out of servers
    servers     INTEGER NOT NULL,
    players     INTEGER NOT NULL
);
