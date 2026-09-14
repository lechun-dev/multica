#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
checker="$repo_root/scripts/check-private-feature-inventory.sh"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/multica-private-inventory-test.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT

git -C "$fixture" init -q
git -C "$fixture" config user.email "private-inventory@example.test"
git -C "$fixture" config user.name "Private Inventory Check"
mkdir -p "$fixture/server/pkg/example"
printf '%s\n' '# Private features' >"$fixture/PRIVATE_FEATURES.md"
printf '%s\n' 'package example' >"$fixture/server/pkg/example/example.go"
git -C "$fixture" add .
git -C "$fixture" commit -qm "baseline"
baseline="$(git -C "$fixture" rev-parse HEAD)"

printf '%s\n' 'package example' '' 'const Enabled = true' >"$fixture/server/pkg/example/example.go"
git -C "$fixture" add .
git -C "$fixture" commit -qm "feature without inventory"
feature_commit="$(git -C "$fixture" rev-parse HEAD)"

if (cd "$fixture" && bash "$checker" "$baseline" "$feature_commit" >/dev/null 2>&1); then
  echo "Expected product code without an inventory update to fail." >&2
  exit 1
fi

printf '%s\n' '# Private features' '' '- Added example feature.' >"$fixture/PRIVATE_FEATURES.md"
git -C "$fixture" add PRIVATE_FEATURES.md
git -C "$fixture" commit -qm "update private inventory"
inventory_commit="$(git -C "$fixture" rev-parse HEAD)"

(
  cd "$fixture"
  bash "$checker" "$baseline" "$inventory_commit" >/dev/null
)

mkdir -p "$fixture/docs"
printf '%s\n' '# Usage' >"$fixture/docs/usage.md"
git -C "$fixture" add docs/usage.md
git -C "$fixture" commit -qm "docs only"
docs_commit="$(git -C "$fixture" rev-parse HEAD)"

(
  cd "$fixture"
  bash "$checker" "$inventory_commit" "$docs_commit" >/dev/null
)

echo "Private feature inventory checker tests passed."
