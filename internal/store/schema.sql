-- AudioSmith database schema, version 1: a SKETCH for thread A (M0).
-- Thread A turns it into migration 1 of internal/store/sqlitestore and may
-- change it; record the reasons in docs/architecture.md.
--
-- Conventions
--   * File: <PathUserData>/audiosmith.db, opened with the MyGo SQLite plugin
--     (purego, no cgo; FTS5 + JSON1 built in; foreign keys on).
--   * PRAGMA user_version = schema version; migrations run in one
--     transaction at start-up, after copying the file to audiosmith.db.bak-<v>.
--   * Ids are ULIDs (core.NewID) as TEXT; MVP take ids keep their old form.
--   * Times are INTEGER Unix milliseconds (UTC).
--   * Flexible parts are JSON TEXT columns; anything we filter or sort on
--     is a real column.
--   * Blobs are files under <PathUserData>/{clips,voices,projects}/; rows
--     hold names relative to those directories.

-- Takes: one generated audio + its recipe (core.Take).
CREATE TABLE takes (
  id            TEXT PRIMARY KEY,
  created_at    INTEGER NOT NULL,
  file          TEXT NOT NULL,              -- name in clips/
  text          TEXT NOT NULL,
  model         TEXT NOT NULL,              -- repo id
  voice_id      TEXT REFERENCES voices(id) ON DELETE SET NULL,
  project_id    TEXT REFERENCES projects(id) ON DELETE CASCADE,
  job_id        TEXT,
  starred       INTEGER NOT NULL DEFAULT 0,
  sample_rate   INTEGER NOT NULL DEFAULT 0,
  audio_seconds REAL NOT NULL DEFAULT 0,
  gen_seconds   REAL NOT NULL DEFAULT 0,
  recipe        TEXT NOT NULL               -- core.Take as JSON (the full recipe; source of truth for Reuse)
);
CREATE INDEX takes_created ON takes(created_at DESC);
CREATE INDEX takes_project ON takes(project_id, created_at);
CREATE VIRTUAL TABLE takes_fts USING fts5(text, content='takes', content_rowid='rowid');
-- + triggers keeping takes_fts in sync (insert/update/delete).

-- Voice profiles (core.Voice). Blobs in voices/<id>/.
CREATE TABLE voices (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('builtin','cloned','designed')),
  model      TEXT NOT NULL DEFAULT '',
  locked     INTEGER NOT NULL DEFAULT 0,
  consent    INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  profile    TEXT NOT NULL                 -- core.Voice as JSON
);
CREATE VIRTUAL TABLE voices_fts USING fts5(name, content='voices', content_rowid='rowid');

-- Projects (core.Project): stories, audiobooks, dubs, transcripts, workflows.
CREATE TABLE projects (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  title      TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  body       TEXT NOT NULL DEFAULT '{}'    -- module-defined JSON with its own "version"
);
CREATE INDEX projects_kind ON projects(kind, updated_at DESC);
CREATE VIRTUAL TABLE projects_fts USING fts5(title, content='projects', content_rowid='rowid');

-- Segments of a project (story lines, dub cues, audiobook sentences) — M3/M4.
CREATE TABLE segments (
  id          TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  ord         REAL NOT NULL,               -- fractional ordering, cheap inserts
  text        TEXT NOT NULL,
  start_ms    INTEGER,                     -- dub/transcript timing
  end_ms      INTEGER,
  speaker     TEXT,
  voice_id    TEXT REFERENCES voices(id) ON DELETE SET NULL,
  take_id     TEXT REFERENCES takes(id) ON DELETE SET NULL,  -- current render
  input_hash  TEXT,                        -- content-addressed cache key of the render
  props       TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX segments_project ON segments(project_id, ord);

-- Jobs (jobs.Job) and their event log.
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
  params       TEXT NOT NULL,              -- JSON
  progress     TEXT,                       -- JSON jobs.Progress (last)
  result       TEXT,                       -- JSON
  error        TEXT,                       -- JSON jobs.Error
  created_at   INTEGER NOT NULL,
  started_at   INTEGER,
  finished_at  INTEGER
);
CREATE INDEX jobs_state ON jobs(state, lane, priority DESC, created_at);
CREATE INDEX jobs_owner ON jobs(owner);
CREATE TABLE job_deps (
  job_id   TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  after_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  PRIMARY KEY (job_id, after_id)
);
CREATE TABLE job_events (                  -- state changes, for diagnostics (not progress ticks)
  job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  at     INTEGER NOT NULL,
  state  TEXT NOT NULL,
  detail TEXT
);

-- Licence acceptances (core.LicenceAcceptance).
CREATE TABLE licences (
  licence     TEXT NOT NULL,
  model       TEXT NOT NULL DEFAULT '',    -- '' = all models under the licence
  accepted_at INTEGER NOT NULL,
  PRIMARY KEY (licence, model)
);

-- Small JSON documents: "settings:app", "draft:<page>" (store.Documents).
CREATE TABLE documents (
  key        TEXT PRIMARY KEY,
  doc        TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

-- Later milestones add (sketch): pronunciation(lang, word, reading, …) M1;
-- glossary(project_id, source, target) M4; transcripts / words M2;
-- exports(project_id, path, format, created_at) M3; usage(day, metric, n) M7;
-- notifications(id, …) if the centre must survive restarts.
