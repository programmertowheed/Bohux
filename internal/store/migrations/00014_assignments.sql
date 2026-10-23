-- +goose Up
-- Node assignments (ADR-0007): the full desired state for a node, one row per
-- generation. Stored with secret handles only; values are resolved, sealed to the
-- node's key, and signed each time the assignment is sent (ADR-0008, ADR-0024), so no
-- secret value, not even sealed, lands here (rule 3).
CREATE TABLE assignments (
    tenant_id  uuid NOT NULL REFERENCES tenants_heads (id),
    node_id    uuid NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    -- Serialized bohux.agent.v1.AssignmentPayload with sealed_secrets empty.
    payload    bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid NOT NULL,
    PRIMARY KEY (node_id, generation),
    FOREIGN KEY (tenant_id, node_id) REFERENCES nodes_heads (tenant_id, id)
);

CREATE TRIGGER assignments_immutable
    BEFORE UPDATE OR DELETE ON assignments
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE assignments FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON assignments
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

-- The highest generation the agent has acknowledged. An observation, not state.
ALTER TABLE nodes_heads ADD COLUMN acked_generation bigint;

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
ALTER TABLE nodes_heads DROP COLUMN acked_generation;
DROP TABLE assignments;
