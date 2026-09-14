#!/usr/bin/env bash
set -euo pipefail

base_ref="${1:-HEAD^}"
head_ref="${2:-HEAD}"

# 2026-09-12 coder(lq): GitHub reports an all-zero before SHA for the first
# push of a branch. Fall back to the parent commit so the guard still runs.
if [[ "$base_ref" =~ ^0+$ ]]; then
  base_ref="${head_ref}^"
fi

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

for ref in "$base_ref" "$head_ref"; do
  if ! git rev-parse --verify --quiet "${ref}^{commit}" >/dev/null; then
    echo "Private feature inventory check: ref '$ref' does not exist." >&2
    exit 2
  fi
done

changed_files="$(git diff --name-only "$base_ref" "$head_ref")"

if grep -qx 'PRIVATE_FEATURES.md' <<<"$changed_files"; then
  echo "Private feature inventory check passed: PRIVATE_FEATURES.md was updated."
  exit 0
fi

# 2026-09-12 coder(lq): Treat all deployable product and integration paths as
# private-feature-sensitive. This deliberately prefers an occasional inventory
# review over silently shipping behavior that disappears in a later sync.
product_changes="$(
  grep -E '^(apps/|packages/|server/|extensions/|deploy/|docker/|\.github/workflows/)' <<<"$changed_files" \
    | grep -Ev '(^|/)(__tests__/|testdata/)|(_test\.go|\.test\.[cm]?[jt]sx?|\.spec\.[cm]?[jt]sx?)$' \
    || true
)"

if [ -z "$product_changes" ]; then
  echo "Private feature inventory check passed: no deployable product code changed."
  exit 0
fi

echo "Private feature inventory check failed: product code changed without updating PRIVATE_FEATURES.md." >&2
echo "Review the protected behavior, contract coverage, and private change register for:" >&2
sed 's/^/  - /' <<<"$product_changes" >&2
exit 1
