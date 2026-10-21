-- +goose Up
-- Release: an artifact plus the App revision it runs with. Immutable in the API;
-- revisions here record only its build progressing (ADR-0010).
CREATE TABLE releases_heads (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants_heads (id),
    revision_id uuid NOT NULL,
    deleted     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid NOT NULL,
    app_id             uuid NOT NULL,
    -- The App revision captured at creation.
    config_revision_id uuid NOT NULL,
    FOREIGN KEY (tenant_id, app_id) REFERENCES apps_heads (tenant_id, id),
    FOREIGN KEY (app_id, config_revision_id) REFERENCES apps_revisions (resource_id, id),
    UNIQUE (tenant_id, id)
);

CREATE TABLE releases_revisions (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    resource_id        uuid NOT NULL,
    parent_revision_id uuid UNIQUE,
    actor_id           uuid NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    deleted            boolean NOT NULL DEFAULT false,
    state              text NOT NULL CHECK (state IN ('building', 'ready', 'failed')),
    artifact_digest    text NOT NULL DEFAULT '',
    source_ref         text NOT NULL DEFAULT '',
    provenance_ref     text NOT NULL DEFAULT '',
    sbom_ref           text NOT NULL DEFAULT '',
    build_workflow_id  text NOT NULL DEFAULT '',
    artifact_available boolean NOT NULL DEFAULT false,
    failure            jsonb,
    UNIQUE (resource_id, id),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES releases_heads (tenant_id, id),
    -- History is linear: a parent belongs to the same resource and has one child.
    FOREIGN KEY (resource_id, parent_revision_id) REFERENCES releases_revisions (resource_id, id)
);

-- One root revision per resource.
CREATE UNIQUE INDEX releases_revisions_root ON releases_revisions (resource_id) WHERE parent_revision_id IS NULL;
CREATE INDEX releases_revisions_by_resource ON releases_revisions (resource_id, created_at DESC);

-- The head points at one of its own revisions. Deferred: a write inserts the head and
-- its revision in the same transaction (ADR-0017).
ALTER TABLE releases_heads
    ADD CONSTRAINT releases_heads_revision_fk FOREIGN KEY (id, revision_id)
    REFERENCES releases_revisions (resource_id, id) DEFERRABLE INITIALLY DEFERRED;

CREATE TRIGGER releases_revisions_immutable
    BEFORE UPDATE OR DELETE ON releases_revisions
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();
CREATE TRIGGER releases_revisions_no_truncate
    BEFORE TRUNCATE ON releases_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE releases_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE releases_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON releases_heads
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

ALTER TABLE releases_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE releases_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON releases_revisions
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

CREATE INDEX releases_heads_by_app ON releases_heads (app_id, created_at DESC, id);

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
DROP TABLE releases_revisions, releases_heads;
