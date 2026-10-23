-- +goose Up
-- Watch paths of a push webhook (ADR-0044): repository-relative prefixes, fixed at
-- creation like the branch. Empty: every push deploys, as before.
ALTER TABLE webhooks_heads ADD COLUMN paths text[] NOT NULL DEFAULT '{}';

-- +goose Down
-- Reversible: drops the column; webhooks then deploy on every push again.
ALTER TABLE webhooks_heads DROP COLUMN paths;
