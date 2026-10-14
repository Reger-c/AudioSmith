-- AudioSmith database, migration 3: transcripts (M2, thread #17).
--
-- One row per transcript: the Transcribe page's history and dictation's.
-- * doc: the whole core.Transcript as JSON (the source of truth);
--   brief: the same without word timings, which lists read (a long
--   recording has thousands of timed words).
-- * text: what search looks in (the transcript, its clean-up and the raw
--   text); transcripts_fts indexes it with the title (trigram: substring
--   search, CJK; queries under 3 characters use LIKE).
-- * audio: the WAV kept with the transcript, a file name in
--   <PathUserData>/transcripts/, deleted with the row by the repository.

CREATE TABLE transcripts (
  seq        INTEGER PRIMARY KEY,
  id         TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  source     TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  language   TEXT NOT NULL DEFAULT '',
  duration   REAL NOT NULL DEFAULT 0,
  audio      TEXT NOT NULL DEFAULT '',
  text       TEXT NOT NULL DEFAULT '',
  brief      TEXT NOT NULL,
  doc        TEXT NOT NULL
);
CREATE INDEX transcripts_created ON transcripts(created_at DESC);
CREATE INDEX transcripts_source ON transcripts(source, created_at DESC);
CREATE VIRTUAL TABLE transcripts_fts USING fts5(title, text, content='transcripts', content_rowid='seq', tokenize='trigram');
CREATE TRIGGER transcripts_fts_insert AFTER INSERT ON transcripts BEGIN
  INSERT INTO transcripts_fts(rowid, title, text) VALUES (new.seq, new.title, new.text);
END;
CREATE TRIGGER transcripts_fts_delete AFTER DELETE ON transcripts BEGIN
  INSERT INTO transcripts_fts(transcripts_fts, rowid, title, text) VALUES ('delete', old.seq, old.title, old.text);
END;
CREATE TRIGGER transcripts_fts_update AFTER UPDATE OF title, text ON transcripts BEGIN
  INSERT INTO transcripts_fts(transcripts_fts, rowid, title, text) VALUES ('delete', old.seq, old.title, old.text);
  INSERT INTO transcripts_fts(rowid, title, text) VALUES (new.seq, new.title, new.text);
END;
