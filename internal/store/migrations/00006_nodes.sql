-- +goose Up
-- Node: a registered agent (ADR-0003).
CREATE TABLE nodes_heads (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants_heads (id),
    revision_id uuid NOT NULL,
    deleted     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid NOT NULL,
    slug               text NOT NULL,
    -- Public key secret values are sealed to (ADR-0008).
    sealing_public_key bytea NOT NULL,
    -- Observation, not state: updated in place, never versioned.
    last_seen_at       timestamptz,
    UNIQUE (tenant_id, id)
);

CREATE TABLE nodes_revisions (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    resource_id        uuid NOT NULL,
    parent_revision_id uuid UNIQUE,
    actor_id           uuid NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    deleted            boolean NOT NULL DEFAULT false,
    agent_version      text NOT NULL,
    protocol_version   integer NOT NULL,
    providers          jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(providers) = 'array'),
    state              text NOT NULL
        CHECK (state IN ('online', 'offline', 'degraded', 'rejected', 'revoked')),
    state_detail       jsonb,
    revoked_at         timestamptz,
    UNIQUE (resource_id, id),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES nodes_heads (tenant_id, id),
    -- History is linear: a parent belongs to the same resource and has one child.
    FOREIGN KEY (resource_id, parent_revision_id) REFERENCES nodes_revisions (resource_id, id)
);

-- One root revision per resource.
CREATE UNIQUE INDEX nodes_revisions_root ON nodes_revisions (resource_id) WHERE parent_revision_id IS NULL;
CREATE INDEX nodes_revisions_by_resource ON nodes_revisions (resource_id, created_at DESC);

-- The head points at one of its own revisions. Deferred: a write inserts the head and
-- its revision in the same transaction (ADR-0017).
ALTER TABLE nodes_heads
    ADD CONSTRAINT nodes_heads_revision_fk FOREIGN KEY (id, revision_id)
    REFERENCES nodes_revisions (resource_id, id) DEFERRABLE INITIALLY DEFERRED;

CREATE TRIGGER nodes_revisions_immutable
    BEFORE UPDATE OR DELETE ON nodes_revisions
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();
CREATE TRIGGER nodes_revisions_no_truncate
    BEFORE TRUNCATE ON nodes_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE nodes_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE nodes_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON nodes_heads
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

ALTER TABLE nodes_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE nodes_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON nodes_revisions
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

CREATE UNIQUE INDEX nodes_heads_slug ON nodes_heads (tenant_id, slug) WHERE NOT deleted;

-- One-time registration tokens. Not a versioned resource: creation and use are audit
-- events. Only the hash is stored.
CREATE TABLE node_bootstrap_tokens (
    id         uuid PRIMARY KEY,
    tenant_id  uuid NOT NULL REFERENCES tenants_heads (id),
    node_slug  text NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid NOT NULL,
    used_at    timestamptz,
    node_id    uuid,
    FOREIGN KEY (tenant_id, node_id) REFERENCES nodes_heads (tenant_id, id),
    CHECK ((used_at IS NULL) = (node_id IS NULL))
);

ALTER TABLE node_bootstrap_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE node_bootstrap_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON node_bootstrap_tokens
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
DROP TABLE node_bootstrap_tokens;
DROP TABLE nodes_revisions, nodes_heads;
