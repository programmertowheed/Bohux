-- +goose Up
-- A token with no project is tenant-wide (ADR-0023).
ALTER TABLE api_tokens_heads ALTER COLUMN project_id DROP NOT NULL;

-- +goose Down
-- Reversible only while no tenant-wide token exists; fails otherwise, by design.
ALTER TABLE api_tokens_heads ALTER COLUMN project_id SET NOT NULL;
