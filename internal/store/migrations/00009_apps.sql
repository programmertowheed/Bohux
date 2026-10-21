-- +goose Up
-- App: a deployable unit (ADR-0014). Nested fields are stored as protojson.
CREATE TABLE apps_heads (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants_heads (id),
    revision_id uuid NOT NULL,
    deleted     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid NOT NULL,
    environment_id uuid NOT NULL,
    slug           text NOT NULL,
    FOREIGN KEY (tenant_id, environment_id) REFERENCES environments_heads (tenant_id, id),
    UNIQUE (tenant_id, id)
);

CREATE TABLE apps_revisions (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    resource_id        uuid NOT NULL,
    parent_revision_id uuid UNIQUE,
    actor_id           uuid NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    deleted            boolean NOT NULL DEFAULT false,
    source             jsonb NOT NULL CHECK (jsonb_typeof(source) = 'object'),
    runtime            jsonb NOT NULL CHECK (jsonb_typeof(runtime) = 'object'),
    -- [{"key": ..., "handle": ...}]: SecretRefs, never values.
    secrets            jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(secrets) = 'array'),
    UNIQUE (resource_id, id),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES apps_heads (tenant_id, id),
    -- History is linear: a parent belongs to the same resource and has one child.
    FOREIGN KEY (resource_id, parent_revision_id) REFERENCES apps_revisions (resource_id, id)
);

-- One root revision per resource.
CREATE UNIQUE INDEX apps_revisions_root ON apps_revisions (resource_id) WHERE parent_revision_id IS NULL;
CREATE INDEX apps_revisions_by_resource ON apps_revisions (resource_id, created_at DESC);

-- The head points at one of its own revisions. Deferred: a write inserts the head and
-- its revision in the same transaction (ADR-0017).
ALTER TABLE apps_heads
    ADD CONSTRAINT apps_heads_revision_fk FOREIGN KEY (id, revision_id)
    REFERENCES apps_revisions (resource_id, id) DEFERRABLE INITIALLY DEFERRED;

CREATE TRIGGER apps_revisions_immutable
    BEFORE UPDATE OR DELETE ON apps_revisions
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();
CREATE TRIGGER apps_revisions_no_truncate
    BEFORE TRUNCATE ON apps_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE apps_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE apps_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON apps_heads
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

ALTER TABLE apps_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE apps_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON apps_revisions
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

CREATE UNIQUE INDEX apps_heads_slug ON apps_heads (environment_id, slug) WHERE NOT deleted;

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
DROP TABLE apps_revisions, apps_heads;
