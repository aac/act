// Package gitops provides a concrete, shellout-based implementation of the
// git mutations needed by the writer pipeline (auto-commit, push, atomic
// claim's pull-rebase, and squash-of-contiguous-act-op-range).
//
// Design notes:
//
//   - Every method invokes /usr/bin/env git via os/exec with a fixed working
//     directory (RepoRoot). No shell is involved, so paths with spaces and
//     unusual characters round-trip safely.
//   - Default verify behavior matches spec §5.B: op-commits use --no-verify
//     because the commit only touches .act/ops/**, which the host's
//     pre-commit hooks should not police. Set Verify=true to opt in.
//   - The concrete *GitOps satisfies the claim.GitOps interface declared by
//     act-9824 (Commit / PullRebase / Push). No adjustment to that interface
//     was required; this package's API is a strict superset.
package gitops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aac/act/internal/claim"
	"github.com/aac/act/internal/config"
)

// TestPushInvocationCount is a process-global counter incremented every time
// (*GitOps).CommitAndAutoPush successfully reaches the PushWithRetry call
// (i.e. origin was configured and the commit succeeded). It exists for
// tests that want to assert "the write helper actually wired the push"
// without parsing git output. Production code never reads it.
//
// Tests using this counter MUST snapshot the value at start and compare
// against the new value at end; resetting is not necessary because the
// counter is monotonic and reads are atomic. Concurrent tests are safe
// (atomic.Int64) but the absolute value is process-global so parallel
// tests should diff against snapshots, not absolute values.
var TestPushInvocationCount atomic.Int64

// TestOrchestratorSyncFireCount is a process-global counter incremented
// every time (*GitOps).maybeFireOrchestratorSync (the Phase 2 ticket-6b
// trigger) decides the role is orchestrator AND successfully starts the
// background `act remote sync` child process. Workers and unset-role
// repos do NOT increment the counter; a Start() failure does NOT
// increment it either. Tests snapshot the value at start and compare
// against the new value at end — same monotonic-counter contract as
// TestPushInvocationCount. Production code never reads it.
var TestOrchestratorSyncFireCount atomic.Int64

// Compile-time assertion: *GitOps satisfies the claim.GitOps interface
// declared by act-9824. If a future signature drift breaks this, the build
// will fail loudly here rather than at the call site.
var _ claim.GitOps = (*GitOps)(nil)

// ErrNoRemote is returned by PullRebase and Push when the working tree has
// no upstream configured. Callers translate this to spec exit code 2 (usage
// error) when the user explicitly asked for --push.
//
// Aliased to claim.ErrNoUpstream so the claim package's PullRebase
// short-circuit (act-fdb2) detects it via errors.Is without the gitops
// package having to expose a second sentinel.
var ErrNoRemote = claim.ErrNoUpstream

// ErrPullRebaseDirtyTree is returned by PullRebase when `git pull --rebase`
// refuses because the working tree has unstaged changes (`error: cannot pull
// with rebase: You have unstaged changes.`). The most common cause under
// Phase 1's nested-repo layout is `.act/index.db`: it is tracked in the
// nested `.act/.git` but gets re-written on every read (the SQLite read
// cache). A subsequent write's pre-step pull-rebase then trips this guard
// even though the local write itself succeeds (act-68f08b).
//
// Aliased to claim.ErrPullRebaseSoftFail so the claim package can swallow
// this specific failure mode the same way it swallows ErrNoUpstream — by
// the time PullRebase fires the local commit is already durable on disk,
// and the op log is convergent: the next read/write will re-fetch and
// reconcile. Other PullRebase failures (rebase conflict on .act/ops/**,
// network error, auth) remain hard errors.
var ErrPullRebaseDirtyTree = claim.ErrPullRebaseSoftFail

// GitOps is a concrete implementation of the git side-effects used by the
// claim and write-op flows. The zero value is not safe; use NewGitOps.
type GitOps struct {
	// RepoRoot is the absolute path to the working tree root. All git
	// commands run with -C <RepoRoot>; relative paths passed to StageOpFile
	// are resolved by git relative to this directory.
	RepoRoot string
	// Verify, when true, causes Commit to omit --no-verify so the host's
	// pre-commit hooks run. Default (false) matches spec §5.B.
	Verify bool

	// gitDir, when non-empty, pins git's repo discovery: every invocation
	// is prefixed with `--git-dir=<gitDir> --work-tree=<RepoRoot>` so git
	// cannot walk up from RepoRoot and find an enclosing host repo. Set
	// by NewActGitOps (and only by NewActGitOps) so the act-state handle
	// is type-system AND argv-level isolated from the host repo. Leaving
	// gitDir empty preserves the original cwd-discovery behavior for
	// callers (HostGitOps; NewGitOps used by tests and the claim/squash/
	// import paths whose RepoRoot is the host working tree itself, not a
	// nested act state) — see act-784b.
	gitDir string

	// runner is an internal indirection so tests can assert the exact argv
	// passed to git. Defaults to exec.Command. Exposed via WithRunner.
	runner func(name string, args ...string) *exec.Cmd
}

// NewGitOps constructs a GitOps rooted at repoRoot with default settings
// (Verify=false). Verify can be flipped on the returned struct directly.
func NewGitOps(repoRoot string) *GitOps {
	return &GitOps{RepoRoot: repoRoot, runner: exec.Command}
}

// ActGitOps is the handle authorized to write act ops and query the act
// state's git history. Under Phase 1 of the coordination-plane design
// (docs/coordination-plane-design.md delta item 2) this will target the
// nested .act/ repo; today it shares findRepoRoot with HostGitOps, so both
// resolve to the same working tree. The split is a type-system enforcement
// of the "writes go through act's handle" invariant — every call site that
// stages an op or commits an act change must use *ActGitOps.
//
// ActGitOps is a type alias for *GitOps so it exposes the full write
// surface (Commit, StageOpFile, Push, PullRebase, SquashActOpRange) plus
// all the read helpers. Migration consists of flipping construction calls
// from NewGitOps to NewActGitOps; the method set is identical.
type ActGitOps = GitOps

// NewActGitOps constructs an ActGitOps for the writer side of the dual-
// handle split. The actStateRoot is the nested .act/ working tree
// (filepath.Join(<host>, ".act")); every git invocation is pinned to
// `--git-dir=<actStateRoot>/.git --work-tree=<actStateRoot>` so git's
// repo-discovery walk cannot escape upward into the host repo. This is
// what makes the dual-handle split effective in practice: when the host
// repo gitignores `.act/`, a stray cwd-based git invocation would
// otherwise be rejected by the host's gitignore (act-784b). Forcing the
// git-dir/work-tree at every call site closes that loophole.
//
// If `<actStateRoot>/.git` does not exist, git itself returns a clear
// "fatal: not a git repository" error on the next invocation; we do not
// pre-check here because (a) the existence check would race with `act
// init` running concurrently in the same tree, and (b) git's error is
// already user-actionable.
func NewActGitOps(actStateRoot string) *ActGitOps {
	g := NewGitOps(actStateRoot)
	g.gitDir = filepath.Join(actStateRoot, ".git")
	return g
}

// HostGitOps is the read-only handle act uses to scan the host repo's
// commit log for `(act-XXXX)` markers. Today the host and act states
// share a working tree; under Phase 1 the host repo and the nested act
// repo will be distinct git directories and this handle will target the
// host (the act-9e8c findRepoRoot resolver work).
//
// HostGitOps deliberately exposes only the read surface that doctor and
// show need (RepoRoot, WorkCommitsForIssue). The write methods on the
// underlying *GitOps (Commit, StageOpFile, Push, PullRebase,
// SquashActOpRange) are a compile-time absence from HostGitOps's method
// set — the only way to perform writes is to drop down to *ActGitOps,
// which makes the policy ("act never writes to the host repo") enforced
// by the type system rather than by convention.
type HostGitOps struct {
	inner *GitOps
}

// NewHostGitOps constructs a HostGitOps for the reader side of the dual-
// handle split. The repoRoot argument is the host repo's working tree —
// under Phase 1, the same path as the act state's root; once the nested
// repo migration lands, the two roots diverge and this constructor will
// be passed the host root specifically.
func NewHostGitOps(repoRoot string) *HostGitOps {
	return &HostGitOps{inner: NewGitOps(repoRoot)}
}

// RepoRoot returns the working tree path the host handle targets.
func (h *HostGitOps) RepoRoot() string {
	return h.inner.RepoRoot
}

// WorkCommitsForIssue surfaces the `(act-<markerHex>` marker grep against
// the host repo's git log. Read-only operation — see *GitOps.WorkCommitsForIssue
// for the contract.
func (h *HostGitOps) WorkCommitsForIssue(markerHex string, limit int) ([]WorkCommit, error) {
	return h.inner.WorkCommitsForIssue(markerHex, limit)
}

// AllMarkers scans the host repo's full git log for any `(act-XXXX` or
// `Act-Id: act-XXXX` markers and returns one record per (sha, markerID)
// pair. Doctor's reconcile-lite uses this to find:
//   - case (a): markers in code with no matching issue in act state.
//   - case (d): markers referencing unknown ids (and dispatches the
//     external-PR heuristic on author email to suppress fork merges).
//
// Read-only operation. The grep pattern mirrors WorkCommitsForIssue but
// captures the full id rather than checking a specific one.
func (h *HostGitOps) AllMarkers() ([]MarkerCommit, error) {
	return h.inner.AllMarkers()
}

// InternalContributors returns the set of author emails from the host
// repo's recent history (most recent `limit` commits). Used by the
// external-PR heuristic: a marker authored by an email outside this set
// is treated as a fork-PR contribution and case (d) warnings are
// suppressed for it. The limit is intentionally generous (50 by default)
// so a small team's churn pattern stays inside the set without needing
// per-repo config (delta item 5, act-37f7).
func (h *HostGitOps) InternalContributors(limit int) (map[string]struct{}, error) {
	return h.inner.InternalContributors(limit)
}

// CheckIgnored reports whether `path` (relative to the host repo root)
// is ignored by .gitignore. Doctor's gitignore-effective probe uses
// this to confirm `.act/` is excluded from the host's tracked files
// (Phase 1 OSS-unblock prerequisite).
func (h *HostGitOps) CheckIgnored(path string) (bool, error) {
	return h.inner.CheckIgnored(path)
}

// run executes `git <args...>` with cwd=RepoRoot and returns stdout. stderr
// is included in the error message on failure.
//
// When g.gitDir is non-empty (NewActGitOps), every invocation is prefixed
// with `--git-dir=<gitDir> --work-tree=<RepoRoot>` so git's repo discovery
// is pinned to the nested .act/.git and cannot walk up into an enclosing
// host repo whose .gitignore would refuse the act-state path (act-784b).
func (g *GitOps) run(args ...string) (string, error) {
	r := g.runner
	if r == nil {
		r = exec.Command
	}
	finalArgs := args
	if g.gitDir != "" {
		// Prepend the discovery overrides. Order matters only insofar as
		// these must precede the subcommand name; both forms must use `=`
		// rather than two-arg form so the prefix is positionally robust
		// against any subcommand-specific arg parsing.
		finalArgs = append([]string{
			"--git-dir=" + g.gitDir,
			"--work-tree=" + g.RepoRoot,
		}, args...)
	}
	cmd := r("git", finalArgs...)
	cmd.Dir = g.RepoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w (stderr: %s)",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// StageOpFile runs `git add <opPath>` with cwd=RepoRoot. opPath may be
// absolute or relative to RepoRoot.
func (g *GitOps) StageOpFile(opPath string) error {
	if opPath == "" {
		return fmt.Errorf("gitops: empty op path")
	}
	if _, err := g.run("add", "--", opPath); err != nil {
		return err
	}
	return nil
}

// UnstageOpFile runs `git restore --staged -- <opPath>` against this
// handle. Mirrors StageOpFile so rollback paths route through the same
// git-dir/work-tree override (act-784b). opPath may be absolute or
// relative to RepoRoot.
func (g *GitOps) UnstageOpFile(opPath string) error {
	if opPath == "" {
		return fmt.Errorf("gitops: empty op path")
	}
	if _, err := g.run("restore", "--staged", "--", opPath); err != nil {
		return err
	}
	return nil
}

// Commit creates a single commit with the given message. By default the
// commit uses --no-verify (spec §5.B); set GitOps.Verify=true to run host
// pre-commit hooks. Cross-platform safe: no shell, no /dev/null redirect.
//
// Commit does NOT push. Callers that want the Phase 2 "push every write
// synchronously" semantics use CommitAndAutoPush below; callers with a
// custom pull-rebase-then-push flow (the atomic claim path, importer,
// compaction) keep calling Commit directly and manage push themselves.
func (g *GitOps) Commit(message string) error {
	if message == "" {
		return fmt.Errorf("gitops: empty commit message")
	}
	args := []string{"commit", "-m", message}
	if !g.Verify {
		args = append(args, "--no-verify")
	}
	if _, err := g.run(args...); err != nil {
		return err
	}
	return nil
}

// slowWriteThresholdMs is the wall-clock cutoff above which a successful
// op commit is recorded as a slow write (warning to stderr + JSON-lines
// append to .act/.slow-writes). Mirrors cli.DefaultSlowWriteThresholdMs;
// duplicated here to avoid importing the cli package upward.
const slowWriteThresholdMs = 1000

// slowWriteLogCap mirrors cli.SlowWriteLogCap; same rationale as
// slowWriteThresholdMs (avoid an upward import). The cli package owns
// the read/append helpers, but the threshold/cap constants live close
// to the commit path that consumes them.
const slowWriteLogCap = 100

// envSlowCommitMs is the fault-injection hook that drives ticket-3b's
// integration tests deterministically. When set to a positive integer
// N, every CommitOp invocation sleeps for N milliseconds AFTER the
// stage timestamp is captured but BEFORE git commit runs, so the
// measured duration is at least N ms. Tests use this to force the
// commit duration past the 1000ms threshold without depending on
// disk/git latency.
//
// Semantics: zero/empty/unparseable values disable the hook (no
// sleep). The sleep happens on every CommitOp call while the env var
// is set; tests that want one-shot semantics unset the env between
// invocations.
//
// This is a TEST hook. Production code never sets it. The hook is
// not build-tagged (the ticket allowed gating to `acttest`, but a
// runtime env-var check costs ~50ns and keeps test/prod binaries
// identical, which matches the existing ACT_TEST_FAIL_PUSH_AFTER
// pattern in push_retry.go).
const envSlowCommitMs = "ACT_TEST_SLOW_COMMIT_MS"

// SlowWriteContext carries the op metadata that CommitOp embeds into a
// `.act/.slow-writes` record when the commit duration exceeds the
// threshold. The zero value disables slow-write logging entirely —
// callers without op metadata (e.g. squash, harvest) keep using
// Commit() and skip the measurement.
type SlowWriteContext struct {
	// OpType is one of `create|close|update|dep_add|reopen|delete`. The
	// schema-pinned op_type field per the ticket-3b spec.
	OpType string
	// OpID is the full id of the op being committed (e.g.
	// `act-abc123def456`). Read directly from env.Hash() at the call
	// site.
	OpID string
	// StateRoot is the directory `.act/.slow-writes` lives under (the
	// nested .act/ working tree root). When empty, slow-write logging
	// is disabled (the warning still fires on stderr).
	StateRoot string
	// ThresholdMs overrides slowWriteThresholdMs when non-zero. Tests
	// use this to drive the slow path with a smaller fault-injected
	// sleep; production callers leave it zero to take the default.
	ThresholdMs int64
}

// CommitOp is Commit() instrumented with the Phase 2 ticket-3b slow-write
// observation: it measures monotonic time from immediately before the
// `git commit` invocation to immediately after, and if the elapsed time
// exceeds the threshold (default slowWriteThresholdMs, overridable via
// ctx.ThresholdMs) it emits a stderr warning AND appends a JSON-line
// record to `<ctx.StateRoot>/.slow-writes` (capped at slowWriteLogCap
// entries via the cli appender).
//
// Stderr warning format (literal, asserted by TestDocClaim_SlowWrite_
// WarningText):
//
//	act: slow write detected (<n>ms > <threshold>ms threshold); see .act/.slow-writes
//
// Append-log record schema (also asserted at the docclaim boundary):
//
//	{"timestamp":"<RFC3339-millis-UTC>","op_id":"<full-id>",
//	 "duration_ms":<int>,"op_type":"<one-of-six>"}
//
// Test fault-injection: ACT_TEST_SLOW_COMMIT_MS=<n> introduces a sleep
// of exactly N milliseconds between the stage timestamp and the git
// commit invocation so tests can drive the threshold deterministically.
// The sleep is consulted at the seam between the stage point and the
// commit call so the measured duration includes it.
//
// Failure modes:
//   - The commit itself is the source of truth: a commit error is
//     returned unchanged, no slow-write log is appended.
//   - A successful commit followed by a failed slow-write append is
//     returned as nil (commit succeeded); the append-log error is
//     swallowed because slow-write observability never blocks the
//     write path.
//
// Callers without op metadata (composed/squash/harvest) keep using
// Commit() directly; the slow-write measurement is opt-in via this
// method.
func (g *GitOps) CommitOp(message string, ctx SlowWriteContext) error {
	if message == "" {
		return fmt.Errorf("gitops: empty commit message")
	}
	// Stage→commit measurement starts NOW: by contract, the caller has
	// already finished `git add` for the op file (and any pending op
	// files in a batch); the duration we report is the wall-clock time
	// spent inside the commit itself, including any hook delays the
	// host installs on the act-state repo. Under Phase 1 the nested
	// .act/ repo uses --no-verify so host pre-commit hooks don't fire
	// here, but a slow `git commit` (e.g. due to a large index) is
	// still observable.
	start := time.Now()

	// Fault-injection hook: ACT_TEST_SLOW_COMMIT_MS=<n> sleeps for N
	// milliseconds before the git commit so the measured duration is
	// at least N ms. Tests rely on this to drive the slow path without
	// real disk/git latency. Documented adjacent to the measurement
	// code per ticket-3b spec.
	if v := os.Getenv(envSlowCommitMs); v != "" {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil && ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
	}

	args := []string{"commit", "-m", message}
	if !g.Verify {
		args = append(args, "--no-verify")
	}
	if _, err := g.run(args...); err != nil {
		return err
	}

	elapsedMs := time.Since(start).Milliseconds()
	threshold := ctx.ThresholdMs
	if threshold <= 0 {
		threshold = slowWriteThresholdMs
	}
	if elapsedMs > threshold {
		// Stderr warning — literal format pinned by ticket-3b
		// (TestDocClaim_SlowWrite_WarningText asserts the prefix and
		// suffix substrings).
		fmt.Fprintf(os.Stderr, "act: slow write detected (%dms > %dms threshold); see .act/.slow-writes\n", elapsedMs, threshold)
		// Append a JSON-line record. Failure here is non-fatal: the
		// commit has already landed and the warning has surfaced
		// to stderr — losing the structured record is worse than
		// failing the write but not by much. We swallow rather
		// than propagate.
		if ctx.StateRoot != "" {
			_ = appendSlowWriteRecord(ctx.StateRoot, elapsedMs, ctx.OpID, ctx.OpType)
		}
	}
	return nil
}

// appendSlowWriteRecord writes one JSON-line entry to
// <stateRoot>/.slow-writes and prunes to the newest slowWriteLogCap
// records. Implementation deliberately duplicates the cli appender's
// rewrite-temp-then-rename pattern to avoid an upward import; the cli
// package owns the read/parse side, but the gitops package writes
// directly so a slow-commit observation doesn't require routing back
// up through cli.
func appendSlowWriteRecord(stateRoot string, durationMs int64, opID, opType string) error {
	rec := struct {
		Timestamp  string `json:"timestamp"`
		OpID       string `json:"op_id"`
		DurationMs int64  `json:"duration_ms"`
		OpType     string `json:"op_type"`
	}{
		Timestamp:  time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		OpID:       opID,
		DurationMs: durationMs,
		OpType:     opType,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("gitops: marshal slow-write record: %w", err)
	}
	path := filepath.Join(stateRoot, ".slow-writes")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("gitops: mkdir for slow-writes: %w", err)
	}
	existing, err := readSlowWriteLines(path)
	if err != nil {
		return err
	}
	existing = append(existing, string(line))
	if len(existing) > slowWriteLogCap {
		existing = existing[len(existing)-slowWriteLogCap:]
	}
	var b strings.Builder
	for _, l := range existing {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	// Temp-then-rename atomic replace.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("gitops: tmp for slow-writes: %w", err)
	}
	tmpPath := tmp.Name()
	if _, werr := tmp.Write([]byte(b.String())); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("gitops: write tmp for slow-writes: %w", werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("gitops: close tmp for slow-writes: %w", cerr)
	}
	if rerr := os.Rename(tmpPath, path); rerr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("gitops: rename tmp to slow-writes: %w", rerr)
	}
	return nil
}

// readSlowWriteLines reads the existing .slow-writes file and returns
// its non-empty lines. Missing file returns (nil, nil). Used only by
// appendSlowWriteRecord for the cap-prune cycle.
func readSlowWriteLines(path string) ([]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("gitops: read slow-writes: %w", err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

// AutoPushAfterCommit is the Phase 2 ticket-3a synchronous publish step.
// Callers invoke it AFTER a successful Commit; it inspects `git remote`
// for `origin`, and if origin is configured, resolves the current branch
// and runs (*GitOps).PushWithRetry with default PushOpts (5 retries,
// 100ms base / 1s cap exponential backoff). The result categories:
//
//   - origin NOT configured → return nil silently (local-only / single-
//     machine path; the op log stays consistent without a remote).
//   - origin configured, push succeeds → return nil.
//   - origin configured, push exhausts retries → return *PushExhaustedError
//     unchanged so callers use errors.As to recover RetryCount /
//     ShallowUnshallowAttempted. A fetch failure encountered mid-loop is
//     retried to exhaustion and ends up carried in
//     PushExhaustedError.LastError — PushWithRetry never bubbles a bare
//     ErrFetchFailed out of the loop, so the write path surfaces
//     push_exhausted, not remote_unreachable (act-6d9546).
//   - Other push failures (e.g. auth) → return wrapped error.
//
// On entry into the PushWithRetry call (origin IS configured), the
// helper increments TestPushInvocationCount by exactly 1 — tests use
// the counter to assert "this write helper actually wired the publish"
// without parsing git output.
//
// Used by the write-helpers in internal/cli (WriteOpAndAutoCommit /
// WriteOpsAndAutoCommit) and by the close.go non-helper commit path so
// all six write subcommands (`create`, `close`, `update`, `dep-add`,
// `reopen`, `delete`) publish their ops to the remote on the same call
// that produced them.
//
// Role gate (Phase 2 ticket 1a follow-up): under the future role config,
// workers will skip the synchronous push because the orchestrator
// harvests their ops at dispatch teardown. Today (pre-1a) we always
// push when origin is set; once 1a lands, this method gates the push
// behind `act.role != "worker"`. The TODO is intentional — the
// conservative "always push" default keeps ticket 3a small and lets
// ticket 1a thread the gate cleanly later. See ticket 1a in
// docs/coordination-plane-phase2-plan.md.
//
// The test-only fault injector ACT_TEST_FAIL_PUSH_AFTER (declared in
// internal/gitops/push_retry.go) lets integration tests exercise the
// exhaustion branch deterministically without setting up a contending
// writer. Setting `ACT_TEST_FAIL_PUSH_AFTER=N` causes every push attempt
// at or after the Nth call to behave as if the remote silently rejected
// the receive; the reachability check inside PushWithRetry catches the
// simulated rejection and treats it as a non-fast-forward, retrying
// until MaxRetries is exhausted. With N=1 and the default cap of 5
// retries, every attempt fails and the helper returns
// *PushExhaustedError{RetryCount=5}. See push_retry.go for the precise
// counter semantics and ResetPushAttemptCounter for test setup.
// EnsureBranch switches the nested act-state repo to the named branch,
// creating it from the current HEAD if it does not yet exist. Returns an
// error if the working tree is not a git repository or if the checkout
// itself fails for any reason other than "branch already current".
//
// Used by the --branch <ref> flag on write subcommands (act-5d6a). The
// caller passes the worktree's branch name so the op auto-commit lands
// on a branch independent of whatever the nested repo's HEAD pointed at
// previously. Calling with an empty branch is a no-op (returns nil)
// so callers can unconditionally invoke this before committing without
// branching on the option being set.
//
// Implementation: `git checkout -B <branch>` is idempotent — it creates
// the branch if missing and forces the checkout to point at HEAD if the
// branch already exists. This matches the agent's mental model: "commit
// goes on branch X regardless of where HEAD was."
func (g *GitOps) EnsureBranch(branch string) error {
	if branch == "" {
		return nil
	}
	if _, err := g.run("checkout", "-B", branch); err != nil {
		return fmt.Errorf("gitops: ensure branch %q: %w", branch, err)
	}
	return nil
}

// AutoPushAfterCommitToBranch is AutoPushAfterCommit with an explicit
// target branch override (act-5d6a). When branch is non-empty, the push
// targets `origin <branch>` using the local HEAD's commit, bypassing the
// current-branch resolution and any tracking-config defaults. When branch
// is empty, behavior is identical to AutoPushAfterCommit (current branch
// resolution + push). All other semantics — orchestrator-sync trigger,
// no-origin early return, retry loop, fault-injection counter — are
// preserved exactly.
//
// The explicit branch is needed because worktree subagents may run the
// nested .act/ commit on a branch named after their worktree (so multiple
// agents don't collide on a shared HEAD) while still publishing to a
// dedicated remote branch. Without the override, a stale tracking-config
// `branch.<x>.merge=refs/heads/main` would silently fan their op commits
// onto origin/main, ahead of their pending work commit.
func (g *GitOps) AutoPushAfterCommitToBranch(branch string) error {
	g.maybeFireOrchestratorSync()

	if !g.hasOriginRemote() {
		return nil
	}
	target := branch
	if target == "" {
		var err error
		target, err = g.CurrentBranch()
		if err != nil {
			return fmt.Errorf("gitops: AutoPushAfterCommitToBranch: branch resolution: %w", err)
		}
	}
	TestPushInvocationCount.Add(1)
	return g.PushWithRetry(target, PushOpts{})
}

func (g *GitOps) AutoPushAfterCommit() error {
	// Phase 2 ticket 6b: orchestrator-write upstream-sync trigger. The
	// trigger is gated on `act.role=orchestrator` only — NOT on the
	// presence of an `origin` remote. The orchestrator's own `.act/.git`
	// typically has no `origin` (it IS the canonical history); workers
	// are the ones with `origin` pointing at the orchestrator. Putting
	// the trigger above the no-origin early-return means orchestrator-
	// role repos without `origin` (the common production shape) still
	// republish their writes to `origin-upstream` via the background
	// `act remote sync`. Worker repos (act.role=worker or unset) skip
	// the trigger; their writes reach the orchestrator via PushWithRetry
	// below, and the orchestrator's post-receive hook (ticket 6a) fans
	// them out to `origin-upstream`.
	//
	// Orchestrator detection is config-key-based only — no filesystem-
	// path heuristic (closes OQ #4 per ticket 1a's pinned decision and
	// the ticket 6b addendum). A push failure below does not suppress
	// the sync attempt above; the two legs are independent.
	g.maybeFireOrchestratorSync()

	if !g.hasOriginRemote() {
		// No remote configured: this is the local-only path. Keep silent
		// so tests / single-machine users see no surprise behavior.
		return nil
	}
	branch, err := g.CurrentBranch()
	if err != nil {
		return fmt.Errorf("gitops: AutoPushAfterCommit: branch resolution: %w", err)
	}
	TestPushInvocationCount.Add(1)
	return g.PushWithRetry(branch, PushOpts{})
}

// maybeFireOrchestratorSync fork-execs `act remote sync` in the
// background when `act.role=orchestrator` is set in the nested .act/
// repo's git config (`.act/.git/config`). When the key is unset or
// `worker`, the trigger is a no-op (safe-by-default).
//
// Background detach mechanism (POSIX):
//
//   - cmd.Start() (no cmd.Wait()) so the parent does not block on the
//     child's lifecycle.
//   - cmd.SysProcAttr.Setsid=true puts the child in its own session
//     so it survives the parent's exit and does not inherit the
//     controlling terminal.
//   - cmd.Stdin/Stdout/Stderr are wired to /dev/null. The spawned
//     `act remote sync` writes its own structured JSON-line records to
//     `.act/.sync-log` (via tmp-file rewrite); redirecting our raw
//     stderr to the same file would interleave non-JSON bytes and
//     corrupt the file's JSON-lines invariant. The post-receive hook
//     (ticket 6a) uses `nohup ... >/dev/null 2>&1 &` for the same
//     reason.
//
// The trigger is fire-and-forget by design: this is a publish-leg
// optimization, and a missed fire is recoverable on the next write
// (which will fire again) or by an explicit `act remote sync`. We
// log nothing on the success path and never propagate errors to the
// caller — the post-commit path returns immediately.
//
// g.RepoRoot is the nested .act/ directory for callers constructed via
// NewActGitOps (every CLI writer); the config layer's ActGitConfigPath
// resolves it to `.act/.git/config`. If the role read itself errors
// (e.g. config file missing on a malformed install) we treat the
// failure as RoleUnknown and skip — matching ReadRole's documented
// safe-by-default behavior.
func (g *GitOps) maybeFireOrchestratorSync() {
	if g.RepoRoot == "" {
		return
	}
	configPath := config.ActGitConfigPath(g.RepoRoot)
	role, err := config.ReadRole(configPath)
	if err != nil || role != config.RoleOrchestrator {
		return
	}
	devNull, _ := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	cmd := exec.Command("act", "remote", "sync")
	cmd.Dir = g.RepoRoot
	if devNull != nil {
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if startErr := cmd.Start(); startErr != nil {
		// Best-effort: nothing to do — close handles and move on. The
		// next write will retry the trigger; an explicit `act remote
		// sync` will catch up regardless. We deliberately do NOT
		// increment TestOrchestratorSyncFireCount on a Start failure
		// so the test counter reflects "the trigger reached a spawned
		// child," not just "the role gate let us through."
		if devNull != nil {
			devNull.Close()
		}
		return
	}
	TestOrchestratorSyncFireCount.Add(1)
	// Close the parent's copy of /dev/null. The child has its own
	// duped copies; closing here prevents the parent from holding the
	// file open longer than necessary. Do NOT cmd.Wait(): the child is
	// reparented to init via Setsid and runs independently.
	if devNull != nil {
		devNull.Close()
	}
}

// PullRebase runs `git pull --rebase`. If no upstream is configured the
// method returns ErrNoRemote so the caller can decide whether to surface a
// usage error or silently no-op (e.g. atomic claim with --isolated).
//
// Special-case classification: when `git pull --rebase` refuses because the
// working tree has unstaged changes (the "cannot pull with rebase: You have
// unstaged changes" message), PullRebase returns ErrPullRebaseDirtyTree so
// callers that have already committed a durable local op can demote this to
// a soft failure rather than misleading the user with raw git stderr
// (act-68f08b). The check is intentionally conservative: we match only the
// canonical refuse-due-to-dirty-tree message, leaving all other failure
// modes (rebase conflict, network) to surface as before.
func (g *GitOps) PullRebase() error {
	if _, err := g.upstream(); err != nil {
		return err
	}
	if _, err := g.run("pull", "--rebase"); err != nil {
		if isDirtyTreeRebaseRefusal(err.Error()) {
			return fmt.Errorf("%w: %v", ErrPullRebaseDirtyTree, err)
		}
		return err
	}
	return nil
}

// isDirtyTreeRebaseRefusal reports whether `git pull --rebase`'s stderr
// matches the canonical refuse-due-to-unstaged-changes message. Two
// equivalent phrasings appear across git versions:
//
//	error: cannot pull with rebase: You have unstaged changes.
//	error: cannot pull with rebase: Your index contains uncommitted changes.
//
// Both share the "cannot pull with rebase" prefix; we anchor on that. Case-
// insensitive to be robust to localized git builds (rare, but harmless).
func isDirtyTreeRebaseRefusal(s string) bool {
	return strings.Contains(strings.ToLower(s), "cannot pull with rebase")
}

// Push runs `git push -u origin <current-branch>`. Returns ErrNoRemote if
// the repo has no `origin` remote at all; an unconfigured upstream on the
// branch is still pushable because we pass `-u origin <branch>` explicitly.
func (g *GitOps) Push() error {
	return g.PushToBranch("")
}

// PushToBranch is Push with an explicit target branch override (act-5d6a).
// When branch is non-empty, it is used verbatim as the push destination
// (`git push -u origin <branch>`), bypassing CurrentBranch resolution. When
// empty, falls back to the historical current-branch behavior.
//
// The explicit branch is needed by worktree subagents where the worktree's
// branch differs from whatever the nested .act/ repo's HEAD pointed at;
// without the override, a stale tracking-config could route the op commit
// onto an unintended remote ref.
func (g *GitOps) PushToBranch(branch string) error {
	if !g.hasOriginRemote() {
		return ErrNoRemote
	}
	target := branch
	if target == "" {
		var err error
		target, err = g.CurrentBranch()
		if err != nil {
			return err
		}
	}
	if _, err := g.run("push", "-u", "origin", target); err != nil {
		return err
	}
	return nil
}

// IsClean reports whether the working tree has no staged or unstaged
// changes (`git status --porcelain` produces empty output).
func (g *GitOps) IsClean() (bool, error) {
	out, err := g.run("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// HasNonActChanges reports whether the working tree has any staged or unstaged
// changes outside the .act/ tree. Used by the close path to decide whether to
// auto-commit standalone (clean elsewhere → standalone close commit) or leave
// the staged close op for the agent's next git commit to subsume (act-a659).
//
// Detection: parse `git status --porcelain` and ignore any path with the
// .act/ prefix. The porcelain v1 format puts paths in columns 4..N; rename
// entries (`R `) use ` -> ` between old and new. We treat both endpoints as
// .act/ if either is — a rename moving into or out of .act/ counts as an
// act-only change for our purposes.
func (g *GitOps) HasNonActChanges() (bool, error) {
	out, err := g.run("status", "--porcelain")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		path := line[3:]
		// Handle rename "old -> new"
		if i := strings.Index(path, " -> "); i >= 0 {
			oldPath := strings.TrimSpace(path[:i])
			newPath := strings.TrimSpace(path[i+4:])
			if !isActPath(oldPath) || !isActPath(newPath) {
				return true, nil
			}
			continue
		}
		if !isActPath(strings.TrimSpace(path)) {
			return true, nil
		}
	}
	return false, nil
}

// isActPath reports whether p lives under the .act/ tree at the repo root.
// Quoted paths (porcelain wraps paths containing unusual chars in double
// quotes) are unwrapped first.
func isActPath(p string) bool {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		p = p[1 : len(p)-1]
	}
	return p == ".act" || strings.HasPrefix(p, ".act/")
}

// CurrentBranch returns the short-form current branch name (e.g. "main").
// Detached-HEAD repositories return "HEAD"; callers that need to reject
// detached-HEAD should check the returned value.
func (g *GitOps) CurrentBranch() (string, error) {
	out, err := g.run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// upstream returns the symbolic upstream of the current branch (e.g.
// "origin/main") or ErrNoRemote if none is configured.
func (g *GitOps) upstream() (string, error) {
	out, err := g.run("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		// `git rev-parse @{u}` exits non-zero when there is no upstream;
		// translate any failure here to ErrNoRemote (the upstream check is
		// purely advisory in our flow).
		return "", ErrNoRemote
	}
	up := strings.TrimSpace(out)
	if up == "" {
		return "", ErrNoRemote
	}
	return up, nil
}

// hasOriginRemote returns true iff `git remote` lists an `origin` entry.
func (g *GitOps) hasOriginRemote() bool {
	out, err := g.run("remote")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "origin" {
			return true
		}
	}
	return false
}

// ContiguousActOpRange walks `git log` from HEAD looking back for a
// maximal contiguous run of commits whose subject starts with "act-op:".
// Returns (firstSHA, lastSHA, count, nil): firstSHA is the OLDEST act-op
// commit in the run; lastSHA is HEAD if HEAD itself is an act-op commit;
// count is the run length. If HEAD is not an act-op commit the run is
// empty and (\"\", \"\", 0, nil) is returned.
func (g *GitOps) ContiguousActOpRange() (string, string, int, error) {
	// `git log --format=%H%x00%s` emits SHA<NUL>SUBJECT<LF> so a NUL split
	// makes subject parsing unambiguous even if the subject contains tabs.
	out, err := g.run("log", "--format=%H%x09%s", "HEAD")
	if err != nil {
		return "", "", 0, err
	}
	var first, last string
	count := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		// Format: "<sha>\t<subject>".
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			break
		}
		sha := line[:tab]
		subject := line[tab+1:]
		if !strings.HasPrefix(subject, "act-op:") {
			break
		}
		if last == "" {
			last = sha
		}
		first = sha
		count++
	}
	return first, last, count, nil
}

// SquashActOpRange collapses the contiguous run [firstSHA..lastSHA] (where
// firstSHA is the OLDEST commit in the range and lastSHA is HEAD) into a
// single squashed commit with message
// `act-squash: writer_version=<maxWriterVersion>`.
//
// Returns nil with no side effects when count == 1 (single commit).
//
// maxWriterVersion is supplied by the caller (typically the writer that
// inspected envelopes inside the range). The caller is responsible for the
// version_skew gate per spec §5.B "Squash-and-push refused on version_skew";
// this method is a pure git-level squash and does not consult writer
// versions on its own.
func (g *GitOps) SquashActOpRange(firstSHA, lastSHA, maxWriterVersion string) error {
	if firstSHA == "" || lastSHA == "" {
		return fmt.Errorf("gitops: empty SHA")
	}
	if firstSHA == lastSHA {
		// Single-commit range: no-op.
		return nil
	}
	if maxWriterVersion == "" {
		return fmt.Errorf("gitops: empty maxWriterVersion")
	}
	// Resolve parent of firstSHA. `git rev-parse <sha>^` exits non-zero if
	// firstSHA is the root commit; treat that as an error (squashing the
	// root commit is unsupported).
	parent, err := g.run("rev-parse", firstSHA+"^")
	if err != nil {
		return fmt.Errorf("gitops: parent of %s: %w", firstSHA, err)
	}
	parent = strings.TrimSpace(parent)
	if _, err := g.run("reset", "--soft", parent); err != nil {
		return err
	}
	msg := fmt.Sprintf("act-squash: writer_version=%s", maxWriterVersion)
	args := []string{"commit", "-m", msg}
	if !g.Verify {
		args = append(args, "--no-verify")
	}
	if _, err := g.run(args...); err != nil {
		return err
	}
	return nil
}

// WorkCommit is a single git commit attributed to an issue via the
// `(act-XXXX)` marker convention.
type WorkCommit struct {
	SHA        string `json:"sha"`
	Subject    string `json:"subject"`
	AuthorDate string `json:"author_date"`
}

// WorkCommitsForIssue runs `git log --all --extended-regexp --grep=<pattern>`
// and returns up to limit matching commits, most-recent-first. The pattern
// matches either the historical subject-line form `(act-<markerHex>` or the
// trailer form `Act-Id: act-<markerHex>` introduced in act-c4c5 (see docs/
// coordination-plane-design.md v2.1 "Marker placement"). Both shapes are
// recognized for resolution; the trailer is the only emission form going
// forward. The grep operates against the full commit message (subject +
// body), so trailers in the body are matched cleanly.
//
// The caller passes the hex tail of the canonical commit marker — exactly
// MinShortHexLen hex chars for ids at or above that floor (6 since
// act-f9a0), and the full hex tail verbatim for historical ids that were
// minted shorter than the current floor (e.g. 4-hex ids from pre-act-f9a0
// repos). Result includes commits whose marker is the canonical short form
// OR any longer extended marker that starts with the same prefix (i.e.
// same-issue ids that grew on collision) — the pattern is anchored on the
// `act-<markerHex>` substring, not a fixed-length window.
//
// The function accepts any markerHex of length >= 4 so historical 4-hex
// ids stay matchable; 4 is the on-disk syntax floor (idPattern), not the
// generation floor (MinShortHexLen).
//
// limit=0 means unbounded.
//
// An empty repository (no commits yet) is treated as "no matches" rather
// than an error: `git log` on a repo with no HEAD exits non-zero, but to
// the caller the answer "this issue has no work commits" is the right
// shape.
func (g *GitOps) WorkCommitsForIssue(markerHex string, limit int) ([]WorkCommit, error) {
	if len(markerHex) < 4 {
		return nil, fmt.Errorf("gitops: WorkCommitsForIssue: markerHex length %d < 4", len(markerHex))
	}
	// POSIX ERE alternation matching either:
	//   - the historical subject form `(act-<hex>` (open-paren guards
	//     against arbitrary "act-XXXX" text in unrelated commits), or
	//   - the trailer form `Act-Id: act-<hex>` (any case-sensitive
	//     position; git --grep matches the full message body).
	// `\(` escapes the literal open paren in ERE so it isn't read as a
	// grouping operator.
	pattern := `(\(act-` + markerHex + `|Act-Id: act-` + markerHex + `)`
	args := []string{
		"log", "--all",
		"--extended-regexp",
		"--grep=" + pattern,
		// Tab-separated triplet so we can split unambiguously even if the
		// subject contains tabs (it normally doesn't, but author_date
		// follows ISO-8601 with colons that would confuse a colon split).
		"--pretty=format:%H%x09%s%x09%aI",
	}
	if limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", limit))
	}
	out, err := g.run(args...)
	if err != nil {
		// Empty repo / no HEAD → treat as no matches.
		if strings.Contains(err.Error(), "does not have any commits yet") ||
			strings.Contains(err.Error(), "bad default revision 'HEAD'") {
			return nil, nil
		}
		return nil, err
	}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return nil, nil
	}
	var commits []WorkCommit
	for _, line := range strings.Split(out, "\n") {
		// Format: "<sha>\t<subject>\t<author_date>".
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		commits = append(commits, WorkCommit{
			SHA:        parts[0],
			Subject:    parts[1],
			AuthorDate: parts[2],
		})
	}
	return commits, nil
}

// MarkerCommit is a single (commit, marker-id) pair. One git commit can
// carry multiple markers (rare — e.g. a fix-up that closes two issues —
// but the shape supports it).
type MarkerCommit struct {
	SHA         string `json:"sha"`
	Subject     string `json:"subject"`
	AuthorEmail string `json:"author_email"`
	// IssueID is the canonical short id form (`act-<hex>`) extracted from
	// the marker. Doctor compares this against the act state's id-space.
	IssueID string `json:"issue_id"`
}

// markerPattern matches either the historical subject form
// `(act-<hex>` or the trailer form `Act-Id: act-<hex>`. Capture group 1
// or 2 is the hex tail; the caller normalises to `act-<hex>` form. The
// hex tail is at least 4 chars (idPattern's syntax floor, MinShortHexLen
// is the *generation* floor and unrelated). We do not anchor the right
// side: existing markers can be the short form or the full-id form.
//
// The pattern is compiled once and reused; it operates on commit message
// text (subject + body), not on git's --grep output.
var markerPattern = regexp.MustCompile(`(?:\(act-([0-9a-f]{4,})|Act-Id: act-([0-9a-f]{4,}))`)

// AllMarkers walks `git log --all` and returns one MarkerCommit per
// (commit, marker) pair. The output is in git-log order (most-recent
// first). Empty repos return (nil, nil).
//
// Implementation note: we run `git log --all --grep=` with the marker
// regex to filter at git's level (avoids streaming every commit through
// Go), then re-scan each returned commit message with markerPattern to
// extract the id(s). The --grep is a filter, the re-scan is the
// extractor — git's grep gives us no capture groups so we need both.
func (g *GitOps) AllMarkers() ([]MarkerCommit, error) {
	// Same pattern as WorkCommitsForIssue but without the issue-specific
	// hex constraint. The `[0-9a-f]\\{4,\\}` form is BRE alternation —
	// `--extended-regexp` switches to ERE so `{4,}` is literal.
	pattern := `(\(act-[0-9a-f]{4,}|Act-Id: act-[0-9a-f]{4,})`
	args := []string{
		"log", "--all",
		"--extended-regexp",
		"--grep=" + pattern,
		// %B is the full commit body (subject + body). %x1E (Record
		// Separator) terminates the body so we can split unambiguously
		// even if the body contains newlines, tabs, or any printable
		// char. %x1F (Unit Separator) delimits fields within a record.
		// Standard ASCII control chars almost never appear in real
		// commit messages.
		"--pretty=format:%H%x1F%s%x1F%ae%x1F%B%x1E",
	}
	out, err := g.run(args...)
	if err != nil {
		if strings.Contains(err.Error(), "does not have any commits yet") ||
			strings.Contains(err.Error(), "bad default revision 'HEAD'") {
			return nil, nil
		}
		return nil, err
	}
	out = strings.TrimRight(out, "\x1e\n")
	if out == "" {
		return nil, nil
	}
	var markers []MarkerCommit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		fields := strings.SplitN(rec, "\x1f", 4)
		if len(fields) != 4 {
			continue
		}
		sha, subject, email, body := fields[0], fields[1], fields[2], fields[3]
		for _, m := range markerPattern.FindAllStringSubmatch(body, -1) {
			hex := m[1]
			if hex == "" {
				hex = m[2]
			}
			if hex == "" {
				continue
			}
			markers = append(markers, MarkerCommit{
				SHA:         sha,
				Subject:     subject,
				AuthorEmail: email,
				IssueID:     "act-" + hex,
			})
		}
	}
	return markers, nil
}

// InternalContributors returns the set of author emails counted as
// "regular" contributors over the most-recent `limit` commits on the
// host repo. The heuristic for "regular" is `commit count >= 2` — a
// single-commit author is treated as external (typical of a one-off
// fork PR commit). `limit <= 0` falls back to a sane default (50).
//
// The set is used by doctor's external-PR heuristic to suppress case
// (a)/(d) findings on commits attributed to one-off contributors. The
// threshold is deliberately low: a small team's churn pattern keeps
// every regular's count well above 1, while a fork's drive-by PR
// commit sits at exactly 1 and gets filtered.
//
// Empty repos return (empty-set, nil) so the doctor can still run.
func (g *GitOps) InternalContributors(limit int) (map[string]struct{}, error) {
	if limit <= 0 {
		limit = 50
	}
	args := []string{
		"log", "-n", fmt.Sprintf("%d", limit),
		"--pretty=format:%ae",
	}
	out, err := g.run(args...)
	if err != nil {
		if strings.Contains(err.Error(), "does not have any commits yet") ||
			strings.Contains(err.Error(), "bad default revision 'HEAD'") {
			return map[string]struct{}{}, nil
		}
		return nil, err
	}
	out = strings.TrimRight(out, "\n")
	contribs := map[string]struct{}{}
	if out == "" {
		return contribs, nil
	}
	counts := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		counts[line]++
	}
	for email, n := range counts {
		if n >= 2 {
			contribs[email] = struct{}{}
		}
	}
	return contribs, nil
}

// CheckIgnored runs `git check-ignore <path>` against the repo and
// reports whether the given path (relative to RepoRoot, or absolute) is
// ignored. Doctor uses this to verify .act/ is actually gitignored — a
// missed .gitignore entry would leak nested act state into the host
// repo's tracked history, defeating Phase 1's "outside contributors see
// exactly the code" property.
//
// Returns (true, nil) when ignored, (false, nil) when not ignored,
// (false, err) on any other failure. `git check-ignore` semantics: exit
// 0 = ignored, exit 1 = not ignored (no output), exit 128 = error.
//
// Like every other method on *GitOps, this routes through g.run so the
// runner seam AND the g.gitDir/g.work-tree override apply: a check-ignore
// invoked from a worktree (or a test pinned to a non-default git-dir)
// reflects THAT repo's ignore rules, not whatever git's cwd-discovery
// would walk up to find (act-784b class). The exit-1 ("not ignored")
// case is normal and must be demoted to (false, nil) rather than treated
// as an error; we recover it from the run() error via exec.ExitError.
func (g *GitOps) CheckIgnored(path string) (bool, error) {
	if _, err := g.run("check-ignore", path); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 1:
				// Not ignored: this is the normal "ignore returns false"
				// path, not an error.
				return false, nil
			case 128:
				return false, fmt.Errorf("gitops: check-ignore %q: %w", path, err)
			}
		}
		return false, err
	}
	return true, nil
}
