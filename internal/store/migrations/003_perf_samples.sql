-- One row per server per sample while its process runs. run is the process's
-- start time, so samples group into runs. Times are unix seconds.
CREATE TABLE perf_samples (
    instance_id TEXT NOT NULL,
    at          INTEGER NOT NULL,
    run         INTEGER NOT NULL,
    cpu         REAL NOT NULL,    -- percent of the whole machine
    mem         INTEGER NOT NULL, -- bytes
    players     INTEGER NOT NULL,
    PRIMARY KEY (instance_id, at)
) WITHOUT ROWID;
CREATE INDEX perf_samples_at ON perf_samples(at);
