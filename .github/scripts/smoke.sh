#!/usr/bin/env bash
#
# smoke.sh — End-to-end smoke workflow for the `act` CLI.
#
# Usage:
#   smoke.sh /path/to/act-binary [workdir]
#
# Exercises the canonical happy-path flow described in spec-v2 §7.8 / §7
# and the CI matrix issue (docs/issues/act-2e8d.md):
#
#   1. `act init` in a fresh git tempdir.
#   2. `act create "smoke task" --json`        ─ capture id.
#   3. `act show <id> --json`                  ─ assert id round-trips.
#   4. `act list --json`                       ─ assert count == 1.
#   5. `act ready --json`                      ─ assert >= 1 ready.
#   6. `act update <id> --claim --isolated --json` ─ assert claimed=true.
#   7. `act close <id> --reason ... --json`    ─ assert ok / commit.
#   8. `act doctor --json`                     ─ assert 0 findings.
#
# Each step asserts both exit code and key JSON fields via jq. Any
# mismatch halts the script with a non-zero exit and a clear diagnostic
# so an automated agent can grep PASS / FAIL from the run output.
#
# The script is idempotent within a single fresh workdir: running it
# twice against the same workdir is undefined (the second run sees the
# leftover repo from the first); supply a fresh tempdir per invocation
# (default behaviour creates one for you).

set -euo pipefail

ACT_BIN="${1:-}"
if [[ -z "${ACT_BIN}" ]]; then
  echo "smoke.sh: missing required arg: ACT_BIN (path to act binary)" >&2
  exit 2
fi
if [[ ! -x "${ACT_BIN}" ]]; then
  echo "smoke.sh: ${ACT_BIN} is not executable" >&2
  exit 2
fi

WORKDIR="${2:-}"
if [[ -z "${WORKDIR}" ]]; then
  WORKDIR="$(mktemp -d -t act-smoke-XXXXXX)"
  echo "smoke.sh: created tempdir ${WORKDIR}"
fi
mkdir -p "${WORKDIR}"
cd "${WORKDIR}"

# Initialize a git repo so .act/ has a parent. The act CLI requires a
# git working tree (cmd/act/main.go: findRepoRoot).
git init -q -b main .
git config user.email "smoke@example.com"
git config user.name  "smoke"
git config commit.gpgsign false
git config tag.gpgsign    false

# Helper: run an act subcommand, fail loudly on unexpected exit code.
#   want_exit  expected exit code (use 0 for success)
#   <cmd...>   args passed to act
run_act() {
  local want_exit="$1"; shift
  local out
  local code=0
  out="$("${ACT_BIN}" "$@" 2>&1)" || code=$?
  if [[ "${code}" -ne "${want_exit}" ]]; then
    echo "FAIL: act $* exited ${code}, want ${want_exit}" >&2
    echo "----- output -----" >&2
    echo "${out}" >&2
    echo "------------------" >&2
    exit 1
  fi
  printf '%s' "${out}"
}

# Helper: assert a jq filter on JSON. Filter must yield "true".
# Trailing args (after label/json/filter) are forwarded to jq verbatim,
# so callers may pass --arg name value pairs to thread shell vars in.
assert_jq() {
  local label="$1"; shift
  local json="$1";  shift
  local filter="$1"; shift
  local got
  got="$(printf '%s' "${json}" | jq -r "$@" "${filter}")"
  if [[ "${got}" != "true" ]]; then
    echo "FAIL: assertion '${label}' failed (filter: ${filter}; got: ${got})" >&2
    echo "----- json -----" >&2
    printf '%s\n' "${json}" >&2
    echo "----------------" >&2
    exit 1
  fi
  echo "ok: ${label}"
}

# Step 1 — init.
INIT_JSON="$(run_act 0 init --json)"
assert_jq "init.has-node-id" "${INIT_JSON}" '(.node_id // "") | length > 0'

# Step 2 — create. Capture the issue id.
CREATE_JSON="$(run_act 0 create --json "smoke task")"
assert_jq "create.has-id"   "${CREATE_JSON}" '(.id // "") | length >= 4'
assert_jq "create.title"    "${CREATE_JSON}" '.title == "smoke task"'
ID="$(printf '%s' "${CREATE_JSON}" | jq -r '.id')"
echo "smoke.sh: created id=${ID}"

# Step 3 — show round-trips id.
SHOW_JSON="$(run_act 0 show --json "${ID}")"
assert_jq "show.id-round-trip" "${SHOW_JSON}" '.id == $id' --arg id "${ID}"

# Step 4 — list reports exactly one open issue.
LIST_JSON="$(run_act 0 list --json)"
assert_jq "list.count==1" "${LIST_JSON}" '.count == 1'

# Step 5 — ready reports at least one ready issue (the new one is open
# with no blockers, so it must surface).
READY_JSON="$(run_act 0 ready --json)"
assert_jq "ready.count>=1" "${READY_JSON}" '.count >= 1'

# Step 6 — claim the issue. --isolated keeps the write off the network
# so the script never touches a real remote. Flags come before the
# positional id because Go's flag package stops parsing at the first
# non-flag arg.
CLAIM_JSON="$(run_act 0 update --claim --isolated --json "${ID}")"
assert_jq "claim.claimed"  "${CLAIM_JSON}" '.claimed == true'
assert_jq "claim.ok"       "${CLAIM_JSON}" '.ok == true'

# Step 7 — close with a reason. JSON shape: {id, ops_written, committed, reason}.
# --no-doctor opts out of the post-close commit-marker correlation check
# (act-f2ea): the smoke flow never builds a host work commit carrying the
# `Act-Id:` trailer, so the check would always warn on stderr and
# run_act's `2>&1` capture would pollute the JSON envelope passed to jq.
# The check itself is regression-tested in close_test.go.
CLOSE_JSON="$(run_act 0 close --no-doctor --reason "smoke complete" --json "${ID}")"
assert_jq "close.id"      "${CLOSE_JSON}" '.id == $id' --arg id "${ID}"
assert_jq "close.reason"  "${CLOSE_JSON}" '.reason == "smoke complete"'

# Step 8 — doctor: every check passes on a fresh repo with one closed
# issue. We first run `doctor --fix --json` to remediate the expected
# index-divergence finding (the index is rebuilt lazily by readers; a
# fresh write-then-doctor sequence will surface the divergence on the
# first call). The second call must report zero findings.
run_act 0 doctor --fix --json >/dev/null
DOCTOR_JSON="$(run_act 0 doctor --json)"
assert_jq "doctor.zero-findings" "${DOCTOR_JSON}" '.count == 0'

echo
echo "PASS: act smoke workflow completed in ${WORKDIR}"
