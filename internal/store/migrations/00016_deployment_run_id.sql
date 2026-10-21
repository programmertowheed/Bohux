-- +goose Up
-- The Temporal run that drives a Deployment (ADR-0026). The workflow ID is one per App,
-- so the run ID is what names this Deployment's execution for cancel and history.
ALTER TABLE deployments_revisions ADD COLUMN workflow_run_id text NOT NULL DEFAULT '';

-- +goose Down
-- Reversible: drops the column. The run IDs in it are lost.
ALTER TABLE deployments_revisions DROP COLUMN workflow_run_id;
