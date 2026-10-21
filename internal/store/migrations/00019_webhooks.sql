-- +goose Up
-- Git push webhooks (ADR-0037). Everything but deletion is fixed at creation, so the
-- revisions record only who created and who deleted.
CREATE TABLE webhooks_heads (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants_heads (id),
    revision_id uuid NOT NULL,
    deleted     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid NOT NULL,
    app_id         uuid NOT NULL,
    branch         text NOT NULL CHECK (branch <> ''),
    flavor         text NOT NULL CHECK (flavor IN ('github', 'generic')),
    -- The signing secret's OpenBao handle. Never the secret (rule 3).
    secret_handle  text NOT NULL UNIQUE,
    FOREIGN KEY (tenant_id, app_id) REFERENCES apps_heads (tenant_id, id),
    UNIQUE (tenant_id, id)
);

CREATE TABLE webhooks_revisions (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    resource_id        uuid NOT NULL,
    parent_revision_id uuid UNIQUE,
    actor_id           uuid NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    deleted            boolean NOT NULL DEFAULT false,
    UNIQUE (resource_id, id),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES webhooks_heads (tenant_id, id),
    -- History is linear: a parent belongs to the same resource and has one child.
    FOREIGN KEY (resource_id, parent_revision_id) REFERENCES webhooks_revisions (resource_id, id)
);

-- One root revision per resource.
CREATE UNIQUE INDEX webhooks_revisions_root ON webhooks_revisions (resource_id) WHERE parent_revision_id IS NULL;
CREATE INDEX webhooks_revisions_by_resource ON webhooks_revisions (resource_id, created_at DESC);

-- The head points at one of its own revisions. Deferred: a write inserts the head and
-- its revision in the same transaction (ADR-0017).
ALTER TABLE webhooks_heads
    ADD CONSTRAINT webhooks_heads_revision_fk FOREIGN KEY (id, revision_id)
    REFERENCES webhooks_revisions (resource_id, id) DEFERRABLE INITIALLY DEFERRED;

CREATE TRIGGER webhooks_revisions_immutable
    BEFORE UPDATE OR DELETE ON webhooks_revisions
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();
CREATE TRIGGER webhooks_revisions_no_truncate
    BEFORE TRUNCATE ON webhooks_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE webhooks_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhooks_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhooks_heads
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

ALTER TABLE webhooks_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhooks_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhooks_revisions
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

CREATE INDEX webhooks_heads_app ON webhooks_heads (app_id) WHERE NOT deleted;

-- Deploys a webhook starts are audited as the webhook.
ALTER TABLE audit_events DROP CONSTRAINT audit_events_actor_kind_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_actor_kind_check
    CHECK (actor_kind IN ('user', 'api_token', 'node', 'system', 'webhook'));

-- +goose Down
-- Reversible while no webhook has acted: the narrower check fails if audit events of
-- kind 'webhook' exist, which is the point (they are history, never deleted).
ALTER TABLE audit_events DROP CONSTRAINT audit_events_actor_kind_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_actor_kind_check
    CHECK (actor_kind IN ('user', 'api_token', 'node', 'system'));
DROP TABLE webhooks_revisions, webhooks_heads;
