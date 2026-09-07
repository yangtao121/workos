-- owner: indexer; ADR-0021
-- Document content, provenance, receipts and cursors remain authoritative inputs
-- for backfill. Only the regenerable feature-hash vector cache is withdrawn.
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;

ALTER TABLE workos_index.documents
    ALTER COLUMN embedding TYPE public.vector(384) USING NULL,
    ADD COLUMN embedding_model text,
    ADD CONSTRAINT documents_embedding_identity CHECK (
        (embedding IS NULL AND embedding_model IS NULL)
        OR (embedding IS NOT NULL AND embedding_model IS NOT NULL AND embedding_model ~ '^sha256:[0-9a-f]{64}$')
    ),
    ADD CONSTRAINT documents_embedding_normalized CHECK (
        embedding IS NULL OR abs(public.vector_norm(embedding) - 1) < 0.00001
    );

COMMENT ON COLUMN workos_index.documents.embedding IS
    'owner: indexer; 384-dimensional normalized model vector; NULL while awaiting model backfill';
COMMENT ON COLUMN workos_index.documents.embedding_model IS
    'owner: indexer; SHA-256 of pinned weights, tokenizer and preprocessing recipe';
