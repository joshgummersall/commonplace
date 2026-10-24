-- "Dismissed" was unclear: the highlight is only hidden from Daily and
-- stays searchable. The UI calls it burying; the column now matches.
ALTER TABLE highlights RENAME COLUMN dismissed TO buried;
