CREATE TABLE books (
	id     INTEGER PRIMARY KEY,
	title  TEXT NOT NULL,
	author TEXT NOT NULL DEFAULT '',
	UNIQUE (title, author)
);

-- location is the display form from the source ("Location 120-125",
-- "page 12"). loc_start/loc_end are the numeric range used to find nearby
-- highlights during dedupe; NULL when the source gives none.
CREATE TABLE highlights (
	id             INTEGER PRIMARY KEY,
	book_id        INTEGER NOT NULL REFERENCES books (id) ON DELETE CASCADE,
	source         TEXT NOT NULL DEFAULT '',
	text           TEXT NOT NULL,
	note           TEXT NOT NULL DEFAULT '',
	location       TEXT NOT NULL DEFAULT '',
	loc_start      INTEGER,
	loc_end        INTEGER,
	highlighted_at TEXT,
	imported_at    TEXT NOT NULL,
	hash           TEXT NOT NULL,
	favorite       INTEGER NOT NULL DEFAULT 0,
	dismissed      INTEGER NOT NULL DEFAULT 0,
	last_seen      TEXT,
	weight         REAL NOT NULL DEFAULT 1.0,
	UNIQUE (book_id, hash)
);

CREATE INDEX highlights_book_loc ON highlights (book_id, loc_start);

CREATE TABLE tags (
	id   INTEGER PRIMARY KEY,
	name TEXT NOT NULL UNIQUE COLLATE NOCASE
);

CREATE TABLE highlight_tags (
	highlight_id INTEGER NOT NULL REFERENCES highlights (id) ON DELETE CASCADE,
	tag_id       INTEGER NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
	PRIMARY KEY (highlight_id, tag_id)
) WITHOUT ROWID;

CREATE INDEX highlight_tags_tag ON highlight_tags (tag_id);

-- One embedding per highlight. model records which model produced it, so a
-- model change can find and replace stale rows.
CREATE TABLE embeddings (
	highlight_id INTEGER PRIMARY KEY REFERENCES highlights (id) ON DELETE CASCADE,
	model        TEXT NOT NULL,
	dims         INTEGER NOT NULL,
	vector       BLOB NOT NULL,
	created_at   TEXT NOT NULL
);

-- The FTS table stores its own copy of book title and author because FTS5
-- external content cannot span a join. Triggers below keep it in sync.
CREATE VIRTUAL TABLE highlights_fts USING fts5 (
	text,
	note,
	title,
	author,
	tokenize = 'porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER highlights_fts_ai AFTER INSERT ON highlights BEGIN
	INSERT INTO highlights_fts (rowid, text, note, title, author)
	SELECT new.id, new.text, new.note, b.title, b.author
	FROM books b WHERE b.id = new.book_id;
END;

CREATE TRIGGER highlights_fts_ad AFTER DELETE ON highlights BEGIN
	DELETE FROM highlights_fts WHERE rowid = old.id;
END;

CREATE TRIGGER highlights_fts_au AFTER UPDATE OF text, note, book_id ON highlights BEGIN
	DELETE FROM highlights_fts WHERE rowid = old.id;
	INSERT INTO highlights_fts (rowid, text, note, title, author)
	SELECT new.id, new.text, new.note, b.title, b.author
	FROM books b WHERE b.id = new.book_id;
END;

CREATE TRIGGER books_fts_au AFTER UPDATE OF title, author ON books BEGIN
	UPDATE highlights_fts SET title = new.title, author = new.author
	WHERE rowid IN (SELECT id FROM highlights WHERE book_id = new.id);
END;
