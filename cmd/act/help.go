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
// Topics: workflow, ops-model. Anything else is a usage error.
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
	default:
		fmt.Fprintf(stderr, "act help: unknown topic %q\n", rest[0])
		fmt.Fprintln(stderr, "topics: workflow, ops-model")
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

THE CANONICAL WORK LOOP (use this in every session)
  1. act ready                    # what's unblocked, ordered by priority
  2. act update --claim <id>      # take it (atomic; concurrent claimers
                                  #          resolve via last-write-wins)
  3. <do the work, write tests, run them>
  4. git commit -m "<summary> (act-<short-id>)"
                                  # the (act-XXXX) marker lets
                                  # 'act doctor' correlate work commits
                                  # with closed issues
  5. act close <id> [--reason "..."]

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
  act <subcommand> --help   # flag reference for any subcommand

  Subcommands:
    init version log list search ready show
    create close reopen delete update redact
    dep add doctor import mcp
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
    Implement, write tests, run them. Each work commit's message
    should embed the issue's commit_marker so 'act doctor' can
    correlate:

      git commit -m "implement <thing> (act-XXXX)"

    If you find a bug or surface gap mid-flight, file it as a
    follow-up but keep working on the current issue:

      $ act create "<follow-up title>" --type bug \
          --description "<repro>" --accept "<resolution criterion>"

  Closing
    $ act close <id> --reason "<one-liner>"

EXAMPLE SESSION (CLI)
  $ act ready --json | jq -r '.ready[0].id'
  act-c26a
  $ act update --claim act-c26a
  $ # ... edit code, write tests, run them ...
  $ git commit -am "implement --blocked-by flag (act-c26a)"
  $ act close act-c26a --reason "all 5 acceptance criteria green"

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
  that is still open). The (act-XXXX) commit_marker is what doctor
  greps for; including it in work-commit messages is what makes
  this check work.
`
