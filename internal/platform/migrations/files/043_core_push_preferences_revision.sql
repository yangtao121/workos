-- owner: core
ALTER TABLE workos_core.push_preferences
    ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0);
