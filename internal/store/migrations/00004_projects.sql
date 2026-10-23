-- +goose Up
-- Project: the unit of grouping and RBAC.
CREATE TABLE projects_heads (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants_heads (id),
    revision_id uuid NOT NULL,
    deleted     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid NOT NULL,
    slug        text NOT NULL,
    UNIQUE (tenant_id, id)
);

CREATE TABLE projects_revisions (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    resource_id        uuid NOT NULL,
    parent_revision_id uuid UNIQUE,
    actor_id           uuid NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    deleted            boolean NOT NULL DEFAULT false,
    display_name       text NOT NULL DEFAULT '',
    UNIQUE (resource_id, id),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES projects_heads (tenant_id, id),
    -- History is linear: a parent belongs to the same resource and has one child.
    FOREIGN KEY (resource_id, parent_revision_id) REFERENCES projects_revisions (resource_id, id)
);

-- One root revision per resource.
CREATE UNIQUE INDEX projects_revisions_root ON projects_revisions (resource_id) WHERE parent_revision_id IS NULL;
CREATE INDEX projects_revisions_by_resource ON projects_revisions (resource_id, created_at DESC);

-- The head points at one of its own revisions. Deferred: a write inserts the head and
-- its revision in the same transaction (ADR-0017).
ALTER TABLE projects_heads
    ADD CONSTRAINT projects_heads_revision_fk FOREIGN KEY (id, revision_id)
    REFERENCES projects_revisions (resource_id, id) DEFERRABLE INITIALLY DEFERRED;

CREATE TRIGGER projects_revisions_immutable
    BEFORE UPDATE OR DELETE ON projects_revisions
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();
CREATE TRIGGER projects_revisions_no_truncate
    BEFORE TRUNCATE ON projects_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE projects_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE projects_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON projects_heads
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

ALTER TABLE projects_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE projects_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON projects_revisions
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

CREATE UNIQUE INDEX projects_heads_slug ON projects_heads (tenant_id, slug) WHERE NOT deleted;

-- RoleBinding: one principal, one fixed role, one project (ADR-0011).
CREATE TABLE role_bindings_heads (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants_heads (id),
    revision_id uuid NOT NULL,
    deleted     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid NOT NULL,
    project_id   uuid NOT NULL,
    principal_id uuid NOT NULL,
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects_heads (tenant_id, id),
    UNIQUE (tenant_id, id)
);

CREATE TABLE role_bindings_revisions (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    resource_id        uuid NOT NULL,
    parent_revision_id uuid UNIQUE,
    actor_id           uuid NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    deleted            boolean NOT NULL DEFAULT false,
    role               text NOT NULL CHECK (role IN ('viewer', 'deployer', 'owner')),
    UNIQUE (resource_id, id),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES role_bindings_heads (tenant_id, id),
    -- History is linear: a parent belongs to the same resource and has one child.
    FOREIGN KEY (resource_id, parent_revision_id) REFERENCES role_bindings_revisions (resource_id, id)
);

-- One root revision per resource.
CREATE UNIQUE INDEX role_bindings_revisions_root ON role_bindings_revisions (resource_id) WHERE parent_revision_id IS NULL;
CREATE INDEX role_bindings_revisions_by_resource ON role_bindings_revisions (resource_id, created_at DESC);

-- The head points at one of its own revisions. Deferred: a write inserts the head and
-- its revision in the same transaction (ADR-0017).
ALTER TABLE role_bindings_heads
    ADD CONSTRAINT role_bindings_heads_revision_fk FOREIGN KEY (id, revision_id)
    REFERENCES role_bindings_revisions (resource_id, id) DEFERRABLE INITIALLY DEFERRED;

CREATE TRIGGER role_bindings_revisions_immutable
    BEFORE UPDATE OR DELETE ON role_bindings_revisions
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();
CREATE TRIGGER role_bindings_revisions_no_truncate
    BEFORE TRUNCATE ON role_bindings_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE role_bindings_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_bindings_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON role_bindings_heads
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

ALTER TABLE role_bindings_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_bindings_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON role_bindings_revisions
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

CREATE UNIQUE INDEX role_bindings_heads_principal
    ON role_bindings_heads (project_id, principal_id) WHERE NOT deleted;

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
DROP TABLE role_bindings_revisions, role_bindings_heads;
DROP TABLE projects_revisions, projects_heads;
