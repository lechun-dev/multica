#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
checker="$repo_root/scripts/check-private-migration-conflicts.sh"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/multica-migration-test.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT

git -C "$fixture" init -q
git -C "$fixture" config user.email "migration-check@example.test"
git -C "$fixture" config user.name "Migration Check"
mkdir -p "$fixture/server/migrations"
printf '%s\n' 'SELECT 1;' >"$fixture/server/migrations/001_baseline.up.sql"
printf '%s\n' 'SELECT 1;' >"$fixture/server/migrations/001_baseline.down.sql"
git -C "$fixture" add server/migrations
git -C "$fixture" commit -qm "baseline"
git -C "$fixture" branch private
git -C "$fixture" branch upstream

git -C "$fixture" switch -q private
printf '%s\n' 'SELECT 2;' >"$fixture/server/migrations/002_private.up.sql"
printf '%s\n' 'SELECT 2;' >"$fixture/server/migrations/002_private.down.sql"
git -C "$fixture" add server/migrations
git -C "$fixture" commit -qm "private migration"

git -C "$fixture" switch -q upstream
printf '%s\n' 'SELECT 3;' >"$fixture/server/migrations/003_upstream.up.sql"
printf '%s\n' 'SELECT 3;' >"$fixture/server/migrations/003_upstream.down.sql"
git -C "$fixture" add server/migrations
git -C "$fixture" commit -qm "non-conflicting upstream migration"

git -C "$fixture" switch -q private
(
  cd "$fixture"
  bash "$checker" private upstream >/dev/null
)

git -C "$fixture" switch -q upstream
git -C "$fixture" reset -q --hard HEAD^
printf '%s\n' 'SELECT 4;' >"$fixture/server/migrations/002_upstream.up.sql"
printf '%s\n' 'SELECT 4;' >"$fixture/server/migrations/002_upstream.down.sql"
git -C "$fixture" add server/migrations
git -C "$fixture" commit -qm "conflicting upstream migration"

if (cd "$fixture" && bash "$checker" private upstream >/dev/null 2>&1); then
  echo "Expected a conflicting numeric migration prefix to fail." >&2
  exit 1
fi

git -C "$fixture" switch -q private
printf '%s\n' 'SELECT 5;' >"$fixture/server/migrations/004_incomplete.up.sql"
git -C "$fixture" add server/migrations
git -C "$fixture" commit -qm "incomplete private migration"

if (cd "$fixture" && bash "$checker" private upstream >/dev/null 2>&1); then
  echo "Expected an incomplete migration pair to fail." >&2
  exit 1
fi

echo "Migration conflict checker tests passed."
