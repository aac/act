# act v0.1.0 — Day-1 Usability Review

Adversarial review from the perspective of an agent driver doing a happy
path plus a first round of error handling against `/tmp/act` built from
this tree on 2026-04-30. Findings ordered by likelihood-of-blocking-an-agent.

---

1. **`-json` is silently dropped when it appears after a positional arg.**
   - Where: `act show act-f5ce -json`, `act log act-81de -json`.
   - Friction: Go's stdlib `flag` package stops parsing at the first
     non-flag, so `act show act-f5ce -json` returns plain text and the
     agent's JSON parser fails. The agent has no way to know the flag
     was ignored — exit is 0 and output looks fine to a human.
   - Fix: parse with `pflag` / interleave-aware logic, or scan
     `os.Args` for `--json` before delegating to the `flag.FlagSet`. At
     minimum, document positional-after-flags in `--help`.

2. **Error envelope shape is inconsistent across commands.**
   - Where: `act show -json act-XXXX` returns
     `{"error":"issue_not_found","message":"..."}`, but
     `act dep add -json a b` (cycle) returns
     `{"error":{"kind":"cycle","path":[...]}}`, and `act update -json
     -claim` returns yet a third shape with `{"error":"claim_failed",
     "message":"..."}` with no `details` field. The spec
     (spec-v2.md:889) mandates one shape:
     `{"error":"<code>","message":"<human>","details":{...}}`.
   - Friction: an agent cannot write a single `parseError(stdout)` —
     it has to type-switch on whether `error` is a string or an object.
   - Fix: route every error through one helper that always emits
     `{error: <slug>, message: <line>, details: <obj>}`; add a golden
     test that exercises every error class in the spec table.

3. **Ambiguous prefixes report "no match" instead of listing candidates.**
   - Where: in a repo with `act-81de`, `act-8013`, `act-8fd5`, running
     `act show -json act-8` yields
     `{"error":"issue_not_found","message":"act show: no issue matches
     \"act-8\""}` and exit 3.
   - Friction: spec-v2.md:529 promises an `id_ambiguous` error with a
     `candidates[]` array so the agent can re-prompt. Today the agent
     just sees "not found" and concludes the issue does not exist —
     the worst possible failure mode.
   - Fix: implement the prefix pipeline as specified — zero matches →
     `issue_not_found`, ≥2 → `id_ambiguous` with
     `details.candidates`. Current tri-state in the spec also mismatches
     exit codes (529 says exit 2, table says exit 3) — pick one.

4. **Usage errors bypass the JSON envelope and the exit-code contract.**
   - Where: `act show -json` (no id), `act log -json` (no id),
     `act update -json` (no id). All print a plain
     `act show: usage: act show <id> [--json] [--include-ops]` to
     stderr and exit 2, even with `--json` set.
   - Friction: an agent that pipes stdout to JSON parsing sees an
     empty stream and a non-zero code with no machine-readable reason.
     The contract in spec-v2.md:521 ("All non-zero exits emit
     `{"error":...}` on stdout when --json is set") is violated.
   - Fix: in each subcommand's flag-parser path, if `-json` was seen,
     emit a `bad_flag` envelope to stdout before exiting.

5. **Exit codes diverge from the spec table.**
   - Where: `not_in_git` and `act_not_initialized` both return exit 3
     in the binary; spec-v2.md:897 says exit 2. Cycle returns exit 1;
     spec table says exit 6. `claim_failed` returns exit 1; spec says
     exit 5.
   - Friction: the spec carefully designs codes 1/2/3/4/5/6/... so a
     wrapper script can branch on "logical no" vs "usage" vs
     "environment". Today the bands are smeared together — an agent
     can't tell "you typed the flag wrong" from "the repo is broken".
   - Fix: pick the spec table as canonical, drive every error through
     a registry that owns both `kind` and `exit`, and add a unit test
     that asserts every entry's exit code.

6. **`claim_failed` (and all commit failures) leak a multiline shell
   stack trace into the JSON `message`.**
   - Where: `act update -json -claim <id>` when commit fails. Got a
     ~700-char message containing the full git invocation,
     environment-runner debug lines, the signing-server HTTP body, and
     "Usage:" help — all jammed into one JSON string with embedded
     newlines.
   - Friction: blows up token budgets, looks like a panic, gives the
     agent zero useful signal. Also makes the error
     non-deterministic, so goldens can't pin it.
   - Fix: classify `git commit` failures into one of `commit_signing_failed`,
     `commit_hook_failed`, `commit_unknown`; put the raw stderr tail
     (≤512 bytes) under `details.stderr_tail`; keep `message` to one
     short line. Mirror the `hook_failed` design (spec:906).

7. **Auto-commit pollutes `git log` with one commit per op.**
   - Where: any rapid sequence of `act create` / `act update` (each
     emits its own `act-op: ...` commit by default).
   - Friction: a typical agent session does dozens of writes; the host
     repo's `git log --oneline` becomes 90% act bookkeeping. There is
     `--no-commit` per call but no global "batch this session" mode,
     and no documented squash strategy. `act doctor -compact` exists
     but the help text says nothing about op-commit squashing.
   - Fix: add `act batch <subcommand> ...` that defers commits and
     emits one squashed commit at the end (or document an env var like
     `ACT_DEFER_COMMIT=1`). At minimum, `act doctor --help` should
     mention compaction's effect on commit volume.

8. **`act doctor` with zero issues found is indistinguishable from
   "doctor didn't run".**
   - Where: `act doctor` and `act doctor -json` on a healthy repo print
     `act doctor: 0 findings` / `{"findings":null,"count":0}`.
   - Friction: `null` instead of `[]` forces the agent to write
     `(findings or []).length` defensively; "0 findings" alone gives
     no signal that, e.g., the index was rebuilt or which checks ran.
     With many checks possible (spec:797), the agent wants to know
     *which* passed.
   - Fix: emit `{"checks_run":["index-divergence","orphan-close",
     "unknown-op-version",...],"findings":[]}` — empty array, not
     null, and an explicit list of what was inspected. Each finding
     should include `kind`, `severity`, `fix_hint`, and `auto_fixable`.

9. **`act log` has no `--limit`, no `--format`, and dumps full op
   payloads inline.**
   - Where: `act log -json act-<id>` returns the full HLC-sorted op
     stream, every payload, every nonce.
   - Friction: for a moderately old issue, this is many KB of JSON the
     agent must parse just to answer "when was it last touched?".
     There is no `--since`, no `--op-type` filter, no `--summary` mode.
   - Fix: add `--limit N`, `--since <hlc>`, `--op-type create,update`,
     and a default summary projection
     (`{hlc, op_type, fields_changed[]}`); require `--full` for the
     current behavior.

10. **`act create` requires a positional title but `--help` doesn't say so.**
    - Where: `act create --help` lists only flags. `act create -title
      "x"` errors with `flag provided but not defined: -title`. The
      correct invocation is `act create -no-commit "x"` — discoverable
      only by reading source.
    - Friction: every agent's first call fails. Sane prior is "title
      is `--title`" because that's what every other CLI does (gh, jira,
      linear).
    - Fix: print a usage line `act create [flags] <title>` at the top
      of `create --help`, and accept `--title` as an alias to the
      positional for ergonomic parity. Same review needed for
      `update`, `close`, `dep add`, `show`, `log`, `search`.

11. **No composed `act_next` / `act_finish` / `act_block` MCP tools
    are reachable from the CLI for testing.**
    - Where: spec-v2.md:850–874 defines these three composed tools as
      the agent's primary surface, but `act mcp --help` exposes only
      `--read-only` and `--workdir`; there is no `act next` / `act
      finish` / `act block` CLI shim. An agent driver authoring tests
      against the binary cannot exercise the composed flows without
      spinning up an MCP client.
    - Friction: integration tests, debug, and "what does `act_next`
      pick right now?" require a full stdio MCP harness. The CLI is
      otherwise a faithful 1:1 with the per-verb tools — these three
      are the gap.
    - Fix: add `act next`, `act finish`, `act block` as CLI commands
      that delegate to the same internal handlers the MCP server
      uses. Bonus: an `act mcp --once <tool> <json-args>` dry-run
      mode for testing the JSON Schemas in spec:846.

12. **HLC drift is undetectable from the CLI surface.**
    - Where: spec-v2.md:669 / error table:909 require an `hlc_drift`
      error (exit 8) when an op's HLC diverges >5min from the repo
      reference. There is no way to produce this error today (writing
      a synthetic op file and folding does not surface it through any
      of the commands tested), and `act doctor` does not list a
      `hlc-drift` check.
    - Friction: in clock-skewed CI runners or replayed clones from
      other timezones, the agent will see strange ordering bugs with
      no diagnostic. It can't query "is my clock OK?" before writing.
    - Fix: add a `clock-skew` check to `act doctor` that compares
      `time.Now()` against the max-HLC in `.act/ops/`; promote to an
      explicit `act doctor -check clock-skew` so an agent can probe.
      When a write would emit `hlc_drift`, return the spec'd error
      with `details.delta_seconds`.

---

## Cross-cutting recommendations

- **One error helper.** A single `emitError(kind, msg, details, exitCode)`
  used by every subcommand removes findings 2/4/5/6 in one stroke.
- **Schema goldens.** Snapshot the JSON of every command (success +
  every documented error class) so envelope drift is caught in CI.
- **A "first 60 seconds" doc.** `init -> create -> ready -> show ->
  close` against a tempdir, copy-pasteable. Would pre-empt finding 10
  and surface flag-position issues the moment the doc is written.
