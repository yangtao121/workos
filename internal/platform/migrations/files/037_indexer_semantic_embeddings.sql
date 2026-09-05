-- 037_indexer_semantic_embeddings.sql
-- Owner: indexer (internal/indexer, ADR-0017).
--
-- Semantic knowledge slice: deterministic 384-dim feature-hash embeddings
-- stored on the indexer-owned document projection. pgvector ANN indexing is
-- deferred: single-owner local scale is bounded (hundreds of documents per
-- project), so cosine is computed in the indexer over a bounded generation
-- fetch — deterministic and fully offline. Migrating to pgvector's vector
-- type + HNSW is a pure storage swap once scale demands it.

ALTER TABLE workos_index.documents
    ADD COLUMN embedding real[];

COMMENT ON COLUMN workos_index.documents.embedding IS
    'owner: indexer; deterministic 384-dim L2-normalized feature-hash embedding of title+content (ADR-0017)';
