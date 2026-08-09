#!/bin/sh
set -eu

mkdir -p "$PGDATA"
chown -R postgres:postgres "$PGDATA"
printf '%s\n' "${POSTGRES_PRIMARY_HOST}:5432:*:${POSTGRES_REPLICATION_USER}:${POSTGRES_REPLICATION_PASSWORD}" > "$PGPASSFILE"
chown postgres:postgres "$PGPASSFILE"
chmod 600 "$PGPASSFILE"

if [ ! -s "$PGDATA/PG_VERSION" ]; then
  if [ -n "$(ls -A "$PGDATA" 2>/dev/null)" ]; then
    echo "Replica data directory is non-empty but not initialized" >&2
    exit 1
  fi
  until pg_isready -h "$POSTGRES_PRIMARY_HOST" -U "$POSTGRES_REPLICATION_USER" >/dev/null 2>&1; do
    sleep 1
  done
  gosu postgres pg_basebackup \
    -h "$POSTGRES_PRIMARY_HOST" \
    -U "$POSTGRES_REPLICATION_USER" \
    -D "$PGDATA" \
    -Fp -Xs -P -R
fi

exec docker-entrypoint.sh postgres -c hot_standby=on
