#!/usr/bin/env bash
# Applies migrations to a fresh database and runs every SQL-level test:
# schema guarantees, then each store statement prepared and executed.
#
#   PGHOST=localhost PGPORT=5432 PGUSER=postgres db/tests/run.sh
#
# Needs a superuser connection (to create the test database and roles) and Go
# (to export the store's statements).
set -euo pipefail
cd "$(dirname "$0")/../.."
DB=${LL_TEST_DB_NAME:-ledgerline_test}
psql -v ON_ERROR_STOP=1 -q -d postgres -c "DROP DATABASE IF EXISTS $DB" -c "CREATE DATABASE $DB"
for m in db/migrations/*.sql; do
  echo "applying $m"
  psql -v ON_ERROR_STOP=1 -q -d "$DB" -f "$m"
done
psql -v ON_ERROR_STOP=1 -d "$DB" -f db/tests/0001_core_test.sql
tmp=$(mktemp -d)
LL_DUMP_SQL="$tmp/prepared.sql" go test ./internal/pgstore -run TestDumpPrepared -count=1 >/dev/null
grep -v -e '^BEGIN;' -e '^ROLLBACK;' -e '^SET ROLE' -e 'set_config' "$tmp/prepared.sql" > "$tmp/prep.sql"
psql -v ON_ERROR_STOP=1 -q -d "$DB" -c "SET ROLE ll_test_app" -f "$tmp/prep.sql" -f db/tests/store_flow.sql | grep -E '^(store flow|FAIL)'
rm -rf "$tmp"
echo "SQL tests passed on $DB"
