-- Bohux must not run as a superuser: superusers bypass row-level security, so tenant
-- isolation would be silently off, and store.Migrate refuses them. This role owns the
-- Bohux schema and serves requests. Runs only when the postgres volume is first created.
CREATE ROLE bohux_app LOGIN PASSWORD 'bohux-dev';
CREATE DATABASE bohux_app OWNER bohux_app;
