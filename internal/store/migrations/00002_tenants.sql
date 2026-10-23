-- +goose Up
-- Tenant: the root of every scope. tenant_id = id.
CREATE TABLE tenants_heads (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL CHECK (tenant_id = id),
    revision_id uuid NOT NULL,
    deleted     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid NOT NULL,
    slug        text NOT NULL,
    UNIQUE (tenant_id, id)
);

CREATE TABLE tenants_revisions (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    resource_id        uuid NOT NULL,
    parent_revision_id uuid UNIQUE,
    actor_id           uuid NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    deleted            boolean NOT NULL DEFAULT false,
    display_name       text NOT NULL DEFAULT '',
    UNIQUE (resource_id, id),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES tenants_heads (tenant_id, id),
    -- History is linear: a parent belongs to the same resource and has one child.
    FOREIGN KEY (resource_id, parent_revision_id) REFERENCES tenants_revisions (resource_id, id)
);

-- One root revision per resource.
CREATE UNIQUE INDEX tenants_revisions_root ON tenants_revisions (resource_id) WHERE parent_revision_id IS NULL;
CREATE INDEX tenants_revisions_by_resource ON tenants_revisions (resource_id, created_at DESC);

-- The head points at one of its own revisions. Deferred: a write inserts the head and
-- its revision in the same transaction (ADR-0017).
ALTER TABLE tenants_heads
    ADD CONSTRAINT tenants_heads_revision_fk FOREIGN KEY (id, revision_id)
    REFERENCES tenants_revisions (resource_id, id) DEFERRABLE INITIALLY DEFERRED;

CREATE TRIGGER tenants_revisions_immutable
    BEFORE UPDATE OR DELETE ON tenants_revisions
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();
CREATE TRIGGER tenants_revisions_no_truncate
    BEFORE TRUNCATE ON tenants_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE tenants_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenants_heads
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

ALTER TABLE tenants_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenants_revisions
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

CREATE UNIQUE INDEX tenants_heads_slug ON tenants_heads (slug) WHERE NOT deleted;

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
DROP TABLE tenants_revisions, tenants_heads;
