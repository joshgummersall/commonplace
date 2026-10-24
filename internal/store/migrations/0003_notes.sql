-- A highlight has any number of dated notes, from the device or written
-- in the web app.
--
-- device_text is a device note's original text, the key re-imports match
-- on. It stays put when the note is edited, and deletes are soft
-- (deleted_at), so re-importing never brings back a device note you
-- changed or removed.
CREATE TABLE notes (
	id           INTEGER PRIMARY KEY,
	highlight_id INTEGER NOT NULL REFERENCES highlights (id) ON DELETE CASCADE,
	source       TEXT NOT NULL, -- 'kindle', 'crosspoint', or 'user'
	body         TEXT NOT NULL,
	device_text  TEXT,
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL,
	deleted_at   TEXT
);

CREATE INDEX notes_highlight ON notes (highlight_id, created_at);
CREATE UNIQUE INDEX notes_device ON notes (highlight_id, device_text) WHERE device_text IS NOT NULL;

INSERT INTO notes (highlight_id, source, body, device_text, created_at, updated_at)
SELECT id, source, note, note, coalesce(highlighted_at, imported_at), imported_at
FROM highlights WHERE note != '';

-- Rebuild the FTS table with a notes column fed from the notes table.
DROP TRIGGER highlights_fts_ai;
DROP TRIGGER highlights_fts_ad;
DROP TRIGGER highlights_fts_au;
DROP TRIGGER books_fts_au;
DROP TABLE highlights_fts;

ALTER TABLE highlights DROP COLUMN note;

CREATE VIRTUAL TABLE highlights_fts USING fts5 (
	text,
	notes,
	title,
	author,
	tokenize = 'porter unicode61 remove_diacritics 2'
);

-- The query building a highlight's FTS row is repeated in each trigger
-- below; SQLite has no stored procedures.
INSERT INTO highlights_fts (rowid, text, notes, title, author)
SELECT h.id, h.text,
	coalesce((SELECT group_concat(n.body, char(10)) FROM notes n
		WHERE n.highlight_id = h.id AND n.deleted_at IS NULL), ''),
	b.title, b.author
FROM highlights h JOIN books b ON b.id = h.book_id;

CREATE TRIGGER highlights_fts_ai AFTER INSERT ON highlights BEGIN
	INSERT INTO highlights_fts (rowid, text, notes, title, author)
	SELECT h.id, h.text,
		coalesce((SELECT group_concat(n.body, char(10)) FROM notes n
			WHERE n.highlight_id = h.id AND n.deleted_at IS NULL), ''),
		b.title, b.author
	FROM highlights h JOIN books b ON b.id = h.book_id WHERE h.id = new.id;
END;

CREATE TRIGGER highlights_fts_ad AFTER DELETE ON highlights BEGIN
	DELETE FROM highlights_fts WHERE rowid = old.id;
END;

CREATE TRIGGER highlights_fts_au AFTER UPDATE OF text, book_id ON highlights BEGIN
	DELETE FROM highlights_fts WHERE rowid = old.id;
	INSERT INTO highlights_fts (rowid, text, notes, title, author)
	SELECT h.id, h.text,
		coalesce((SELECT group_concat(n.body, char(10)) FROM notes n
			WHERE n.highlight_id = h.id AND n.deleted_at IS NULL), ''),
		b.title, b.author
	FROM highlights h JOIN books b ON b.id = h.book_id WHERE h.id = new.id;
END;

CREATE TRIGGER books_fts_au AFTER UPDATE OF title, author ON books BEGIN
	UPDATE highlights_fts SET title = new.title, author = new.author
	WHERE rowid IN (SELECT id FROM highlights WHERE book_id = new.id);
END;

CREATE TRIGGER notes_fts_ai AFTER INSERT ON notes BEGIN
	UPDATE highlights_fts SET notes = coalesce((SELECT group_concat(n.body, char(10)) FROM notes n
		WHERE n.highlight_id = new.highlight_id AND n.deleted_at IS NULL), '')
	WHERE rowid = new.highlight_id;
END;

-- Covers edits, soft deletes, and moves between highlights (merges).
CREATE TRIGGER notes_fts_au AFTER UPDATE ON notes BEGIN
	UPDATE highlights_fts SET notes = coalesce((SELECT group_concat(n.body, char(10)) FROM notes n
		WHERE n.highlight_id = old.highlight_id AND n.deleted_at IS NULL), '')
	WHERE rowid = old.highlight_id;
	UPDATE highlights_fts SET notes = coalesce((SELECT group_concat(n.body, char(10)) FROM notes n
		WHERE n.highlight_id = new.highlight_id AND n.deleted_at IS NULL), '')
	WHERE rowid = new.highlight_id;
END;

CREATE TRIGGER notes_fts_ad AFTER DELETE ON notes BEGIN
	UPDATE highlights_fts SET notes = coalesce((SELECT group_concat(n.body, char(10)) FROM notes n
		WHERE n.highlight_id = old.highlight_id AND n.deleted_at IS NULL), '')
	WHERE rowid = old.highlight_id;
END;
