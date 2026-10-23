-- +goose Up
-- An App's routes (ADR-0034): hosts the node's ingress serves it on, versioned with
-- the rest of the App as a JSON array of bohux.v1.Route.
ALTER TABLE apps_revisions
    ADD COLUMN routes jsonb NOT NULL DEFAULT '[]'
    CHECK (jsonb_typeof(routes) = 'array');

-- Which App holds each host, across all tenants. The primary key is checked against
-- rows row-level security hides, so a tenant cannot claim another tenant's host; it
-- learns only that the host is taken. Kept equal to the live Apps' current routes by
-- the App write path, in the same transaction. Not history: the revisions are.
CREATE TABLE route_hosts (
    host      text PRIMARY KEY CHECK (host = lower(host) AND host <> ''),
    tenant_id uuid NOT NULL,
    app_id    uuid NOT NULL,
    FOREIGN KEY (tenant_id, app_id) REFERENCES apps_heads (tenant_id, id)
);
CREATE INDEX route_hosts_app ON route_hosts (app_id);

ALTER TABLE route_hosts ENABLE ROW LEVEL SECURITY;
ALTER TABLE route_hosts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON route_hosts
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

-- +goose Down
-- Reversible: drops what Up created. The routes in it are lost.
DROP TABLE route_hosts;
ALTER TABLE apps_revisions DROP COLUMN routes;
