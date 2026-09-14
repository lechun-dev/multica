#!/usr/bin/env bash
set -euo pipefail

private_ref="${1:-HEAD}"
upstream_ref="${2:-origin/main}"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

for ref in "$private_ref" "$upstream_ref"; do
  if ! git rev-parse --verify --quiet "${ref}^{commit}" >/dev/null; then
    echo "Migration conflict check: ref '$ref' does not exist." >&2
    exit 2
  fi
done

merge_base="$(git merge-base "$private_ref" "$upstream_ref")"
temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/multica-migrations.XXXXXX")"
trap 'rm -rf "$temp_dir"' EXIT

collect_migrations() {
  local ref="$1"
  local output="$2"

  git diff --name-only --diff-filter=AR "$merge_base..$ref" -- server/migrations \
    | awk -F/ '
        $1 == "server" && $2 == "migrations" {
          file = $3
          if (file !~ /^[0-9]+_.+\.(up|down)\.sql$/) next
          id = file
          sub(/_.*/, "", id)
          stem = file
          sub(/^[0-9]+_/, "", stem)
          sub(/\.(up|down)\.sql$/, "", stem)
          direction = file ~ /\.up\.sql$/ ? "up" : "down"
          print id "\t" stem "\t" direction
        }
      ' \
    | sort -u >"$output"
}

check_pairs() {
  local label="$1"
  local input="$2"

  if ! awk -F '\t' '
      {
        key = $1 SUBSEP $2
        if ($3 == "up") up[key] = 1
        if ($3 == "down") down[key] = 1
        display[key] = $1 "_" $2
      }
      END {
        failed = 0
        for (key in display) {
          if (!up[key] || !down[key]) {
            print display[key] " is missing its matching .up.sql or .down.sql file"
            failed = 1
          }
        }
        exit failed
      }
    ' "$input" >"$temp_dir/pair-errors"; then
    echo "Migration conflict check: incomplete $label migration pair:" >&2
    sed 's/^/  - /' "$temp_dir/pair-errors" >&2
    return 1
  fi
}

private_file="$temp_dir/private.tsv"
upstream_file="$temp_dir/upstream.tsv"
collect_migrations "$private_ref" "$private_file"
collect_migrations "$upstream_ref" "$upstream_file"

check_pairs "private" "$private_file"
check_pairs "upstream" "$upstream_file"

# 2026-09-12 coder(lq): Historical upstream releases already contain repeated
# numeric prefixes. Compare only migrations independently added after the two
# branches diverged so the guard catches future private/upstream collisions
# without retroactively rejecting the official migration history.
if ! awk -F '\t' '
    FNR == NR {
      private_stems[$1 SUBSEP $2] = 1
      private_ids[$1] = 1
      next
    }
    {
      upstream_stems[$1 SUBSEP $2] = 1
      upstream_ids[$1] = 1
    }
    END {
      failed = 0
      for (id in private_ids) {
        if (!upstream_ids[id]) continue
        incompatible = 0
        for (key in private_stems) {
          split(key, parts, SUBSEP)
          if (parts[1] == id && !upstream_stems[key]) incompatible = 1
        }
        for (key in upstream_stems) {
          split(key, parts, SUBSEP)
          if (parts[1] == id && !private_stems[key]) incompatible = 1
        }
        if (incompatible) {
          print id
          failed = 1
        }
      }
      exit failed
    }
  ' "$private_file" "$upstream_file" >"$temp_dir/collisions"; then
  echo "Migration conflict check: private and upstream added different migrations with the same numeric prefix:" >&2
  while IFS= read -r id; do
    echo "  prefix $id" >&2
    awk -F '\t' -v id="$id" '$1 == id {print "    private:  " $1 "_" $2}' "$private_file" | sort -u >&2
    awk -F '\t' -v id="$id" '$1 == id {print "    upstream: " $1 "_" $2}' "$upstream_file" | sort -u >&2
  done <"$temp_dir/collisions"
  echo "Renumber the private migration before merging upstream." >&2
  exit 1
fi

echo "Migration conflict check passed for $private_ref against $upstream_ref."
