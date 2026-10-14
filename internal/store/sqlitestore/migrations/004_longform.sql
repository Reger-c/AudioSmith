-- AudioSmith database, migration 3: longform renders, exports and the
-- project library (M3, thread #18).
--
-- * projects.summary: what the Projects page shows of a project without
--   reading its body (JSON: lines, chapters, words, voices…).
-- * renders: a render of a Stories or Audiobook project (core.Render):
--   its manifest, chapter states and outputs in body (JSON owned by the
--   longform service). project_id is SET NULL when the project goes:
--   finished renders and their audio are kept.
-- * exports: what was exported where (core.Export). Deleting a record
--   keeps the file.
-- * segments_project_voice: "where this voice has been used" groups
--   segments by project.

ALTER TABLE projects ADD COLUMN summary TEXT NOT NULL DEFAULT '{}';

CREATE TABLE renders (
  seq           INTEGER PRIMARY KEY,
  id            TEXT NOT NULL UNIQUE,
  project_id    TEXT REFERENCES projects(id) ON DELETE SET NULL,
  kind          TEXT NOT NULL,
  title         TEXT NOT NULL,
  state         TEXT NOT NULL,
  audition      INTEGER NOT NULL DEFAULT 0,
  audio_seconds REAL NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  body          TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX renders_project ON renders(project_id, created_at DESC);
CREATE INDEX renders_state ON renders(state, audition);
CREATE VIRTUAL TABLE renders_fts USING fts5(title, content='renders', content_rowid='seq', tokenize='trigram');
CREATE TRIGGER renders_fts_insert AFTER INSERT ON renders BEGIN
  INSERT INTO renders_fts(rowid, title) VALUES (new.seq, new.title);
END;
CREATE TRIGGER renders_fts_delete AFTER DELETE ON renders BEGIN
  INSERT INTO renders_fts(renders_fts, rowid, title) VALUES ('delete', old.seq, old.title);
END;
CREATE TRIGGER renders_fts_update AFTER UPDATE OF title ON renders BEGIN
  INSERT INTO renders_fts(renders_fts, rowid, title) VALUES ('delete', old.seq, old.title);
  INSERT INTO renders_fts(rowid, title) VALUES (new.seq, new.title);
END;

CREATE TABLE exports (
  seq        INTEGER PRIMARY KEY,
  id         TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  kind       TEXT NOT NULL,
  source_id  TEXT NOT NULL DEFAULT '',
  project_id TEXT REFERENCES projects(id) ON DELETE SET NULL,
  title      TEXT NOT NULL,
  path       TEXT NOT NULL,
  format     TEXT NOT NULL DEFAULT '',
  bytes      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX exports_created ON exports(created_at DESC);
CREATE INDEX exports_source ON exports(source_id);
CREATE VIRTUAL TABLE exports_fts USING fts5(title, path, content='exports', content_rowid='seq', tokenize='trigram');
CREATE TRIGGER exports_fts_insert AFTER INSERT ON exports BEGIN
  INSERT INTO exports_fts(rowid, title, path) VALUES (new.seq, new.title, new.path);
END;
CREATE TRIGGER exports_fts_delete AFTER DELETE ON exports BEGIN
  INSERT INTO exports_fts(exports_fts, rowid, title, path) VALUES ('delete', old.seq, old.title, old.path);
END;
CREATE TRIGGER exports_fts_update AFTER UPDATE OF title, path ON exports BEGIN
  INSERT INTO exports_fts(exports_fts, rowid, title, path) VALUES ('delete', old.seq, old.title, old.path);
  INSERT INTO exports_fts(rowid, title, path) VALUES (new.seq, new.title, new.path);
END;

CREATE INDEX segments_project_voice ON segments(project_id, voice_id);
