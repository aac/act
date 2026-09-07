#!/usr/bin/env bash
# bench-read-after-write.sh — measure what `act list` costs on the first read
# after a write, on a large real store.
#
# act-43d11f made a read on an UNCHANGED store skip the fold. act-50d2e2 is
# about the read that follows a WRITE: it used to refold every op in the store
# to account for one new op file. This script measures the three numbers that
# distinguish those cases on the same store:
#
#   cold      — no index.db at all; the full fold-and-rebuild cost.
#   cached    — nothing changed since the last read; the act-43d11f fast path.
#   after 1 write — one op file appended, then read; act-50d2e2's target.
#
# It never touches a live tracker: the store named by SRC is COPIED to a temp
# dir, the copy's nested .act/.git is removed so no fetch/rebase can run, and
# the test op is written into the copy.
#
# Usage: scripts/bench-read-after-write.sh [/path/to/repo/with/.act] [act-binary]
set -euo pipefail

SRC=${1:-/Users/agent/Workspace/financial}
ACT=${2:-$(cd "$(dirname "$0")/.." && pwd)/bin/act-dev}

[ -d "$SRC/.act/ops" ] || { echo "no .act/ops under $SRC" >&2; exit 2; }
[ -x "$ACT" ] || { echo "not executable: $ACT" >&2; exit 2; }

WORK=$(mktemp -d "${TMPDIR:-/tmp}/act-bench.XXXXXX")
trap 'rm -rf "$WORK"' EXIT
git -C "$WORK" init -q
cp -R "$SRC/.act" "$WORK/.act"
rm -rf "$WORK/.act/.git" "$WORK/.act/index.db"

ops=$(find "$WORK/.act/ops" -name '*.json' | wc -l | tr -d ' ')
issues=$(find "$WORK/.act/ops" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')
echo "store: $SRC  ($ops ops across $issues issues)"
echo "act:   $ACT  ($("$ACT" version 2>&1 | head -1))"

run() { # run <label> — time one `act list`, print seconds
  local label=$1 t0 t1
  t0=$(python3 -c 'import time;print(time.time())')
  ( cd "$WORK" && ACT_NO_FETCH=1 "$ACT" list --all --limit 0 >/dev/null )
  t1=$(python3 -c 'import time;print(time.time())')
  printf '%-22s %6.3fs\n' "$label" "$(python3 -c "print($t1-$t0)")"
}

run "cold (full rebuild)"
run "cached (no change)"

# Append one op the way a concurrent writer would: a create for a brand-new
# issue, written straight into the ops tree.
ID="act-be9c4a7f"
DIR="$WORK/.act/ops/$ID/2026-09"
mkdir -p "$DIR"
python3 - "$DIR/$ID-create.json" "$ID" <<'PY'
import hashlib, json, sys
path, iid = sys.argv[1], sys.argv[2]
payload = {"title": "bench probe", "type": "task", "priority": 3,
           "nonce": "0" * 32}
pj = json.dumps(payload, separators=(",", ":"), sort_keys=True)
hlc = {"wall": "2026-09-06T00:00:00.000Z", "logical": 0, "node_id": "0123abcd"}
env = {"op_version": 1, "schema_version": 1, "writer_version": "0.1.0",
       "op_type": "create", "issue_id": iid, "payload": json.loads(pj),
       "hlc": hlc, "node_id": "0123abcd"}
open(path, "w").write(json.dumps(env, separators=(",", ":"), sort_keys=True))
PY

run "after 1 write"
run "cached again"
