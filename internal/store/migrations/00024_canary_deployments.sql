-- +goose Up
-- Canary deploys (ADR-0049): a strategy, its weight and bake on the Deployment
-- (immutable, so on the head), and the state in which the new Release serves its share.
ALTER TABLE deployments_heads DROP CONSTRAINT deployments_heads_strategy_check;
ALTER TABLE deployments_heads ADD CONSTRAINT deployments_heads_strategy_check
    CHECK (strategy IN ('recreate', 'canary'));
ALTER TABLE deployments_heads
    ADD COLUMN canary_weight smallint,
    ADD COLUMN canary_bake_seconds integer,
    ADD CONSTRAINT deployments_heads_canary_check CHECK (
        (strategy = 'canary') = (canary_weight IS NOT NULL AND canary_bake_seconds IS NOT NULL)
        AND (canary_weight IS NULL OR canary_weight BETWEEN 1 AND 99)
        AND (canary_bake_seconds IS NULL OR canary_bake_seconds BETWEEN 60 AND 86400)
    );
ALTER TABLE deployments_revisions DROP CONSTRAINT deployments_revisions_state_check;
ALTER TABLE deployments_revisions ADD CONSTRAINT deployments_revisions_state_check CHECK (state IN (
    'pending', 'building', 'awaiting_approval', 'applying', 'health_gate', 'canary',
    'succeeded', 'failed', 'rolled_back', 'compensating', 'cancelled'
));

-- +goose Down
-- Reversible while no canary Deployment exists; delete those first, or this fails.
ALTER TABLE deployments_revisions DROP CONSTRAINT deployments_revisions_state_check;
ALTER TABLE deployments_revisions ADD CONSTRAINT deployments_revisions_state_check CHECK (state IN (
    'pending', 'building', 'awaiting_approval', 'applying', 'health_gate',
    'succeeded', 'failed', 'rolled_back', 'compensating', 'cancelled'
));
ALTER TABLE deployments_heads
    DROP CONSTRAINT deployments_heads_canary_check,
    DROP COLUMN canary_bake_seconds,
    DROP COLUMN canary_weight;
ALTER TABLE deployments_heads DROP CONSTRAINT deployments_heads_strategy_check;
ALTER TABLE deployments_heads ADD CONSTRAINT deployments_heads_strategy_check
    CHECK (strategy IN ('recreate'));
