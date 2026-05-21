#!/usr/bin/env bash
#
# migrate-ntfs-safe-op-filenames.sh
#
# One-shot rename of `.act/ops/<id>/<YYYY-MM>/*.json` files from the legacy
# colon-bearing ISO-8601 timestamp form to the NTFS-safe dashed form.
#
# Pre-fix:  2026-05-16T02:12:59.503Z-380ec44c-create.json
# Post-fix: 2026-05-16T02-12-59.503Z-380ec44c-create.json
#
# Colons are illegal in Windows filenames, so any repo that commits its
# `.act/` directory fails `actions/checkout` on `windows-latest` before any
# build step runs (act-2d98). The act writer started emitting the dashed
# form in act-2f3d; this script fixes the on-disk history of repos that
# accumulated colon-form files prior to that change.
#
# Behavior:
#   - Auto-detects which git tree tracks `.act/ops/`:
#       * nested layout (`.act/.git/` present): renames operate on the
#         nested repo via `git -C .act` semantics.
#       * host-tracked layout: renames operate on the host repo.
#   - Uses `git mv` so blame/history follows the rename.
#   - Idempotent: zero matching files -> exits 0 with a no-op message.
#   - Does NOT commit. Stages renames only; review with `git status` and
#     commit yourself with whatever message your project requires.
#
# Flags:
#   --dry-run    list what would be renamed; touch nothing.
#   --help       show this header.
#
# Exit codes:
#   0  success (including the no-op case)
#   1  setup error (no .act/ops found, no git tree tracks the ops dir, etc.)
#   2  unexpected rename failure (a `git mv` returned non-zero)

set -euo pipefail

usage() {
  sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
}

DRY_RUN=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage >&2; exit 1 ;;
  esac
  shift
done

# Walk up from CWD until we find a directory containing .act/ops/.
find_project_root() {
  d=$(pwd)
  while [ "$d" != "/" ]; do
    if [ -d "$d/.act/ops" ]; then
      printf '%s\n' "$d"
      return 0
    fi
    d=$(dirname "$d")
  done
  return 1
}

ROOT=$(find_project_root) || {
  echo "migrate-ntfs-safe: no .act/ops directory found from $(pwd) up to /" >&2
  exit 1
}

# Decide which git tree to operate against. The nested-repo layout (Phase 1
# of the coordination-plane design) means `.act/` has its own `.git`; the
# legacy host-tracked layout means `.act/ops/` lives inside the host repo's
# index. The Windows-checkout failure only bites the host-tracked layout
# (the nested repo's `.git` isn't fetched by host-side `actions/checkout`),
# but this script handles both because renaming nested-repo files is also
# safe and forward-only.
if [ -d "$ROOT/.act/.git" ]; then
  LAYOUT="nested"
  GIT_DIR="$ROOT/.act/.git"
  WORK_TREE="$ROOT/.act"
  REL_PREFIX="ops"
else
  LAYOUT="host-tracked"
  GIT_DIR="$ROOT/.git"
  WORK_TREE="$ROOT"
  REL_PREFIX=".act/ops"
fi

if [ ! -d "$GIT_DIR" ]; then
  echo "migrate-ntfs-safe: expected git dir $GIT_DIR is missing" >&2
  exit 1
fi

git_in() {
  git --git-dir="$GIT_DIR" --work-tree="$WORK_TREE" "$@"
}

# Legacy form regex: must match the exact substring
# `THH:MM:SS.sss` (case-sensitive) within an op-file basename. The hash
# and op-type fields and the .json suffix follow per spec.
LEGACY_RE='T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]\.[0-9][0-9][0-9]Z-[0-9a-f]+-[a-z_]+\.json$'

# Use a tempfile + while-read loop to stay portable to macOS bash 3.2
# (which has no `mapfile`).
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

git_in ls-files "$REL_PREFIX" 2>/dev/null | grep -E "$LEGACY_RE" > "$TMP" || true

count=$(wc -l < "$TMP" | tr -d ' ')
if [ "$count" -eq 0 ]; then
  echo "ok: no legacy (colon-form) op files tracked under $REL_PREFIX ($LAYOUT layout) — nothing to do."
  exit 0
fi

echo "migrate-ntfs-safe: layout=$LAYOUT root=$ROOT git=$GIT_DIR"
echo "migrate-ntfs-safe: $count legacy op file(s) to rename"

if [ "$DRY_RUN" -eq 1 ]; then
  echo "--- dry run; listing without renaming ---"
  while IFS= read -r path; do
    base=$(basename "$path")
    newbase=$(printf '%s' "$base" | sed -E 's/T([0-9]{2}):([0-9]{2}):([0-9]{2})\./T\1-\2-\3./')
    printf '  %s\n    -> %s\n' "$path" "$(dirname "$path")/$newbase"
  done < "$TMP"
  echo "ok: dry-run complete; $count file(s) would be renamed."
  exit 0
fi

renamed=0
skipped=0
while IFS= read -r path; do
  base=$(basename "$path")
  dir=$(dirname "$path")
  newbase=$(printf '%s' "$base" | sed -E 's/T([0-9]{2}):([0-9]{2}):([0-9]{2})\./T\1-\2-\3./')
  if [ "$newbase" = "$base" ]; then
    echo "  skip: regex transform produced no change for $base" >&2
    skipped=$((skipped + 1))
    continue
  fi
  newpath="$dir/$newbase"
  if ! git_in mv -- "$path" "$newpath"; then
    echo "migrate-ntfs-safe: git mv failed for $path -> $newpath" >&2
    exit 2
  fi
  renamed=$((renamed + 1))
done < "$TMP"

echo "ok: staged $renamed rename(s) in $LAYOUT layout (skipped=$skipped)."
echo "next: review with: git --git-dir='$GIT_DIR' --work-tree='$WORK_TREE' status"
echo "      then commit in the $LAYOUT-layout repo."
