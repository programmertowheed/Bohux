-- +goose Up
-- An App's provider options (ADR-0032): a JSON object the environment's provider
-- interprets, versioned with the rest of the App.
ALTER TABLE apps_revisions
    ADD COLUMN provider_options jsonb NOT NULL DEFAULT '{}'
    CHECK (jsonb_typeof(provider_options) = 'object');

-- +goose Down
-- Reversible: drops the column. The options in it are lost.
ALTER TABLE apps_revisions DROP COLUMN provider_options;
