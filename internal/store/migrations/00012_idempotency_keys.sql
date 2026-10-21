-- +goose Up
-- Idempotency records (ADR-0022). Written in the same transaction as the mutation, so
-- a row exists exactly when the mutation committed. Never holds a response: only the
-- hash of the request with its redacted fields cleared, and what the request produced.
CREATE TABLE idempotency_keys (
    tenant_id       uuid NOT NULL REFERENCES tenants_heads (id),
    principal_id    uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    -- Full RPC name, e.g. 'bohux.v1.ProjectService/CreateProject'.
    rpc             text NOT NULL,
    request_hash    bytea NOT NULL,
    resource_type   text NOT NULL,
    resource_id     uuid NOT NULL,
    revision_id     uuid NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, principal_id, idempotency_key)
);

-- For the expiry cleanup job.
CREATE INDEX idempotency_keys_expiry ON idempotency_keys (expires_at);

ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON idempotency_keys
    USING (tenant_id = bohux_current_tenant())
    WITH CHECK (tenant_id = bohux_current_tenant());

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
DROP TABLE idempotency_keys;
