-- The daily shuffle is chosen once per day and stored, so the set stays
-- the same all day even as viewing it updates last_seen and imports add
-- highlights.
CREATE TABLE daily_picks (
	day          TEXT NOT NULL, -- local date, YYYY-MM-DD
	position     INTEGER NOT NULL,
	highlight_id INTEGER NOT NULL REFERENCES highlights (id) ON DELETE CASCADE,
	PRIMARY KEY (day, position)
) WITHOUT ROWID;
