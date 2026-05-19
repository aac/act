package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// runHelp dispatches `act help` and `act help <topic>`.
//
// Distinct from `act --help` / `act -h` (the conventional short usage
// line, handled by usage()): `act help` is the agent-onboarding tutorial.
// A new agent in a new repo can run it once and learn enough to start
// being useful, without consulting any external doc.
//
// Topics: workflow, ops-model, errors. Anything else is a usage error.
//
// Output goes to stdout (this is documentation, not an error).
func runHelp(args []string) int {
	return runHelpTo(os.Stdout, os.Stderr, args)
}

// runHelpTo is runHelp with explicit writers, for testing.
func runHelpTo(stdout, stderr io.Writer, args []string) int {
	fs := flag.NewFlagSet("help", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	rest := fs.Args()
	topic := ""
	if len(rest) > 0 {
		topic = strings.ToLower(rest[0])
	}
	switch topic {
	case "":
		fmt.Fprint(stdout, helpOverview)
	case "workflow":
		fmt.Fprint(stdout, helpWorkflow)
	case "ops-model", "opsmodel", "ops_model":
		fmt.Fprint(stdout, helpOpsModel)
	case "errors", "error", "error-envelope":
		fmt.Fprint(stdout, helpErrors)
	default:
		fmt.Fprintf(stderr, "act help: unknown topic %q\n", rest[0])
		fmt.Fprintln(stderr, "topics: workflow, ops-model, errors")
		return 2
	}
	return 0
}

const helpOverview = `act — agent task tracker

WHAT THIS IS
  act is an agent-first task tracker. State lives as an append-only
  op-log in .act/ inside a normal git repo. Concurrent writes from
  different sessions or machines merge with plain git pull --rebase.
  Agents are the primary user; humans interact through the agents,
  not the tracker.

GETTING STARTED
  If the 'act' binary isn't on PATH yet:

    go install github.com/aac/act/cmd/act@latest

  Then, in any git repo:

    act init                 # creates .act/, records a node_id
    act ready                # the canonical first loop step

  go install is the canonical bootstrap — one command, no setup. The
  alternates (brew tap, prebuilt release binaries, curl installer)
  exist but are tracked separately (act-e6a5, act-4fe6, act-8416) and
  are not the recommended path for new agent sessions. See README.md
  for the rationale and the tradeoffs.

THE CANONICAL WORK LOOP (use this in every session)
  1. act ready                    # what's unblocked, ordered by priority
  2. act update --claim <id>      # take it (atomic; concurrent claimers
                                  #          resolve via last-write-wins)
  3. <do the work, write tests, run them>
  4. act close <id> [--reason "..."]
                                  # writes + stages the close op. If the
                                  # working tree has uncommitted code
                                  # changes, the op stays staged for the
                                  # NEXT git commit to subsume — one
                                  # work commit instead of work + close
                                  # (see 'act help workflow' for the
                                  # rationale and act-a659 in the rough).
                                  # If the tree is otherwise clean, the
                                  # close commits standalone.
  5. git commit -a -m "<summary>" -m "Act-Id: act-<short-id>"
                                  # subsumes the staged close op + your
                                  # code changes. The 'Act-Id: act-XXXX'
                                  # trailer in the commit body lets 'act
                                  # doctor' correlate work commits with
                                  # closed issues. Use two -m flags so
                                  # the trailer becomes its own paragraph
                                  # in the body (separated from the
                                  # subject by a blank line).
  6. git push                     # publish for the other agents.

  In an MCP context, prefer act_next + act_finish — they compose the
  steps above into single tool calls and return commit_marker for free.

WHEN TO FILE FOLLOW-UPS
  Mid-task discovery of a real bug or surface gap → 'act create' with
  type=bug or task; do NOT halt the current task on it. File it, keep
  working. The dogfood signal is the bug landing in the backlog with
  a clear repro, not a half-finished current task.

ACCEPTANCE CRITERIA
  Each issue has a list of acceptance criteria; they define done.
  Before closing: confirm each is satisfied or explicitly waived in
  the close --reason. Don't close on partial — file follow-ups for
  the unsatisfied criteria instead.

CONCURRENCY
  Multiple sessions/machines can run act simultaneously. Each write
  is a new op file with a hash-derived name; new files never conflict
  textually in git. Logical conflicts (e.g. two claim ops on the same
  issue) resolve deterministically: last-write-wins by HLC timestamp,
  ties broken by op hash. There is no manual merge step.

IDENTITY
  Each .act/ has a node_id derived at init from machine-id + git email.
  'assignee' strings tag work with the node that owns it. The MCP
  composed tools resolve identity automatically.

COMPACTION
  Long-lived issues accumulate ops. 'act compact --issue <id>'
  snapshots the op log and (optionally) prunes. Mechanical only;
  no LLM summarization.

DEEPER DIVES
  act help workflow         # the loop in detail with copy-pastable examples
  act help ops-model        # how the op log folds into state
  act help errors           # error-envelope contract (--json error shape & codes)
  act <subcommand> --help   # flag reference for any subcommand

  Subcommands:
    init version log list search ready mine show
    create close reopen delete update redact
    dep add doctor import mcp install-skill
    bootstrap-worker harvest remote

  'act mine' lists issues currently assigned to your node that are
  in_progress or blocked. 'act ready --mine' filters the ready queue
  to issues already assigned to you. Both default to identity from
  .act/config.json node_id; --as <id> overrides.

BOOTSTRAPPING A WORKER WORKTREE
  When an orchestrator dispatches a sub-agent into a separate git worktree
  (or any other empty directory), that target has no .act/ state yet.
  'act bootstrap-worker' copies the current host repo's .act/ tree into
  the target so the dispatched worker can run act commands against state
  that mirrors the orchestrator's view:

    act bootstrap-worker <target-path>          # seed <target>/.act/
    act bootstrap-worker <target-path> --force  # replace non-empty target
    act bootstrap-worker <target-path> --json   # machine-readable summary

  The copy stages into <target>/.act.bootstrap/ first and atomic-renames
  to <target>/.act/ on success, so a mid-copy failure never leaves a
  partial state at the target. After the rename, an 'act ready' fold
  runs against the new target as a round-trip validation; if validation
  fails, the target is torn down. A small .act/.bootstrap-meta.json
  records the source root, copied_at timestamp, and a dispatch_hlc that
  future Phase 2 'act harvest' may use as a fallback ordering signal.

  Phase 1.5 (current): cwd resolves the source repo. Phase 2 will add
  '--from-remote <url>' to clone from a remote instead; both modes will
  coexist for sandboxed-no-network workers.

HARVESTING A WORKER'S OPS BACK
  Mirror of bootstrap-worker. When a dispatched sub-agent finishes work
  in its worktree, the orchestrator pulls the new ops back into the host
  with 'act harvest':

    act harvest <worker-path>            # copy new ops + commit + re-fold
    act harvest <worker-path> --dry-run  # list what would be harvested
    act harvest <worker-path> --json     # machine-readable summary

  Identity is by op filename (HLC + content hash); harvest computes the
  set difference between the worker's .act/ops/ and the host's .act/ops/,
  copies new files into the host, stages them, and produces a single
  commit on the nested .act/.git with message
  'act harvest: <N> ops from <basename>'. Re-folds the index afterward;
  a fold failure is surfaced in the JSON envelope but does not roll back
  the copy or commit — harvest is one-way append.

  Re-running harvest with no new ops is a no-op (zero ops, exit 0). A
  worker op whose filename exists at the host with divergent content is
  rejected with code op_filename_collision (a corruption signal, not a
  silent overwrite). Harvest does NOT push the host's commit to its git
  remote — that's the orchestrator's responsibility.

  The --json envelope carries:

    harvested_ops      list of op file paths (relative to .act/ops/) that
                       were newly copied into the host this run. On
                       --dry-run, the set that WOULD be copied.
    skipped_ops        list of {path, reason} entries for worker ops the
                       host already had (reason: already_present).
    fold_diff_summary  {issues_indexed, ops_added} counts captured from
                       the index rebuild after the harvest commit. Zero
                       on --dry-run and on zero-op no-ops.

ACT REMOTE (PHASE 2 COORDINATION PLANE)
  The 'act remote' subcommand toggles the small set of git-config keys
  that wire the nested .act/.git repo into the Phase 2 coordination
  plane: receive behaviour, scalar timeouts/thresholds, and the
  load-bearing 'act.role' key (orchestrator | worker) that closes v1
  open-question #4.

    act remote enable               # set canonical keys + install hook skeleton
    act remote disable              # unset keys + remove post-receive hook
    act remote enable --json        # machine-readable summary

  Enable writes the following keys to .act/.git/config:

    act.role=orchestrator                          # role marker
    receive.denyCurrentBranch=updateInstead        # accept worker pushes
    act.readCacheTTLSeconds=5
    act.bootstrapTimeoutSeconds=30
    act.fetchTimeoutSeconds=10
    act.slowWriteThresholdMs=1000
    act.upstreamDriftThresholdCommits=50
    act.upstreamDriftThresholdSeconds=3600

  Enable also installs .act/.git/hooks/post-receive (a documented no-op
  skeleton; ticket 6a fills in the body) and runs 'act doctor' as a
  verification pass. Disable unsets every key and removes the hook
  file. Both verbs are idempotent: running disable twice exits zero
  both times; running enable on an already-enabled repo re-writes the
  keys to defaults.

  If 'act.role' is unset (legacy or hand-crafted repo), the default
  parsed value is 'worker' (safe — workers don't trigger upstream
  sync). No filesystem-path heuristic; the config key is the only
  mechanism for role decision.

INSTALLING THE SKILL
  The canonical Claude Code skill for act (SKILL.md plus reference
  docs) is embedded in this binary. To drop it into your skills
  directory so agents pick it up on every session:

    act install-skill              # writes to ~/.claude/skills/act/
    act install-skill --force      # overwrite local edits to canonical files
    act install-skill --dest PATH  # alternate destination
    act install-skill --json       # machine-readable install summary

  install-skill is idempotent: files already matching the embedded
  copy are skipped; files that diverge are listed and left untouched
  unless --force is passed. Re-run after every 'act' upgrade.
`

const helpWorkflow = `act — workflow

THE LOOP IN DETAIL

  Pulling work
    $ act ready --json
    Returns the unblocked frontier ordered by priority. Empty result
    means there is nothing to claim — either everything is closed or
    everything open is waiting on a dep.

  Claiming
    $ act update --claim <id>
    Writes a claim op + assignee=$node + status=in_progress in one
    auto-commit. If two sessions claim the same issue concurrently,
    the loser learns by running 'act show' and seeing a different
    assignee. 'act show <id>' is cheap; check it after claim if the
    work is expensive to redo.

  Doing the work
    Implement, write tests, run them. The work commit's message must
    embed the issue's commit_marker as a trailer in the body so
    'act doctor' can correlate:

      git commit -a -m "implement <thing>" -m "Act-Id: act-XXXX"

    Two -m flags produce a body paragraph for the trailer (separated
    from the subject by a blank line). The Act-Id trailer form
    (act-c4c5) replaces the pre-migration '(act-XXXX)' subject-line
    suffix; doctor still resolves the old form for back-compat, but
    new commits should always use the trailer.

    Order matters. The canonical loop is close-then-commit, NOT
    commit-then-close: 'act close' stages its op file but defers
    the commit when the working tree has code changes; your next
    'git commit -a' subsumes the staged op into the work commit.
    Result: one work-commit-with-close per task instead of work +
    close (act-a659).

    If you find a bug or surface gap mid-flight, file it as a
    follow-up but keep working on the current issue:

      $ act create "<follow-up title>" --type bug \
          --description "<repro>" --accept "<resolution criterion>"

    To file a follow-up AND link it as blocked by an existing issue
    in one atomic commit (replaces a separate 'act dep add' call):

      $ act create "<title>" --blocked-by <id> [--blocked-by <id>...]

    The flag is repeatable; duplicate ids fold to one edge. The
    create + add_dep ops are written under the new issue's id and
    bundled into a single commit; on any failure between op-write
    and commit-success, the partial state rolls back so the new
    issue never exists without its declared blocking edges. Same
    semantic as 'act_block --blocked-by' (the new issue is the one
    being blocked).

    For the inverse direction — a follow-up that BLOCKS an existing
    issue (e.g. a critic-review finding that must land before its
    parent fanout meta-ticket runs) — use --blocks, the mirror flag:

      $ act create "<title>" --blocks <id> [--blocks <id>...]

    Direction is unambiguous from the flag name. --blocks records
    "new ticket blocks <id>", so <id> drops out of 'act ready' until
    the new ticket closes. The add_dep op lands under <id>'s shard
    (its deps[] is what grew), not the new issue's. The two flags
    compose on a single create call when the new issue both is
    blocked by some prereqs and gates some downstream work; an id
    appearing in both is rejected as a 2-cycle.

  Closing
    $ act close <id> --reason "<one-liner>"

    Writes the close op file, runs .act/hooks/close (if present), and
    stages the op for git. Three commit outcomes depending on context:

      - Working tree has non-.act changes (typical): close op stays
        staged. Your next 'git commit -a' picks it up alongside the
        code change. The CloseResult includes commit_marker (the
        'Act-Id: act-XXXX' trailer) so the agent's prompt can build
        the message verbatim.
      - Working tree clean outside .act/: close commits standalone
        (preserves no-code-close UX as a single command).
      - --no-commit: op file written, not staged, not committed.

    --push errors when the close stays staged — there's nothing on
    HEAD yet to publish. The error path fully rolls back the close
    (op file removed), so the recovery is: commit your work first
    via 'git commit -a -m <subject> -m "Act-Id: act-XXXX"', then
    either re-run 'act close <id> --push' or push manually after
    the work commit subsumes the close op via the next plain
    'act close'.

    --reason is capped at 500 bytes. The cap is deliberate: reasons
    are audit-trail summaries, intended to be readable at a glance
    from git log and 'act show'. If you want more room, the work
    commit message or a follow-up issue is the right home. Same cap
    applies to --reason on 'act reopen' and each --accept on 'act
    create'.

EXAMPLE SESSION (CLI)
  $ act ready --json | jq -r '.ready[0].id'
  act-c26a01
  $ act update --claim act-c26a01
  $ # ... edit code, write tests, run them ...
  $ act close act-c26a01 --reason "all 5 acceptance criteria green"
  Closed act-c26a01: all 5 acceptance criteria green
    Close op staged. Include in your next commit:
    git commit -a -m '<subject>' -m 'Act-Id: act-c26a01'
  $ git commit -a -m "implement --blocked-by flag" -m "Act-Id: act-c26a01"
  $ git push

COMMIT MARKER INVARIANTS
  Emission form (since act-c4c5): 'Act-Id: act-<short>' as a trailer
  in the commit body (separated from the subject by a blank line).
  The short string is the issue's shortest-unique prefix as computed
  by ids.ShortestUniquePrefixes — variable length, minimum 6 hex chars
  for newly minted ids (4 hex chars for historical ids that pre-date
  the act-f9a0 widening; both shapes remain valid on disk). Use
  'act show <id> --commit-marker' (or the commit_marker field on
  act_next's response) to get the canonical string;
  do NOT slice the id by hand.

  'act doctor' orphan-close greps commit messages for either the new
  'Act-Id: act-XXXX' trailer or the historical '(act-XXXX)' subject-
  line marker (back-compat for resolution, not for emission). The
  grep keys on the issue's canonical marker prefix (6 hex chars for
  new ids, the full id for historical sub-floor ids) and matches as
  a substring, so unique-prefix growth from id collisions still finds
  the right commits. Hand-rolled shapes ('issue act-c26a01' or 'closes
  #c26a01') do not match — use the canonical trailer.

  The trailer form was chosen for OSS-friendliness: it survives
  squash-merge intact, is invisible to conventional-commit linters,
  is ignored by semantic-release CHANGELOGs, and is safe for
  external contributors to ignore.

EXTERNAL DEPS
  Sometimes an act issue is blocked on work tracked in a sibling system
  act doesn't import — a Linear ticket, a GitHub issue in another repo,
  a Jira card, anything with its own canonical id. Use --ext-add to
  attach an opaque ref:

    $ act update <id> --ext-add "linear:ENG-123"
    $ act update <id> --ext-add "gh:org/other-repo#42" --ext-add "..."

  Each --ext-add writes one add_external_dep op. The ref is stored
  verbatim — act doesn't interpret it. Re-adding an already-attached
  ref is idempotent at the apply layer.

  An issue with at least one external dep is excluded from 'act ready'
  the same way an unresolved internal block excludes. The caller owns
  the lifecycle: when the upstream work is done, clear the ref with
  --ext-rm (also idempotent on absence, so an orchestrator can fire
  the clear twice without erroring):

    $ act update <id> --ext-rm "linear:ENG-123"

  Use --ext-add for cross-tracker blocks; use 'act dep add' for act-
  to-act block edges. The two surfaces compose: an issue may carry
  both internal blockers and external refs, and either kind keeps it
  out of 'act ready' until cleared.

ESCAPE HATCHES
  Halt the loop and surface to the human when:
    - acceptance criteria are ambiguous or conflict
    - the fix needs a behavior change that isn't strictly additive
    - the issue's scope turns out to depend on another open issue's fix
    - tests reveal a defect deeper than the issue describes
  Otherwise: keep going.
`

const helpOpsModel = `act — ops model

EVERY WRITE IS AN OP FILE
  Issues are not stored as monolithic JSON. Each state change writes
  a new file under:
    .act/ops/<issue-id>/<yyyy-mm>/<hlc>-<hash>-<type>.json
  Files are never modified after creation. Folding the op stream in
  HLC order produces the current state.

WHY THIS SHAPE
  Two sessions writing different ops to the same issue produce two
  different files. Git merges them trivially because new files never
  textually conflict. This gets cell-level merge semantics without
  Dolt or any custom merge driver.

HLC ORDERING
  Each op has a Hybrid Logical Clock stamp: (wall_ms, logical_counter,
  node_id). HLC combines physical wall-clock time with a Lamport-style
  logical counter so writers on different machines, with imperfectly
  synchronised clocks, still produce a deterministic order. Comparison
  is wall first, logical second, op_hash as tiebreak. This is what lets
  the fold function produce identical state from any permutation of the
  same op set.

LOGICAL CONFLICTS
  Two ops touching the same field (e.g. both setting priority)
  produce two files. The fold function applies LWW by HLC timestamp,
  ties broken by op hash. There is no manual resolution step; you
  cannot see a "merge conflict" from this.

AUTO-COMMIT
  By default each op file is committed immediately so concurrent
  sessions see each other's writes after a fetch. Use --no-commit on
  any write command to bundle multiple ops into one commit (useful
  during a bootstrap or migration).

INDEX
  .act/index.db is a SQLite cache rebuilt on demand from the op log.
  It accelerates 'act ready' and 'act list --status closed'. It is
  derived state, gitignored, and the source of truth is always the
  ops directory.

SNAPSHOTS
  'act compact --issue <id>' writes .act/snapshots/<id>.json from
  the op log and optionally prunes the ops directory. v0.1
  compaction is mechanical only.

DOCTOR
  'act doctor' verifies git history vs tracker state, including
  orphan-close detection (a commit's message references an issue
  that is still open). The 'Act-Id: act-XXXX' trailer (or, for
  back-compat, the historical '(act-XXXX)' subject-line marker) is
  what doctor greps for; including the trailer in work-commit
  messages is what makes this check work.
`

const helpErrors = `act — error-envelope contract

WHAT THIS IS
  Every act subcommand that fails under --json emits a single JSON
  object on stdout and exits non-zero. stderr stays empty so an agent
  parsing stdout never has to also drain stderr. Without --json, the
  same failure prints a one-line human-readable message to stderr.

  This page is the canonical contract. If you are implementing a new
  error path, you should not need to read internal/cli/errors.go to
  do it correctly — the shape, the code list, and the length rules
  are all here.

ENVELOPE SHAPE
  {
    "error":   "<code-slug>",     // required, stable identifier (snake_case)
    "message": "<human-readable>", // required, may be empty string
    "details": { ... }             // required key; always an object,
                                   // possibly empty {}.  Never null,
                                   // never absent.
  }

  Field order in the encoded JSON is fixed (error, message, details)
  to keep golden tests stable. The "details" key is always present
  even when empty so downstream parsers can rely on it.

ERROR CODES (STABLE; RENAMING IS A BREAKING CHANGE)
  Two tiers, both surfaced verbatim in the "error" field:

  Spec canonical codes (per spec-v2.md §error-envelope):
    not_in_git              act run outside a git repo
    act_not_initialized     .act/ missing in this repo
    issue_not_found         id resolved to nothing
    id_ambiguous            short id matched 2+ issues
    version_skew            on-disk schema newer than this binary
    claim_lost              concurrent claim won the race
    cycle_detected          dep edge would close a cycle
    dep_not_found           dep target id resolved to nothing
    hook_failed             user pre-commit / pre-push hook returned non-zero
    op_invalid              op-file payload failed validation
    hlc_drift               clock moved backwards beyond tolerance
    index_corrupt           SQLite cache failed integrity check
    import_invalid_jsonl    'act import' could not parse a line
    compaction_locked       another compaction is in progress
    redact_target_not_found 'act redact' target id missing

  Internal / per-command codes (also stable; tests pin them):
    bad_flag                argparse rejected a flag/value
    ambiguous_id            legacy alias for id_ambiguous
    cycle                   legacy alias used by 'dep add'
    claim_failed            claim could not write or push
    config_read_failed      .act/config.json unreadable
    index_open_failed       SQLite open failed
    index_query_failed      SQLite query failed
    index_rebuild_failed    cache rebuild failed
    index_update_failed     cache write failed
    envelope_invalid        op file's envelope schema invalid
    payload_invalid         op file's payload failed Validate()
    marshal_failed          json.Marshal returned an error
    fold_failed             folding op log into state failed
    ops_scan_failed         walking .act/ops failed
    ops_walk_failed         walking .act/ops failed (alt site)
    ops_read_failed         reading an op file failed
    push_failed             git push returned non-zero
    write_failed            os.WriteFile failed
    stat_failed             os.Stat failed
    walk_failed             generic filepath.Walk failure
    no_repo                 git rev-parse showed no repo (subset of not_in_git)
    import_failed           'act import' wrote nothing usable

  When in doubt, add a new constant in internal/cli/errors.go rather
  than reusing one. Callers pin codes; rename = breakage.

DETAILS KEYS PROMOTED VERBATIM
  Normalize() lifts these well-known keys from legacy payloads into
  details automatically; new code should also place them under
  details rather than at the top level:

    candidates, path, winner, winner_node_id, winner_hlc,
    stderr_tail, issue_id, query, prefix, target, exit_code, hook

  stderr_tail is capped at MaxStderrTail = 4096 bytes (last N bytes
  preserved — most recent output is most diagnostic).

LENGTHS ARE BYTE-COUNTED
  All length checks across act use Go's len() on the raw string,
  which counts BYTES, not runes. Precedent lives in
  internal/op/payloads.go (e.g. create.title <= 200, accept[i] <= 500,
  close.reason <= 500). Apply the same rule when adding new
  validations: a 200-byte cap with a multibyte UTF-8 string may
  reject before 200 user-visible characters, and that is intentional —
  it gives a stable, language-independent ceiling for op file size.
  Document any rune-counted exception explicitly in the error
  message.

EMITTING AN ERROR
  - Build an Envelope via cli.New(code, message, details). nil
    details is fine (encodes as {}).
  - Call cli.Emit(env, asJSON, stdout, stderr). With asJSON=true the
    JSON goes to stdout and stderr stays empty; with asJSON=false
    a single line goes to stderr (message if non-empty, else code,
    no period, no ANSI).
  - For legacy / ad-hoc payload shapes, cli.Normalize(payload)
    squashes them into the canonical envelope without losing fields.

EXIT CODES
  Any error path: exit non-zero (typically 1 for runtime failures,
  2 for usage errors from flag parsing). The envelope is emitted
  before exit; success paths emit no envelope at all.
`
