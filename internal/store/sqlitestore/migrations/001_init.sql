-- AudioSmith database, migration 1: the M0 schema.
--
-- Conventions
--   * File: <PathUserData>/audiosmith.db, opened with the MyGo SQLite plugin
--     (purego, no cgo; FTS5 + JSON1 built in; foreign keys on), WAL journal.
--   * PRAGMA user_version = the last migration applied. Each migration runs
--     in one transaction at start-up, after the file was copied to
--     audiosmith.db.bak-<previous version> (no copy for a new database).
--   * Ids are ULIDs (core.NewID) as TEXT; MVP take ids keep their old form.
--   * Times are INTEGER Unix milliseconds (UTC).
--   * Flexible parts are JSON TEXT columns; anything we filter or sort on
--     is a real column, copied from the JSON when the row is written.
--   * Blobs are files under <PathUserData>/{clips,voices,projects}/; rows
--     hold names relative to those directories.
--   * Tables with a full-text index have an INTEGER PRIMARY KEY (seq) for
--     the index's content_rowid: an implicit rowid may change on VACUUM.
--     The public id is a UNIQUE TEXT column.
--   * Full-text indexes use the trigram tokenizer: case-insensitive
--     substring search that works for Chinese and Japanese too. Queries
--     shorter than three characters fall back to LIKE (see takes.go).
--   * Statements end with ";" at the end of a line; a trigger body ends at
--     a line "END;". Whole-line comments are dropped before splitting;
--     don't end a line of code with a comment.

-- Takes: one generated audio + its recipe (core.Take).
CREATE TABLE takes (
  seq           INTEGER PRIMARY KEY,
  id            TEXT NOT NULL UNIQUE,
  created_at    INTEGER NOT NULL,
  file          TEXT NOT NULL,
  text          TEXT NOT NULL,
  model         TEXT NOT NULL,
  voice_id      TEXT REFERENCES voices(id) ON DELETE SET NULL,
  project_id    TEXT REFERENCES projects(id) ON DELETE SET NULL,
  job_id        TEXT,
  starred       INTEGER NOT NULL DEFAULT 0,
  sample_rate   INTEGER NOT NULL DEFAULT 0,
  audio_seconds REAL NOT NULL DEFAULT 0,
  gen_seconds   REAL NOT NULL DEFAULT 0,
  recipe        TEXT NOT NULL
);
CREATE INDEX takes_created ON takes(created_at DESC);
CREATE INDEX takes_project ON takes(project_id, created_at);
CREATE INDEX takes_voice ON takes(voice_id);
CREATE VIRTUAL TABLE takes_fts USING fts5(text, content='takes', content_rowid='seq', tokenize='trigram');
CREATE TRIGGER takes_fts_insert AFTER INSERT ON takes BEGIN
  INSERT INTO takes_fts(rowid, text) VALUES (new.seq, new.text);
END;
CREATE TRIGGER takes_fts_delete AFTER DELETE ON takes BEGIN
  INSERT INTO takes_fts(takes_fts, rowid, text) VALUES ('delete', old.seq, old.text);
END;
CREATE TRIGGER takes_fts_update AFTER UPDATE OF text ON takes BEGIN
  INSERT INTO takes_fts(takes_fts, rowid, text) VALUES ('delete', old.seq, old.text);
  INSERT INTO takes_fts(rowid, text) VALUES (new.seq, new.text);
END;

-- Voice profiles (core.Voice). Blobs in voices/<id>/.
CREATE TABLE voices (
  seq        INTEGER PRIMARY KEY,
  id         TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('builtin','cloned','designed')),
  model      TEXT NOT NULL DEFAULT '',
  locked     INTEGER NOT NULL DEFAULT 0,
  consent    INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  profile    TEXT NOT NULL
);
CREATE VIRTUAL TABLE voices_fts USING fts5(name, content='voices', content_rowid='seq', tokenize='trigram');
CREATE TRIGGER voices_fts_insert AFTER INSERT ON voices BEGIN
  INSERT INTO voices_fts(rowid, name) VALUES (new.seq, new.name);
END;
CREATE TRIGGER voices_fts_delete AFTER DELETE ON voices BEGIN
  INSERT INTO voices_fts(voices_fts, rowid, name) VALUES ('delete', old.seq, old.name);
END;
CREATE TRIGGER voices_fts_update AFTER UPDATE OF name ON voices BEGIN
  INSERT INTO voices_fts(voices_fts, rowid, name) VALUES ('delete', old.seq, old.name);
  INSERT INTO voices_fts(rowid, name) VALUES (new.seq, new.name);
END;

-- Projects (core.Project): stories, audiobooks, dubs, transcripts, workflows.
CREATE TABLE projects (
  seq        INTEGER PRIMARY KEY,
  id         TEXT NOT NULL UNIQUE,
  kind       TEXT NOT NULL,
  title      TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  body       TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX projects_kind ON projects(kind, updated_at DESC);
CREATE VIRTUAL TABLE projects_fts USING fts5(title, content='projects', content_rowid='seq', tokenize='trigram');
CREATE TRIGGER projects_fts_insert AFTER INSERT ON projects BEGIN
  INSERT INTO projects_fts(rowid, title) VALUES (new.seq, new.title);
END;
CREATE TRIGGER projects_fts_delete AFTER DELETE ON projects BEGIN
  INSERT INTO projects_fts(projects_fts, rowid, title) VALUES ('delete', old.seq, old.title);
END;
CREATE TRIGGER projects_fts_update AFTER UPDATE OF title ON projects BEGIN
  INSERT INTO projects_fts(projects_fts, rowid, title) VALUES ('delete', old.seq, old.title);
  INSERT INTO projects_fts(rowid, title) VALUES (new.seq, new.title);
END;

-- Segments of a project (story lines, dub cues, audiobook sentences): M3/M4.
-- ord is fractional, so inserting between two segments rewrites one row.
-- input_hash is the content-addressed cache key of the current render.
CREATE TABLE segments (
  id          TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  ord         REAL NOT NULL,
  text        TEXT NOT NULL,
  start_ms    INTEGER,
  end_ms      INTEGER,
  speaker     TEXT,
  voice_id    TEXT REFERENCES voices(id) ON DELETE SET NULL,
  take_id     TEXT REFERENCES takes(id) ON DELETE SET NULL,
  input_hash  TEXT,
  props       TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX segments_project ON segments(project_id, ord);

-- Jobs (jobs.Job). params/progress/result/error are JSON.
CREATE TABLE jobs (
  id           TEXT PRIMARY KEY,
  kind         TEXT NOT NULL,
  lane         TEXT NOT NULL,
  priority     INTEGER NOT NULL,
  state        TEXT NOT NULL,
  owner        TEXT NOT NULL DEFAULT '',
  title        TEXT NOT NULL DEFAULT '',
  attempt      INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 1,
  idempotent   INTEGER NOT NULL DEFAULT 0,
  params       TEXT NOT NULL,
  progress     TEXT,
  result       TEXT,
  error        TEXT,
  created_at   INTEGER NOT NULL,
  started_at   INTEGER,
  finished_at  INTEGER
);
CREATE INDEX jobs_state ON jobs(state, lane, priority DESC, created_at);
CREATE INDEX jobs_owner ON jobs(owner);
-- Dependencies (Spec.After), in order. after_id has no foreign key: the
-- scheduler may save the jobs of a graph in any order.
CREATE TABLE job_deps (
  job_id   TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  after_id TEXT NOT NULL,
  PRIMARY KEY (job_id, after_id)
);
-- State changes, for diagnostics (not progress ticks); written by triggers.
CREATE TABLE job_events (
  job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  at     INTEGER NOT NULL,
  state  TEXT NOT NULL,
  detail TEXT
);
CREATE INDEX job_events_job ON job_events(job_id, at);
CREATE TRIGGER jobs_event_insert AFTER INSERT ON jobs BEGIN
  INSERT INTO job_events(job_id, at, state, detail)
  VALUES (new.id, CAST(unixepoch('subsec') * 1000 AS INTEGER), new.state, json_extract(new.error, '$.code'));
END;
CREATE TRIGGER jobs_event_update AFTER UPDATE OF state ON jobs WHEN old.state IS NOT new.state BEGIN
  INSERT INTO job_events(job_id, at, state, detail)
  VALUES (new.id, CAST(unixepoch('subsec') * 1000 AS INTEGER), new.state, json_extract(new.error, '$.code'));
END;

-- Licence acceptances (core.LicenceAcceptance). model '' = every model
-- under the licence.
CREATE TABLE licences (
  licence     TEXT NOT NULL,
  model       TEXT NOT NULL DEFAULT '',
  accepted_at INTEGER NOT NULL,
  PRIMARY KEY (licence, model)
);

-- Small JSON documents (store.Documents): "settings:app", "draft:<page>",
-- "migration:historyjson".
CREATE TABLE documents (
  key        TEXT PRIMARY KEY,
  doc        TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

-- Later milestones add (sketch): pronunciation(lang, word, reading, …) M1;
-- glossary(project_id, source, target) M4; transcripts / words M2;
-- exports(project_id, path, format, created_at) M3; usage(day, metric, n) M7;
-- notifications(id, …) if the centre must survive restarts.
