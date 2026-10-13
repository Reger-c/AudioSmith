-- AudioSmith database, migration 2: the voice library (M1, thread V).
--
-- * voices.search: the text the library searches besides the name (style,
--   transcript, description, tags), copied from the profile JSON when the
--   row is written; voices_fts now indexes both.
-- * voices.origin: where a voice came from ("gallery:<id>", "asvoice:<id>"),
--   so "Use voice" in the gallery finds the profile it saved before.
-- * favorites: gallery favorites (and later community voices), by scope.
-- * segments_voice: "where this voice has been used" counts segments.

ALTER TABLE voices ADD COLUMN search TEXT NOT NULL DEFAULT '';
ALTER TABLE voices ADD COLUMN origin TEXT NOT NULL DEFAULT '';
CREATE INDEX voices_origin ON voices(origin);

DROP TRIGGER voices_fts_insert;
DROP TRIGGER voices_fts_delete;
DROP TRIGGER voices_fts_update;
DROP TABLE voices_fts;
CREATE VIRTUAL TABLE voices_fts USING fts5(name, search, content='voices', content_rowid='seq', tokenize='trigram');
CREATE TRIGGER voices_fts_insert AFTER INSERT ON voices BEGIN
  INSERT INTO voices_fts(rowid, name, search) VALUES (new.seq, new.name, new.search);
END;
CREATE TRIGGER voices_fts_delete AFTER DELETE ON voices BEGIN
  INSERT INTO voices_fts(voices_fts, rowid, name, search) VALUES ('delete', old.seq, old.name, old.search);
END;
CREATE TRIGGER voices_fts_update AFTER UPDATE OF name, search ON voices BEGIN
  INSERT INTO voices_fts(voices_fts, rowid, name, search) VALUES ('delete', old.seq, old.name, old.search);
  INSERT INTO voices_fts(rowid, name, search) VALUES (new.seq, new.name, new.search);
END;
INSERT INTO voices_fts(voices_fts) VALUES ('rebuild');

CREATE INDEX segments_voice ON segments(voice_id);

CREATE TABLE favorites (
  scope      TEXT NOT NULL,
  key        TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (scope, key)
);
