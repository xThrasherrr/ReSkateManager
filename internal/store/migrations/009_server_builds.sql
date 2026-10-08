-- ReSkate server releases the manager has met since it was built: the SHA-256
-- of a release's program and the version it shipped as, so a server's
-- installed version can be told from its program. seen is unix seconds.
CREATE TABLE server_builds (
    sha256  TEXT PRIMARY KEY,
    version TEXT NOT NULL,
    seen    INTEGER NOT NULL
) WITHOUT ROWID;
