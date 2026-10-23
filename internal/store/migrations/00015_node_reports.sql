-- +goose Up
-- The latest StatusReport from each node's agent (ADR-0007): an observation, updated in
-- place and never versioned. The deploy workflow reads it to wait for healthy.
ALTER TABLE nodes_heads
    ADD COLUMN report_generation  bigint,
    -- bohux.agent.v1.StatusReport as protojson.
    ADD COLUMN report             jsonb,
    ADD COLUMN report_received_at timestamptz;

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
ALTER TABLE nodes_heads
    DROP COLUMN report_received_at,
    DROP COLUMN report,
    DROP COLUMN report_generation;
