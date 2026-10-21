-- +goose Up
-- The key of the environment secret an App pulls its image with (ADR-0041). A key, not
-- a value: the value is in OpenBao (rule 3).
ALTER TABLE apps_revisions ADD COLUMN image_pull_secret text NOT NULL DEFAULT '';

-- +goose Down
-- Reversible: drops the column. Apps lose the reference, not any credential.
ALTER TABLE apps_revisions DROP COLUMN image_pull_secret;
