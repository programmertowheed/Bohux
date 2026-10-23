#!/bin/sh
# First start only. Bohux runs as an unprivileged role: superusers bypass row-level
# security (store.Migrate refuses them). Temporal gets its own role, owning its two
# databases and nothing more.
set -eu
psql -v ON_ERROR_STOP=1 --username postgres <<SQL
CREATE ROLE bohux_app LOGIN PASSWORD '${BOHUX_APP_PASSWORD}';
CREATE DATABASE bohux_app OWNER bohux_app;
CREATE ROLE temporal LOGIN PASSWORD '${TEMPORAL_PASSWORD}';
CREATE DATABASE temporal OWNER temporal;
CREATE DATABASE temporal_visibility OWNER temporal;
SQL
