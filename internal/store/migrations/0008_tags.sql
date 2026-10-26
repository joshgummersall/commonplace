-- Tags replace themes. Clustering put some highlights in groups they
-- don't belong to, and themes could only be renamed, not corrected.
-- Tags are yours to edit; the app suggests them, and you confirm or
-- remove what it suggested.
CREATE TABLE tags (
	id   INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE COLLATE NOCASE
);

-- state is 'auto' when the app suggested the tag, 'yes' when you set or
-- confirmed it, and 'no' when you removed it, so the app never suggests
-- it for that highlight again.
CREATE TABLE highlight_tags (
	highlight_id INTEGER NOT NULL REFERENCES highlights (id) ON DELETE CASCADE,
	tag_id       INTEGER NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
	state        TEXT NOT NULL CHECK (state IN ('auto', 'yes', 'no')),
	PRIMARY KEY (highlight_id, tag_id)
) WITHOUT ROWID;

CREATE INDEX highlight_tags_tag ON highlight_tags (tag_id);

-- Each area becomes a suggested tag; areas that share a name merge. The
-- themes nested inside areas are dropped.
INSERT INTO tags (name)
	SELECT name FROM themes WHERE parent_id IS NULL
	GROUP BY name COLLATE NOCASE ORDER BY min(position);

INSERT OR IGNORE INTO highlight_tags (highlight_id, tag_id, state)
	SELECT m.highlight_id, g.id, 'auto' FROM theme_members m
	JOIN themes t ON t.id = m.theme_id AND t.parent_id IS NULL
	JOIN tags g ON g.name = t.name;

DROP TABLE theme_members;
DROP TABLE themes;
DROP TABLE theme_refresh;
