-- +goose Up
-- An App's SLO (ADR-0050), stored as the proto message in proto field names; NULL is
-- no SLO, which judges deploys on health alone as before.
ALTER TABLE apps_revisions ADD COLUMN slo jsonb;
ALTER TABLE deployments_revisions DROP CONSTRAINT deployments_revisions_state_check;
ALTER TABLE deployments_revisions ADD CONSTRAINT deployments_revisions_state_check CHECK (state IN (
    'pending', 'building', 'awaiting_approval', 'applying', 'health_gate', 'canary', 'verifying',
    'succeeded', 'failed', 'rolled_back', 'compensating', 'cancelled'
));

-- +goose Down
-- Reversible while no Deployment is in 'verifying'; Apps lose their SLOs.
ALTER TABLE deployments_revisions DROP CONSTRAINT deployments_revisions_state_check;
ALTER TABLE deployments_revisions ADD CONSTRAINT deployments_revisions_state_check CHECK (state IN (
    'pending', 'building', 'awaiting_approval', 'applying', 'health_gate', 'canary',
    'succeeded', 'failed', 'rolled_back', 'compensating', 'cancelled'
));
ALTER TABLE apps_revisions DROP COLUMN slo;
