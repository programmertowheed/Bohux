-- +goose Up
-- ApiToken: scoped to one project and one role. Only the hash of the token is stored.
CREATE TABLE api_tokens_heads (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants_heads (id),
    revision_id uuid NOT NULL,
    deleted     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid NOT NULL,
    project_id   uuid NOT NULL,
    role         text NOT NULL CHECK (role IN ('viewer', 'deployer', 'owner')),
    token_hash   bytea NOT NULL UNIQUE,
    expires_at   timestamptz,
    -- Observation, not state: updated in place, never versioned.
    last_used_at timestamptz,
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects_heads (tenant_id, id),
    UNIQUE (tenant_id, id)
);

CREATE TABLE api_tokens_revisions (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    resource_id        uuid NOT NULL,
    parent_revision_id uuid UNIQUE,
    actor_id           uuid NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    deleted            boolean NOT NULL DEFAULT false,
    name               text NOT NULL,
    revoked_at         timestamptz,
    UNIQUE (resource_id, id),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES api_tokens_heads (tenant_id, id),
    -- History is linear: a parent belongs to the same resource and has one child.
    FOREIGN KEY (resource_id, parent_revision_id) REFERENCES api_tokens_revisions (resource_id, id)
);

-- One root revision per resource.
CREATE UNIQUE INDEX api_tokens_revisions_root ON api_tokens_revisions (resource_id) WHERE parent_revision_id IS NULL;
CREATE INDEX api_tokens_revisions_by_resource ON api_tokens_revisions (resource_id, created_at DESC);

-- The head points at one of its own revisions. Deferred: a write inserts the head and
-- its revision in the same transaction (ADR-0017).
ALTER TABLE api_tokens_heads
    ADD CONSTRAINT api_tokens_heads_revision_fk FOREIGN KEY (id, revision_id)
    REFERENCES api_tokens_revisions (resource_id, id) DEFERRABLE INITIALLY DEFERRED;

CREATE TRIGGER api_tokens_revisions_immutable
    BEFORE UPDATE OR DELETE ON api_tokens_revisions
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();
CREATE TRIGGER api_tokens_revisions_no_truncate
    BEFORE TRUNCATE ON api_tokens_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE api_tokens_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_tokens_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON api_tokens_heads
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

ALTER TABLE api_tokens_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_tokens_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON api_tokens_revisions
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
DROP TABLE api_tokens_revisions, api_tokens_heads;
