#!/usr/bin/env bash
set -euo pipefail

# The notification module is deliberately outside the host migration ledger.
# Keep this command explicit so a missing staging database cannot make a
# normal Multica startup fail, and so the concurrently-built index remains a
# separate statement as required by the repository migration rules.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIGRATION_DIR="$ROOT_DIR/extensions/dingtalk-notify/migrations"
ENV_FILE=".env"
ACTION="print"

usage() {
  cat <<'EOF'
Usage: scripts/dingtalk-notify-migrate.sh [--print|--apply] [env-file]

--print (default) prints the ordered SQL files without connecting to a
          database. This is safe while staging infrastructure is unavailable.
--apply   applies each file separately to DATABASE_URL. The second file must
          stay a separate psql invocation because it uses CREATE INDEX
          CONCURRENTLY.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --print) ACTION="print" ;;
    --apply) ACTION="apply" ;;
    -h|--help) usage; exit 0 ;;
    -*) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
    *) ENV_FILE="$1" ;;
  esac
  shift
done

if [ -f "$ENV_FILE" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  set +a
fi

FIRST="$MIGRATION_DIR/001_dingtalk_notify.sql"
SECOND="$MIGRATION_DIR/002_dingtalk_notify_outbox_ready_idx.sql"
for file in "$FIRST" "$SECOND"; do
  [ -f "$file" ] || { echo "Missing migration file: $file" >&2; exit 1; }
done

if [ "$ACTION" = "print" ]; then
  printf '%s\n' "-- $FIRST"
  sed 's/^/  /' "$FIRST"
  printf '%s\n' "-- $SECOND"
  sed 's/^/  /' "$SECOND"
  exit 0
fi

DATABASE_URL="${DATABASE_URL:-}"
if [ -z "$DATABASE_URL" ] || [[ "$DATABASE_URL" == *CHANGE_ME* ]]; then
  echo "DATABASE_URL must be configured before applying DingTalk notification migrations." >&2
  exit 1
fi
command -v psql >/dev/null 2>&1 || {
  echo "psql is required for --apply (install PostgreSQL client tools first)." >&2
  exit 1
}

echo "Applying DingTalk notification schema (1/2)..."
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$FIRST"
echo "Applying DingTalk notification ready index (2/2)..."
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$SECOND"
echo "DingTalk notification schema applied."
