-- AudioSmith database, migration 6: workflows (M6, thread #21).
--
-- One row per WorkflowStudio workflow:
-- * doc: the whole internal/workflow.Workflow as JSON (the graph, its
--   steps' settings and inputs; the source of truth; the store does not
--   read it), saved by the editor's autosave;
-- * run: the last run's checkpoints (internal/workflow.Run as JSON; ''
--   before the first run), saved by the run's jobs. Two columns, so the
--   editor and a run never overwrite each other's writes;
-- * the other columns are what lists show and search: the name, a summary
--   of the steps ("Start → Speak → Export"), the run's state and counts.
-- A run's files (scripts, results) live in <PathUserData>/workflows/<id>/,
-- removed by the service with the row.

CREATE TABLE workflows (
  seq        INTEGER PRIMARY KEY,
  id         TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  name       TEXT NOT NULL DEFAULT '',
  summary    TEXT NOT NULL DEFAULT '',
  state      TEXT NOT NULL DEFAULT '',
  items      INTEGER NOT NULL DEFAULT 0,
  done       INTEGER NOT NULL DEFAULT 0,
  doc        TEXT NOT NULL,
  run        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX workflows_updated ON workflows(updated_at DESC);
-- The Projects page searches workflows by name and steps (trigram, as the
-- other tables; queries under 3 characters use LIKE).
CREATE VIRTUAL TABLE workflows_fts USING fts5(name, summary, content='workflows', content_rowid='seq', tokenize='trigram');
CREATE TRIGGER workflows_fts_insert AFTER INSERT ON workflows BEGIN
  INSERT INTO workflows_fts(rowid, name, summary) VALUES (new.seq, new.name, new.summary);
END;
CREATE TRIGGER workflows_fts_delete AFTER DELETE ON workflows BEGIN
  INSERT INTO workflows_fts(workflows_fts, rowid, name, summary) VALUES ('delete', old.seq, old.name, old.summary);
END;
CREATE TRIGGER workflows_fts_update AFTER UPDATE OF name, summary ON workflows BEGIN
  INSERT INTO workflows_fts(workflows_fts, rowid, name, summary) VALUES ('delete', old.seq, old.name, old.summary);
  INSERT INTO workflows_fts(rowid, name, summary) VALUES (new.seq, new.name, new.summary);
END;
