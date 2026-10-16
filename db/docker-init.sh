#!/usr/bin/env bash
# Runs once when the local PostgreSQL container is first created.
# Applies migrations, creates a service login (non-superuser, so row-level
# security applies) and a demo tenant. Never use these passwords outside local dev.
set -euo pipefail
for m in /docker-entrypoint-initdb.d/migrations/*.sql; do
  psql -v ON_ERROR_STOP=1 -U postgres -d ledgerline -f "$m"
done
psql -v ON_ERROR_STOP=1 -U postgres -d ledgerline <<'SQL'
CREATE ROLE ledgerline_svc LOGIN PASSWORD 'ledgerline_svc' IN ROLE ledgerline_app;
ALTER ROLE ledgerline_svc SET search_path = ll, public;
INSERT INTO ll.tenant (id, name) VALUES ('demo', 'Demo Securities (local)');
SQL
