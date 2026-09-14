#!/usr/bin/env bash
set -euo pipefail

upstream_remote="${UPSTREAM_REMOTE:-origin}"
private_remote="${PRIVATE_REMOTE:-private}"
upstream_ref="refs/remotes/${upstream_remote}/main"
private_ref="${PRIVATE_REF:-HEAD}"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

if [ -n "$(git status --porcelain)" ]; then
  echo "Upstream check requires a clean worktree so uncommitted private behavior is not omitted." >&2
  exit 2
fi

current_branch="$(git branch --show-current)"
configured_upstream="$(git rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null || true)"
if [ "$current_branch" = "main" ] && [ "$configured_upstream" != "private/main" ]; then
  echo "Upstream check: local main must track private/main (currently '${configured_upstream:-unset}')." >&2
  echo "Configure it with: git branch --set-upstream-to=private/main main" >&2
  exit 2
fi

for remote in "$upstream_remote" "$private_remote"; do
  if ! git remote get-url "$remote" >/dev/null 2>&1; then
    echo "Upstream check: remote '$remote' does not exist." >&2
    exit 2
  fi
done

echo "==> Fetching private and official main branches"
git fetch --no-tags "$private_remote" main
git fetch --no-tags "$upstream_remote" main

read -r private_ahead private_behind <<EOF
$(git rev-list --left-right --count "$private_ref...$upstream_ref")
EOF
echo "Private HEAD is $private_ahead commit(s) ahead of and $private_behind commit(s) behind official main."

bash scripts/check-private-migration-conflicts.sh "$private_ref" "$upstream_ref"

worktree_dir="$(mktemp -d "${TMPDIR:-/tmp}/multica-upstream-check.XXXXXX")"
rmdir "$worktree_dir"
worktree_added=0
cleanup() {
  if [ "$worktree_added" -eq 1 ]; then
    git worktree remove --force "$worktree_dir" >/dev/null 2>&1 || true
  elif [ -d "$worktree_dir" ]; then
    rm -rf "$worktree_dir"
  fi
}
trap cleanup EXIT

# 2026-09-12 coder(lq): Simulate the official merge in a detached temporary
# worktree so the check can exercise the real merge result without moving or
# modifying the developer's private main checkout.
git worktree add --quiet --detach "$worktree_dir" "$private_ref"
worktree_added=1

echo "==> Simulating merge of $upstream_ref"
if ! git -C "$worktree_dir" merge --no-commit --no-ff "$upstream_ref"; then
  echo "Upstream merge simulation found conflicts:" >&2
  git -C "$worktree_dir" diff --name-only --diff-filter=U | sed 's/^/  - /' >&2
  exit 1
fi

echo "==> Running private behavior contracts on the simulated merge"
bash "$worktree_dir/scripts/test-private-contracts.sh"

echo "Upstream synchronization check passed; the current checkout was not modified."
