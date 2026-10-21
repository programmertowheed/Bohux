-- +goose Up
-- Who did what, written in the same transaction as the revision it records
-- (ADR-0004). Never holds secret values (rule 3).
CREATE TABLE audit_events (
    id            uuid PRIMARY KEY,
    tenant_id     uuid NOT NULL REFERENCES tenants_heads (id),
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    actor_id      uuid NOT NULL,
    actor_kind    text NOT NULL CHECK (actor_kind IN ('user', 'api_token', 'node', 'system')),
    -- The RPC or internal operation, e.g. 'bohux.v1.AppService/UpdateApp'.
    action        text NOT NULL,
    -- Fully qualified proto message name, e.g. 'bohux.v1.App'.
    resource_type text NOT NULL,
    resource_id   uuid NOT NULL,
    -- NULL for actions that change no state, e.g. Exec.
    revision_id   uuid,
    project_id    uuid
);

CREATE INDEX audit_events_by_time ON audit_events (tenant_id, occurred_at DESC, id);
CREATE INDEX audit_events_by_resource
    ON audit_events (tenant_id, resource_type, resource_id, occurred_at DESC);
CREATE INDEX audit_events_by_project
    ON audit_events (tenant_id, project_id, occurred_at DESC) WHERE project_id IS NOT NULL;

CREATE TRIGGER audit_events_immutable
    BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();
CREATE TRIGGER audit_events_no_truncate
    BEFORE TRUNCATE ON audit_events
    FOR EACH STATEMENT EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit_events
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
DROP TABLE audit_events;
