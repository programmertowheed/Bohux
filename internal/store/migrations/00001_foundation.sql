-- +goose Up
-- Helpers every tenant-scoped table uses. See docs/specs/data-model.md, "Schema rules".

-- The tenant of the current transaction, set by the store with
-- set_config('bohux.tenant_id', <uuid>, true). NULL when unset, so row-level security
-- matches no rows instead of erroring or matching everything.
CREATE FUNCTION bohux_current_tenant() RETURNS uuid
    LANGUAGE sql STABLE
    AS $$ SELECT NULLIF(current_setting('bohux.tenant_id', true), '')::uuid $$;

-- Revisions and audit events are append-only (ADR-0004).
-- +goose StatementBegin
CREATE FUNCTION bohux_forbid_change() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION '% on %.% is not allowed: rows are append-only',
        TG_OP, TG_TABLE_SCHEMA, TG_TABLE_NAME
        USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd

-- +goose Down
-- Reversible: drops what Up created. Data in it is lost.
DROP FUNCTION bohux_forbid_change();
DROP FUNCTION bohux_current_tenant();
