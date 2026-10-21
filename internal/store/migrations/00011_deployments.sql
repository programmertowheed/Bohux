-- +goose Up
-- Deployment: one attempt to reconcile a Release onto an Environment (ADR-0002).
-- Its state is driven by the workflow; every transition is a revision.
CREATE TABLE deployments_heads (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants_heads (id),
    revision_id uuid NOT NULL,
    deleted     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid NOT NULL,
    release_id     uuid NOT NULL,
    environment_id uuid NOT NULL,
    strategy       text NOT NULL CHECK (strategy IN ('recreate')),
    rollback_of    uuid,
    reason         text NOT NULL DEFAULT '',
    FOREIGN KEY (tenant_id, release_id) REFERENCES releases_heads (tenant_id, id),
    FOREIGN KEY (tenant_id, environment_id) REFERENCES environments_heads (tenant_id, id),
    FOREIGN KEY (tenant_id, rollback_of) REFERENCES deployments_heads (tenant_id, id),
    UNIQUE (tenant_id, id)
);

CREATE TABLE deployments_revisions (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    resource_id        uuid NOT NULL,
    parent_revision_id uuid UNIQUE,
    actor_id           uuid NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    deleted            boolean NOT NULL DEFAULT false,
    state              text NOT NULL CHECK (state IN (
        'pending', 'building', 'awaiting_approval', 'applying', 'health_gate',
        'succeeded', 'failed', 'rolled_back', 'compensating', 'cancelled'
    )),
    workflow_id        text NOT NULL DEFAULT '',
    started_at         timestamptz,
    finished_at        timestamptz,
    failure            jsonb,
    UNIQUE (resource_id, id),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES deployments_heads (tenant_id, id),
    -- History is linear: a parent belongs to the same resource and has one child.
    FOREIGN KEY (resource_id, parent_revision_id) REFERENCES deployments_revisions (resource_id, id)
);

-- One root revision per resource.
CREATE UNIQUE INDEX deployments_revisions_root ON deployments_revisions (resource_id) WHERE parent_revision_id IS NULL;
CREATE INDEX deployments_revisions_by_resource ON deployments_revisions (resource_id, created_at DESC);

-- The head points at one of its own revisions. Deferred: a write inserts the head and
-- its revision in the same transaction (ADR-0017).
ALTER TABLE deployments_heads
    ADD CONSTRAINT deployments_heads_revision_fk FOREIGN KEY (id, revision_id)
    REFERENCES deployments_revisions (resource_id, id) DEFERRABLE INITIALLY DEFERRED;

CREATE TRIGGER deployments_revisions_immutable
    BEFORE UPDATE OR DELETE ON deployments_revisions
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();
CREATE TRIGGER deployments_revisions_no_truncate
    BEFORE TRUNCATE ON deployments_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE deployments_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE deployments_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON deployments_heads
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

ALTER TABLE deployments_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE deployments_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON deployments_revisions
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

CREATE INDEX deployments_heads_by_environment
    ON deployments_heads (environment_id, created_at DESC, id);

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
DROP TABLE deployments_revisions, deployments_heads;
