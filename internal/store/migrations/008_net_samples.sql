-- One row per [network] line a server logs: once a minute while anyone is on,
-- with its activity log on. run is the process's start, as in perf_samples;
-- failed, skipped and dropped are totals over the run, so a run's rows give
-- how many each minute. Columns a server's line lacked are NULL. Times are
-- unix seconds.
CREATE TABLE net_samples (
    instance_id TEXT NOT NULL,
    at          INTEGER NOT NULL,
    run         INTEGER NOT NULL,
    players     INTEGER NOT NULL,
    out_kbs     INTEGER NOT NULL, -- KB/s sent
    in_kbs      INTEGER NOT NULL, -- KB/s received
    ping_ms     INTEGER NOT NULL, -- the worst player's
    queued_kb   INTEGER,          -- waiting to be sent
    queue_ms    INTEGER,          -- how long a message sent then would wait
    failed      INTEGER,
    skipped     INTEGER,
    dropped     INTEGER,
    busy_pct    INTEGER,          -- of the main loop, over the minute
    pass_ms     REAL,             -- the loop's longest pass in the minute
    gap_ms      REAL,             -- and its longest gap between passes
    PRIMARY KEY (instance_id, at)
) WITHOUT ROWID;
CREATE INDEX net_samples_at ON net_samples(at);
