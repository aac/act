# act v0.1.0 verification

Date: 2026-04-29
Branch: `claude/implement-dispatcher-6k6sO`
Verifier: Stage 8 (final)

## Build (binary path, size)

| Item | Value |
|------|-------|
| Command | `CGO_ENABLED=0 go build -o /tmp/act ./cmd/act/` |
| Result | PASS |
| Binary path | `/tmp/act` |
| Binary size | 11,072,037 bytes (~10.6 MiB) |
| Exit code | 0 |

## Test suite (per-package PASS/FAIL with summary stats)

Command: `go test ./... -count=1`

| Package | Result | Time |
|---------|--------|------|
| `github.com/aac/act/cmd/act` | (no tests) | n/a |
| `github.com/aac/act/internal/canonicaljson` | PASS | 0.006s |
| `github.com/aac/act/internal/claim` | PASS | 0.066s |
| `github.com/aac/act/internal/cli` | PASS | 6.596s |
| `github.com/aac/act/internal/compact` | PASS | 0.205s |
| `github.com/aac/act/internal/config` | PASS | 0.023s |
| `github.com/aac/act/internal/fold` | PASS | 0.842s |
| `github.com/aac/act/internal/gitops` | PASS | 0.467s |
| `github.com/aac/act/internal/hlc` | PASS | 0.006s |
| `github.com/aac/act/internal/hooks` | PASS | 0.226s |
| `github.com/aac/act/internal/ids` | PASS | 0.024s |
| `github.com/aac/act/internal/importer` | PASS | 0.027s |
| `github.com/aac/act/internal/index` | PASS | 0.070s |
| `github.com/aac/act/internal/mcp` | PASS | 0.312s |
| `github.com/aac/act/internal/op` | PASS | 0.020s |
| `github.com/aac/act/internal/store` | (no tests) | n/a |

Summary: **14/14 testing packages PASS, 0 FAIL**. Two packages (`cmd/act`, `internal/store`) have no tests.

## Lint (gofmt, vet)

| Check | Command | Result | Output |
|-------|---------|--------|--------|
| gofmt | `gofmt -l .` | PASS | (no files reported) |
| go vet | `go vet ./...` | PASS | (no warnings) |

## Smoke workflow (per-command exit + summary)

Setup: fresh `mktemp -d`, `git init -q`, `git config commit.gpgsign false`, configured user.email/user.name. Required because the local environment's commit-signing hook (`environment-runner code-sign`) returns 400 "missing source"; this is an environment issue, not an `act` defect — disabling commit-signing reflects a normal end-user setup.

> Note on flag ordering: the spec test plan invokes flags after the positional argument (e.g. `/tmp/act create "verify task" -p 1 --json`). Go's `flag` package stops parsing at the first non-flag argument, so flags placed after positional args are silently ignored. With the literal command from the spec, `--json` is dropped and `act create` emits the human-friendly `Created act-XXXX "verify task"` line on stdout (exit 0). This is a real CLI ergonomics defect — `act create` should switch to a parser that allows interleaved flags (e.g. `pflag` or manual rearrangement). The smoke results below use the corrected ordering (flags before positional) so the JSON envelopes can be exercised.

| # | Command | Exit | Summary |
|---|---------|------|---------|
| 1 | `act init --json` | 0 | `{"ok":true,"act_dir":"…/.act","node_id":"…"}` |
| 2 | `act create --json -p 1 "verify task"` | 0 | `{"id":"act-1609","short_id":"act-1609","title":"verify task"}` |
| 3 | `act show --json act-1609` | 0 | issue envelope with status=open, priority=1, type=task |
| 4 | `act list --json` | 0 | `{"issues":[{…act-1609 open p=1…}],"count":1}` |
| 5 | `act ready --json` | 0 | `{"ready":[{…act-1609…}],"count":1}` |
| 6 | `act update --json --claim --isolated act-1609` | 0 | `{"ok":true,"claimed":true,"id":"act-1609","winner":"…","ops_written":["claim"]}` |
| 7 | `act close --json --reason "verified" act-1609` | 0 | `{"id":"act-1609","short_id":"act-1609","ops_written":1,"committed":true,"reason":"verified"}` |
| 8 | `act doctor --json` | **1 (FAIL)** | `{"findings":[{"check":"index-divergence","severity":"error","message":"index diverged: current=\"act-1609|in_progress|verify task;\" rebuilt=\"act-1609|closed|verify task;\""}],"count":1}` |
| 9 | `act version --json` | 0 | `{"binary_version":"0.1.0","go_version":"go1.25.0","platform":"linux/amd64","writer_version":"0.1.0"}` |

### Defects discovered in smoke flow

1. **CLI flag parser stops at first positional** (medium): `act create "title" -p 1 --json` does not honor `-p`/`--json` because Go's stdlib `flag` package stops at the first non-flag arg. The spec test plan exercises this calling convention, so the binary fails the literal spec invocation. Mitigation: pass flags before the positional argument, or migrate `cmd/act` to a parser that supports interleaving.
2. **Index diverges after `close`** (high): after a normal `create → update --claim → close` sequence, `act doctor` reports `index-divergence` (current shows `in_progress`, rebuilt shows `closed`). The `close` op is durable on disk (rebuilt index sees it) but the live SQLite index was not updated when the close op was written. `act doctor --fix` repairs the divergence successfully. Likely the close path skips `index.Apply` for the close op.

## MCP server (initialize + tools/list)

Setup: spawned from an initialized act repo (`act init` first), since `act mcp` requires `.act/` to exist.

| Step | Result | Detail |
|------|--------|--------|
| `initialize` request | PASS | Response contains `"protocolVersion":"2024-11-05"`, `serverInfo.name=act-mcp`, `serverInfo.version=0.1.0`, capabilities.tools present |
| `tools/list` request | PASS | **15 tools returned (≥12 required)** |

Tools advertised: `act_init`, `act_create`, `act_list`, `act_show`, `act_update`, `act_close`, `act_dep_add`, `act_ready`, `act_search`, `act_log`, `act_doctor`, `act_version`, `act_next`, `act_finish`, `act_block`.

## Cross-platform builds (per target)

Command pattern: `GOOS=… GOARCH=… go build -o /tmp/act-… ./cmd/act/`

| Target | Result | Binary size |
|--------|--------|-------------|
| `linux/arm64` | PASS | 10,711,163 bytes |
| `darwin/amd64` | PASS | 11,297,376 bytes |
| `darwin/arm64` | PASS | 11,000,850 bytes |
| `windows/amd64` | PASS | 11,399,168 bytes (`.exe`) |

All cross-builds succeeded with exit 0 and produced reasonably-sized executables.

## Concurrency tests (per test name)

Command: `go test ./internal/cli/ -run TestConcurrent -v`

| Test | Result | Time |
|------|--------|------|
| `TestConcurrentDistinctOps` | PASS | 0.29s |
| `TestConcurrentClaimRace` (5 sub-iterations) | PASS | 1.55s |
| `TestConcurrentClaimRace/0` | PASS | 0.31s |
| `TestConcurrentClaimRace/1` | PASS | 0.32s |
| `TestConcurrentClaimRace/2` | PASS | 0.30s |
| `TestConcurrentClaimRace/3` | PASS | 0.31s |
| `TestConcurrentClaimRace/4` | PASS | 0.31s |
| `TestConcurrentDistinctOpsBidirectional` | PASS | 0.32s |

Multi-writer claim race converges on a single winner across 5 iterations; bidirectional distinct-ops scenario converges. **Concurrency tests all PASS.**

## Overall: FAIL

Two real defects were uncovered by the smoke flow that block calling this a clean v0.1.0:

1. **`act doctor` reports `index-divergence` (severity=error, exit 1) immediately after a normal close** — the live index is out of sync with what would be rebuilt from `.act/ops/`. This is a correctness bug in the close path's index-apply step.
2. **Flags after the positional argument in `act create` are dropped silently**, so the spec's literal `act create "title" -p 1 --json` invocation does not emit JSON. This is a CLI ergonomics regression visible to every user of the documented command form.

Build, unit tests (14/14 packages), gofmt, vet, cross-platform builds (4/4 targets), MCP scaffold (initialize + 15 tools), and concurrency tests (8/8) all pass. The defects above are scoped, reproducible, and have clear remediation paths, but they prevent an unconditional PASS verdict for the verification stage.
