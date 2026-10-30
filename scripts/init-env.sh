#!/usr/bin/env bash
# Creates .env.<mode> from its example file and fills in a new PASETO_KEY.
# Never overwrites an existing file.   Usage: scripts/init-env.sh [development|production]
set -euo pipefail

mode="${1:-development}"
case "$mode" in
  development|production) ;;
  *) echo "mode must be development or production" >&2; exit 2 ;;
esac

cd "$(dirname "$0")/.."
src=".env.${mode}.example"
dst=".env.${mode}"

[ -f "$src" ] || { echo "$src not found" >&2; exit 1; }
if [ -e "$dst" ]; then
  echo "$dst already exists, not overwriting"
  exit 0
fi

key="$(go run ./cmd/cli keygen)"
umask 077                                   # only the owner may read
sed "s|^PASETO_KEY=.*|PASETO_KEY=${key}|" "$src" > "$dst"
chmod 600 "$dst"
echo "created: $dst (permissions 600), new PASETO_KEY filled in"

if [ "$mode" = production ]; then
  echo "WARNING: fill in DB_HOST, DB_USER, DB_PASSWORD, DB_NAME. On a real server, keep secrets in a secret manager, not in a file." >&2
fi
