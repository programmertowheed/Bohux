-- +goose Up
-- Secret metadata. Handles only, never values (rule 3, ADR-0008).
CREATE TABLE secret_refs_heads (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants_heads (id),
    revision_id uuid NOT NULL,
    deleted     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid NOT NULL,
    environment_id uuid NOT NULL,
    key            text NOT NULL,
    -- Stable for the key across value changes.
    handle         text NOT NULL UNIQUE,
    FOREIGN KEY (tenant_id, environment_id) REFERENCES environments_heads (tenant_id, id),
    UNIQUE (tenant_id, id)
);

CREATE TABLE secret_refs_revisions (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    resource_id        uuid NOT NULL,
    parent_revision_id uuid UNIQUE,
    actor_id           uuid NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    deleted            boolean NOT NULL DEFAULT false,
    -- The OpenBao version the value was written as. Records that it changed, not what to.
    value_version      integer NOT NULL CHECK (value_version > 0),
    UNIQUE (resource_id, id),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES secret_refs_heads (tenant_id, id),
    -- History is linear: a parent belongs to the same resource and has one child.
    FOREIGN KEY (resource_id, parent_revision_id) REFERENCES secret_refs_revisions (resource_id, id)
);

-- One root revision per resource.
CREATE UNIQUE INDEX secret_refs_revisions_root ON secret_refs_revisions (resource_id) WHERE parent_revision_id IS NULL;
CREATE INDEX secret_refs_revisions_by_resource ON secret_refs_revisions (resource_id, created_at DESC);

-- The head points at one of its own revisions. Deferred: a write inserts the head and
-- its revision in the same transaction (ADR-0017).
ALTER TABLE secret_refs_heads
    ADD CONSTRAINT secret_refs_heads_revision_fk FOREIGN KEY (id, revision_id)
    REFERENCES secret_refs_revisions (resource_id, id) DEFERRABLE INITIALLY DEFERRED;

CREATE TRIGGER secret_refs_revisions_immutable
    BEFORE UPDATE OR DELETE ON secret_refs_revisions
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();
CREATE TRIGGER secret_refs_revisions_no_truncate
    BEFORE TRUNCATE ON secret_refs_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE secret_refs_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE secret_refs_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON secret_refs_heads
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

ALTER TABLE secret_refs_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE secret_refs_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON secret_refs_revisions
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

CREATE UNIQUE INDEX secret_refs_heads_key
    ON secret_refs_heads (environment_id, key) WHERE NOT deleted;

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
DROP TABLE secret_refs_revisions, secret_refs_heads;
