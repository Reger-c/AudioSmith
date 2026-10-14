-- AudioSmith database, migration 5: dubbing (M4, thread #19).
--
-- One row per dub project (the Dub page's, and every video of the batch
-- queue):
-- * doc: the whole internal/dub.Project as JSON (the source of truth; the
--   store does not read it);
-- * the other columns are what lists show and filter on, copied from it
--   by the dub service: title, the source (a path or a URL), the target
--   languages ("es,fr"), the phase, the source's duration, and batch, the
--   batch queue the project belongs to ('' = made on the Dub page).
-- The project's files (decoded audio, stems, rendered lines, tracks) live
-- in <PathUserData>/dubs/<id>/, removed by the service with the row.

CREATE TABLE dubs (
  seq        INTEGER PRIMARY KEY,
  id         TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  batch      TEXT NOT NULL DEFAULT '',
  title      TEXT NOT NULL DEFAULT '',
  source     TEXT NOT NULL DEFAULT '',
  languages  TEXT NOT NULL DEFAULT '',
  phase      TEXT NOT NULL DEFAULT '',
  duration   REAL NOT NULL DEFAULT 0,
  doc        TEXT NOT NULL
);
CREATE INDEX dubs_updated ON dubs(updated_at DESC);
CREATE INDEX dubs_batch ON dubs(batch, created_at);
