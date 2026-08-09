#!/bin/sh
set -eu

export PGPASSWORD="$POSTGRES_PASSWORD"
until pg_isready -h postgres -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null 2>&1; do
  sleep 1
done

psql -h postgres -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -v ON_ERROR_STOP=1 \
  -v repl_password="$POSTGRES_REPLICATION_PASSWORD" <<'SQL'
SELECT format('CREATE ROLE gamics_replica WITH REPLICATION LOGIN PASSWORD %L', :'repl_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gamics_replica') \gexec
SELECT format('ALTER ROLE gamics_replica WITH REPLICATION LOGIN PASSWORD %L', :'repl_password') \gexec
SQL
