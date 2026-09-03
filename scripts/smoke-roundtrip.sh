#!/usr/bin/env bash
# act smoke test — round-trip a scratch tracker with a given act binary.
#
# Usage: scripts/smoke-roundtrip.sh [path-to-act]   (default: act on PATH)
#
# WHY THIS EXISTS. `act version` proves a binary runs; it does not prove the
# binary can still record work. This script exercises the loop an agent
# actually depends on — init, create, claim, close, and a push/pull round
# trip through a bare repo — against throwaway directories, so an install
# that would break the tracker fails here instead of on the first real
# ticket. It touches nothing outside its own temp dir.
#
# It also covers the close-gate ordering (act-8ee085): a gate that refuses a
# close must leave no close op behind, in the working tree or in HEAD, even
# when something commits whatever it finds under ops/ mid-gate.
#
# Exit 0 = the binary is safe to install. Any failure exits non-zero and
# names the step.
set -uo pipefail

# A path (anything with a slash) is resolved to an absolute one: the script
# cds into its scratch dir, where a relative path would no longer resolve.
ACT_BIN="${1:-act}"
case "$ACT_BIN" in
  */*) ACT_BIN="$(cd "$(dirname "$ACT_BIN")" && pwd)/$(basename "$ACT_BIN")"
       [ -x "$ACT_BIN" ] || { echo "smoke: not an executable act binary: ${1}" >&2; exit 1; } ;;
  *)   command -v "$ACT_BIN" >/dev/null 2>&1 || { echo "smoke: no '$ACT_BIN' on PATH" >&2; exit 1; } ;;
esac

ROOT="$(mktemp -d "${TMPDIR:-/tmp}/act-smoke-XXXXXX")"
trap 'rm -rf "$ROOT"' EXIT
fail() { echo "smoke: FAIL — $*" >&2; exit 1; }
step() { printf 'smoke: %s\n' "$*"; }

git init --bare --quiet --initial-branch=main "$ROOT/bare.git" || fail "could not create scratch bare"
mkdir -p "$ROOT/host" && cd "$ROOT/host" || fail "scratch host dir"
git init --quiet --initial-branch=main .
git config user.email smoke@example.invalid
git config user.name smoke
echo x > README && git add README && git commit --quiet --no-verify -m init

step "init"
"$ACT_BIN" init >/dev/null 2>&1 || fail "act init"
[ -d .act/ops ] || fail "act init left no .act/ops"
git -C .act config user.email smoke@example.invalid
git -C .act config user.name smoke
git -C .act remote add origin "$ROOT/bare.git"

step "create"
ID="$("$ACT_BIN" create "smoke ticket" --json 2>/dev/null | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')"
[ -n "$ID" ] || fail "act create returned no id"

step "claim"
"$ACT_BIN" update --claim "$ID" >/dev/null 2>&1 || fail "act update --claim"

step "refused close leaves nothing behind (act-8ee085)"
mkdir -p .act/hooks
cat > .act/hooks/close <<'HOOK'
#!/bin/sh
# Stand in for a sweep that commits whatever it finds under ops/ while the
# gate runs, then refuse the close.
cd "$ACT_STATE_PATH" || exit 1
if [ -n "$(git status --porcelain -- ops)" ]; then
  git add -- ops && git commit --no-verify -q -m "sweep: uncommitted op file(s)"
fi
echo "smoke gate refuses this close" >&2
exit 1
HOOK
chmod +x .act/hooks/close
"$ACT_BIN" close "$ID" --reason "should not land" >/dev/null 2>&1
[ $? -ne 0 ] || fail "a refused close exited 0"
[ -z "$(find .act/ops -name '*-close.json' 2>/dev/null)" ] || fail "refused close left a close op in the working tree"
git -C .act ls-tree -r --name-only HEAD -- ops | grep -q -- '-close\.json' && fail "refused close is committed in HEAD (phantom close)"
[ -z "$(git -C .act status --porcelain -- ops)" ] || fail "refused close left ops/ dirty: $(git -C .act status --porcelain -- ops)"
[ "$("$ACT_BIN" show "$ID" 2>/dev/null | sed -n 's/^status: //p')" != "closed" ] || fail "a refused close reports closed"

step "close"
rm -f .act/hooks/close
"$ACT_BIN" close "$ID" --reason "smoke" >/dev/null 2>&1 || fail "act close"
[ "$("$ACT_BIN" show "$ID" 2>/dev/null | sed -n 's/^status: //p')" = "closed" ] || fail "act show does not read closed after close"
[ -z "$(git -C .act status --porcelain -- ops)" ] || fail "close left ops/ uncommitted"

step "sync (push, clone, read back)"
git -C .act push --quiet -u origin main || fail "push to scratch bare"
git clone --quiet "$ROOT/bare.git" "$ROOT/consumer/.act" >/dev/null 2>&1 || fail "clone of scratch bare"
( cd "$ROOT/consumer" && git init --quiet --initial-branch=main . && "$ACT_BIN" doctor --fix >/dev/null 2>&1
  [ "$("$ACT_BIN" show "$ID" 2>/dev/null | sed -n 's/^status: //p')" = "closed" ] ) \
  || fail "a fresh consumer of the pushed tracker does not read the ticket closed"

echo "smoke: OK — $ACT_BIN round-tripped init/create/claim/close/sync"
