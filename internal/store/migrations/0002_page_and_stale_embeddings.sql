-- page holds the source's page as written ("138", "xvi"). CrossPoint gives
-- no locations, so dedupe falls back to the page to find nearby highlights.
ALTER TABLE highlights ADD COLUMN page TEXT NOT NULL DEFAULT '';

CREATE INDEX highlights_book_page ON highlights (book_id, page);

-- When an import replaces a highlight's text with a newer version, its
-- embedding no longer matches. Deleting it lets `cmpl embed` pick it up.
CREATE TRIGGER highlights_embedding_stale AFTER UPDATE OF text ON highlights
WHEN old.text IS NOT new.text BEGIN
	DELETE FROM embeddings WHERE highlight_id = new.id;
END;
