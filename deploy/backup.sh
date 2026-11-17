#!/bin/sh
# Run from the repository root; credentials stay inside PostgreSQL's environment.
set -eu
umask 077
backup_dir=${BACKUP_DIR:-/var/backups/hotel-parser}
mkdir -p "$backup_dir"
target="$backup_dir/hotel-parser-$(date -u +%Y%m%dT%H%M%SZ)-$$.dump"
temporary=$(mktemp "$backup_dir/.backup-XXXXXX")
trap 'rm -f "$temporary"' EXIT HUP INT TERM
docker compose --env-file "${COLLECTION_ENV_FILE:-.env.collection}" -f compose.vps.yml exec -T postgres \
    sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$temporary"
docker compose --env-file "${COLLECTION_ENV_FILE:-.env.collection}" -f compose.vps.yml exec -T postgres \
    pg_restore --list < "$temporary" > /dev/null
mv "$temporary" "$target"
sha256sum "$target" > "$target.sha256"
printf '%s\n' "$target"
