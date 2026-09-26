-- Kipple schema v5 (backend-additions-round2 section 7): search with stemming. The external-content
-- FTS5 index is dropped and recreated with the porter tokenizer over unicode61 (diacritics folded),
-- a persistent bm25 rank that weights the title 4x and the author 2x, and then repopulated from the
-- item_search view. Runs once, in BEGIN IMMEDIATE, gated by user_version: a failure rolls the whole
-- file back and the old index survives, and the runner takes the pre-migration snapshot first.
--
-- The five sync triggers (item_content_fts_ai, items_fts_bd, item_content_fts_bd, items_fts_au,
-- item_content_fts_au) name items_fts in their bodies, which SQLite resolves when they fire, so they
-- survive the drop and recreate untouched and need no rewrite. The view item_search is unchanged.
--
-- Cost: 'rebuild' re-tokenizes every content_text, linear in the corpus (see docs/design.md 2.2a
-- for the measured time). Index size stays about the same.
DROP TABLE items_fts;
CREATE VIRTUAL TABLE items_fts USING fts5(
  title, author, content_text,
  content='item_search', content_rowid='id',
  tokenize='porter unicode61 remove_diacritics 2'
);
INSERT INTO items_fts(items_fts, rank) VALUES ('rank', 'bm25(4.0, 2.0, 1.0)');
INSERT INTO items_fts(items_fts) VALUES ('rebuild');
