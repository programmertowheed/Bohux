-- +goose Up
-- Environment: where a project's apps run, and its shared non-secret config.
CREATE TABLE environments_heads (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants_heads (id),
    revision_id uuid NOT NULL,
    deleted     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid NOT NULL,
    project_id  uuid NOT NULL,
    slug        text NOT NULL,
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects_heads (tenant_id, id),
    UNIQUE (tenant_id, id)
);

CREATE TABLE environments_revisions (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    resource_id        uuid NOT NULL,
    parent_revision_id uuid UNIQUE,
    actor_id           uuid NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    deleted            boolean NOT NULL DEFAULT false,
    provider           text NOT NULL,
    -- ProviderBinding.target: exactly one of node_id and target_ref.
    node_id            uuid,
    target_ref         text,
    config             jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(config) = 'object'),
    FOREIGN KEY (tenant_id, node_id) REFERENCES nodes_heads (tenant_id, id),
    CHECK ((node_id IS NULL) <> (target_ref IS NULL)),
    UNIQUE (resource_id, id),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES environments_heads (tenant_id, id),
    -- History is linear: a parent belongs to the same resource and has one child.
    FOREIGN KEY (resource_id, parent_revision_id) REFERENCES environments_revisions (resource_id, id)
);

-- One root revision per resource.
CREATE UNIQUE INDEX environments_revisions_root ON environments_revisions (resource_id) WHERE parent_revision_id IS NULL;
CREATE INDEX environments_revisions_by_resource ON environments_revisions (resource_id, created_at DESC);

-- The head points at one of its own revisions. Deferred: a write inserts the head and
-- its revision in the same transaction (ADR-0017).
ALTER TABLE environments_heads
    ADD CONSTRAINT environments_heads_revision_fk FOREIGN KEY (id, revision_id)
    REFERENCES environments_revisions (resource_id, id) DEFERRABLE INITIALLY DEFERRED;

CREATE TRIGGER environments_revisions_immutable
    BEFORE UPDATE OR DELETE ON environments_revisions
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();
CREATE TRIGGER environments_revisions_no_truncate
    BEFORE TRUNCATE ON environments_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE environments_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE environments_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON environments_heads
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

ALTER TABLE environments_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE environments_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON environments_revisions
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

CREATE UNIQUE INDEX environments_heads_slug
    ON environments_heads (project_id, slug) WHERE NOT deleted;

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
DROP TABLE environments_revisions, environments_heads;
