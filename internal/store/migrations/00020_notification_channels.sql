-- +goose Up
-- Deploy notification channels (ADR-0038). Fixed at creation but for deletion.
CREATE TABLE notification_channels_heads (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants_heads (id),
    revision_id uuid NOT NULL,
    deleted     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid NOT NULL,
    project_id     uuid NOT NULL,
    kind           text NOT NULL CHECK (kind IN ('slack', 'discord', 'webhook')),
    -- Terminal deployment states to post, lowercase (data-model.md enum rule).
    states         text[] NOT NULL CHECK (cardinality(states) > 0
                       AND states <@ ARRAY['succeeded', 'failed', 'rolled_back', 'cancelled']),
    -- The URL's OpenBao handle (it is a credential) and its host, for display.
    url_handle     text NOT NULL UNIQUE,
    url_host       text NOT NULL,
    -- WEBHOOK channels: the signing secret's handle.
    signing_handle text UNIQUE,
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects_heads (tenant_id, id),
    UNIQUE (tenant_id, id)
);

CREATE TABLE notification_channels_revisions (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    resource_id        uuid NOT NULL,
    parent_revision_id uuid UNIQUE,
    actor_id           uuid NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    deleted            boolean NOT NULL DEFAULT false,
    UNIQUE (resource_id, id),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES notification_channels_heads (tenant_id, id),
    -- History is linear: a parent belongs to the same resource and has one child.
    FOREIGN KEY (resource_id, parent_revision_id) REFERENCES notification_channels_revisions (resource_id, id)
);

-- One root revision per resource.
CREATE UNIQUE INDEX notification_channels_revisions_root ON notification_channels_revisions (resource_id) WHERE parent_revision_id IS NULL;
CREATE INDEX notification_channels_revisions_by_resource ON notification_channels_revisions (resource_id, created_at DESC);

-- The head points at one of its own revisions. Deferred: a write inserts the head and
-- its revision in the same transaction (ADR-0017).
ALTER TABLE notification_channels_heads
    ADD CONSTRAINT notification_channels_heads_revision_fk FOREIGN KEY (id, revision_id)
    REFERENCES notification_channels_revisions (resource_id, id) DEFERRABLE INITIALLY DEFERRED;

CREATE TRIGGER notification_channels_revisions_immutable
    BEFORE UPDATE OR DELETE ON notification_channels_revisions
    FOR EACH ROW EXECUTE FUNCTION bohux_forbid_change();
CREATE TRIGGER notification_channels_revisions_no_truncate
    BEFORE TRUNCATE ON notification_channels_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION bohux_forbid_change();

ALTER TABLE notification_channels_heads ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_channels_heads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notification_channels_heads
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

ALTER TABLE notification_channels_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_channels_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notification_channels_revisions
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

CREATE INDEX notification_channels_heads_project ON notification_channels_heads (project_id) WHERE NOT deleted;

-- +goose Down
-- Reversible: drops what Up created. The channels are lost (their URLs stay in OpenBao
-- until destroyed there).
DROP TABLE notification_channels_revisions, notification_channels_heads;
