-- Themes group highlights that say related things, usually across books.
-- Areas (parent_id NULL) are broad; themes nest inside an area. A refresh
-- replaces every row, carrying ids and names over to the new group that
-- best matches an old one, so links and names survive. AUTOINCREMENT
-- keeps a removed theme's id from going to a new, different theme.
CREATE TABLE themes (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	parent_id INTEGER REFERENCES themes (id) ON DELETE CASCADE,
	name      TEXT NOT NULL,
	renamed   INTEGER NOT NULL DEFAULT 0, -- you named it, not the model
	position  INTEGER NOT NULL
);

CREATE INDEX themes_parent ON themes (parent_id);

-- rank orders a theme's highlights, most central (most typical) first.
CREATE TABLE theme_members (
	theme_id     INTEGER NOT NULL REFERENCES themes (id) ON DELETE CASCADE,
	highlight_id INTEGER NOT NULL REFERENCES highlights (id) ON DELETE CASCADE,
	rank         INTEGER NOT NULL,
	PRIMARY KEY (theme_id, highlight_id)
) WITHOUT ROWID;

CREATE INDEX theme_members_highlight ON theme_members (highlight_id);

-- The last successful refresh: when, and from how many highlights.
CREATE TABLE theme_refresh (
	id           INTEGER PRIMARY KEY CHECK (id = 1),
	refreshed_at TEXT NOT NULL,
	highlights   INTEGER NOT NULL
);
