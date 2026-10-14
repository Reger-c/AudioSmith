-- AudioSmith database, migration 7: phone calls (M6b, thread #23).
--
-- One row per AI phone call, placed or answered through Twilio:
-- * doc: the whole services.Call as JSON (brief, voice, disclosure,
--   transcript, timeline, per-turn latency; the source of truth; the store
--   does not read it);
-- * the other columns are what lists show, filter and search: direction,
--   the other party's number, the brief's first line, the phase, the
--   outcome and its summary, Twilio's Call SID (webhooks find calls by it),
--   the length.
-- Recordings (only when recording is on and the disclosure says so) live
-- in <PathUserData>/calls/<id>.wav, removed by the service with the row.

CREATE TABLE calls (
  seq          INTEGER PRIMARY KEY,
  id           TEXT NOT NULL UNIQUE,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  direction    TEXT NOT NULL DEFAULT 'outbound',
  number       TEXT NOT NULL DEFAULT '',
  title        TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT '',
  outcome      TEXT NOT NULL DEFAULT '',
  summary      TEXT NOT NULL DEFAULT '',
  provider_sid TEXT NOT NULL DEFAULT '',
  duration     REAL NOT NULL DEFAULT 0,
  doc          TEXT NOT NULL
);
CREATE INDEX calls_created ON calls(created_at DESC);
CREATE INDEX calls_sid ON calls(provider_sid);
-- The Projects page searches calls by brief and summary (trigram, as the
-- other tables; queries under 3 characters use LIKE).
CREATE VIRTUAL TABLE calls_fts USING fts5(title, summary, content='calls', content_rowid='seq', tokenize='trigram');
CREATE TRIGGER calls_fts_insert AFTER INSERT ON calls BEGIN
  INSERT INTO calls_fts(rowid, title, summary) VALUES (new.seq, new.title, new.summary);
END;
CREATE TRIGGER calls_fts_delete AFTER DELETE ON calls BEGIN
  INSERT INTO calls_fts(calls_fts, rowid, title, summary) VALUES ('delete', old.seq, old.title, old.summary);
END;
CREATE TRIGGER calls_fts_update AFTER UPDATE OF title, summary ON calls BEGIN
  INSERT INTO calls_fts(calls_fts, rowid, title, summary) VALUES ('delete', old.seq, old.title, old.summary);
  INSERT INTO calls_fts(rowid, title, summary) VALUES (new.seq, new.title, new.summary);
END;
