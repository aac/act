package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/hooks"
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/index"
	"github.com/aac/act/internal/op"
)

// CommitMarkerLen is the length of the parenthesized short id used in
// auto-commit subjects (`(act-XXXX)`), counting the `act-` prefix plus the
// first MinShortHexLen hex characters. Doctor's orphan-close grep keys on
// this exact marker, so the constant is load-bearing across packages.
const CommitMarkerLen = len("act-") + ids.MinShortHexLen

// ShortIssueID returns the canonical short id used in auto-commit subjects
// for the given full issue id. For ids that already match `act-` + at least
// MinShortHexLen hex characters, the prefix is truncated to that length.
// For shorter / malformed ids the input is returned verbatim — the commit
// is still written but doctor's grep may not match it; that is the correct
// behavior because the input is itself non-canonical.
func ShortIssueID(full string) string {
	if len(full) > CommitMarkerLen {
		return full[:CommitMarkerLen]
	}
	return full
}

// BuildOpCommitMessage returns the canonical auto-commit subject for a
// single-op write. The format is `act-op: (act-XXXX) <op_type>` — the
// parenthesized short id is required so `act doctor orphan-close` (which
// greps for the literal `(act-XXXX)` marker) keeps matching every op
// commit, not only closes. This single helper is the only place the
// format is built; per-command call sites must NOT construct the subject
// inline (act-d3a5 lesson: three different inline templates produced three
// different shapes within a single session).
func BuildOpCommitMessage(env op.Envelope) string {
	return fmt.Sprintf("act-op: (%s) %s", ShortIssueID(env.IssueID), env.OpType)
}

// BuildBatchCommitMessage returns the canonical auto-commit subject for a
// multi-op batch covering a single issue id (the batch must be homogeneous
// in issue_id; spec §5.D.2). The format is
// `act-op: (act-XXXX) <op_type> [+N]` where `+N` is the count of ops
// beyond the first. For a single-op batch the count suffix is omitted, so
// the message is byte-identical to BuildOpCommitMessage(env).
//
// The shared helper ensures cascade tombstones, act_block-style composed
// tools, and any future multi-op write all produce subjects that doctor's
// grep recognizes.
func BuildBatchCommitMessage(env op.Envelope, count int) string {
	base := BuildOpCommitMessage(env)
	if count <= 1 {
		return base
	}
	return fmt.Sprintf("%s +%d", base, count-1)
}

// hookTimeout is the wall-clock limit applied to a single hook
// invocation, per spec §"Hooks contract" step 7.
const hookTimeout = 5 * time.Second

// WriteOpts encodes the universal-flag knobs that apply to every write
// command (per spec §4 + the act-5ca9 acceptance criteria). Its fields are
// chosen so the zero value matches the spec default: commit, do not push,
// not isolated.
type WriteOpts struct {
	// NoCommit, when true, suppresses the auto-commit step. The op file is
	// still written to disk; the caller is responsible for downstream
	// staging.
	NoCommit bool
	// Push, when true, runs `git push` after the commit. Combining with
	// NoCommit or Isolated yields ErrInvalidFlags (exit 2).
	Push bool
	// Isolated, when true, signals the offline path: the commit happens but
	// no network operation runs (no pull-rebase, no push).
	Isolated bool
}

// ErrInvalidFlags is returned when WriteOpts contains an illegal flag
// combination per spec §4: --no-commit + --push, or --isolated + --push.
var ErrInvalidFlags = errors.New("cli: invalid flag combination")

// WriteOpAndAutoCommit encapsulates the standard write-then-auto-commit
// flow shared by `act create`, `act update`, `act close`, `act dep add`,
// and other write commands.
//
// Steps:
//  1. Validate flag combinations (§4).
//  2. Write the op file via op.ProbeAndWrite into paths.Ops.
//  3. If !opts.NoCommit: stage the new file and commit with the canonical
//     `act-op: (act-XXXX) <op_type>` message produced by
//     BuildOpCommitMessage. The parenthesized marker is required by
//     doctor's orphan-close grep (spec §act doctor orphan-close).
//  4. If opts.Push: invoke gitops.Push(); a missing remote yields ErrNoRemote
//     which the caller may surface as exit 2.
//
// gitops may be nil iff opts.NoCommit is true; otherwise the caller must
// provide a configured *gitops.GitOps.
func WriteOpAndAutoCommit(env op.Envelope, body []byte, paths config.LayoutPaths, gops *gitops.GitOps, opts WriteOpts) error {
	if opts.NoCommit && opts.Push {
		return fmt.Errorf("%w: --no-commit and --push are mutually exclusive", ErrInvalidFlags)
	}
	if opts.Isolated && opts.Push {
		return fmt.Errorf("%w: --isolated and --push are mutually exclusive", ErrInvalidFlags)
	}
	if !opts.NoCommit && gops == nil {
		return fmt.Errorf("cli: gitops is required unless --no-commit is set")
	}

	// Step 2: write the op file.
	fsLock := func() (func(), error) { return func() {}, nil }
	opPath, _, err := op.ProbeAndWrite(paths.Ops, env, body, fsLock)
	if err != nil {
		return fmt.Errorf("cli: write op: %w", err)
	}

	if opts.NoCommit {
		return nil
	}

	// Step 3: stage + commit.
	if err := gops.StageOpFile(opPath); err != nil {
		return fmt.Errorf("cli: stage: %w", err)
	}
	// Step 3a: run the matching hook (if any). Hooks fire pre-commit
	// per spec §"Hooks contract"; on failure we unstage and delete the
	// op file so the working tree returns to its pre-attempt state and
	// bubble up the HookFailedError unchanged.
	if hookPath, ok := hooks.ResolveHook(paths.Hooks, env.OpType); ok {
		opID, herr := env.Hash()
		if herr != nil {
			_ = unstage(gops, opPath)
			return fmt.Errorf("cli: hash op for hook: %w", herr)
		}
		hctx := hooks.HookContext{
			OpID:    opID,
			OpType:  env.OpType,
			IssueID: env.IssueID,
			Phase:   hooks.PhasePreCommitOp,
			OpJSON:  body,
		}
		if err := hooks.Run(hctx, hookPath, hookTimeout); err != nil {
			_ = unstage(gops, opPath)
			_ = os.Remove(opPath)
			return err
		}
	}
	// Auto-commit subject is built by BuildOpCommitMessage; the canonical
	// format is `act-op: (act-XXXX) <op_type>`. Doctor's orphan-close
	// check greps for the literal parenthesized marker, so every write op
	// (not just close) embeds it. See act-d3a5.
	msg := BuildOpCommitMessage(env)
	if err := gops.Commit(msg); err != nil {
		// Best-effort un-stage so the working tree returns to its
		// pre-attempt state; the op file is intentionally left on disk so
		// the user can retry without rebuilding the envelope.
		_ = unstage(gops, opPath)
		return fmt.Errorf("cli: commit: %w", err)
	}

	// Step 4: optional push.
	if opts.Push {
		if err := gops.Push(); err != nil {
			return fmt.Errorf("cli: push: %w", err)
		}
	}
	return nil
}

// WriteOpsAndAutoCommit writes a batch of op files and commits all of them
// in a single git commit, per spec §5.D.2. Used by composed tools (act_block)
// that require multi-op atomicity:
//
//  1. Validate flag combinations.
//  2. For each (env, body) pair: write the op file via op.ProbeAndWrite.
//  3. Stage every newly-written op file.
//  4. Commit once with the supplied message.
//  5. On any failure between (2) and (4), unstage and delete every staged
//     op file so the worktree returns to its pre-attempt state. The
//     underlying error is surfaced unchanged.
//
// Unlike WriteOpAndAutoCommit, this helper does NOT run hooks: composed
// multi-op flows are expected to be opaque to per-op-type hooks. Callers
// that need hook invocation should compose the single-op helper instead.
//
// envs and bodies must have the same length and be non-empty. gops may be
// nil iff opts.NoCommit is true. The commit message is supplied by the
// caller; act_block uses `act-block: <short>` per spec §"MCP composed".
func WriteOpsAndAutoCommit(envs []op.Envelope, bodies [][]byte, paths config.LayoutPaths, gops *gitops.GitOps, opts WriteOpts, commitMessage string) error {
	if len(envs) == 0 {
		return fmt.Errorf("cli: WriteOpsAndAutoCommit: no ops supplied")
	}
	if len(envs) != len(bodies) {
		return fmt.Errorf("cli: WriteOpsAndAutoCommit: envs/bodies length mismatch (%d vs %d)", len(envs), len(bodies))
	}
	if opts.NoCommit && opts.Push {
		return fmt.Errorf("%w: --no-commit and --push are mutually exclusive", ErrInvalidFlags)
	}
	if opts.Isolated && opts.Push {
		return fmt.Errorf("%w: --isolated and --push are mutually exclusive", ErrInvalidFlags)
	}
	if !opts.NoCommit && gops == nil {
		return fmt.Errorf("cli: gitops is required unless --no-commit is set")
	}
	if !opts.NoCommit && commitMessage == "" {
		return fmt.Errorf("cli: WriteOpsAndAutoCommit: empty commit message")
	}

	// Step 2: write all op files. Track each path so we can roll back on
	// failure.
	fsLock := func() (func(), error) { return func() {}, nil }
	written := make([]string, 0, len(envs))
	rollback := func() {
		for _, p := range written {
			if gops != nil {
				_ = unstage(gops, p)
			}
			_ = os.Remove(p)
		}
	}
	for i, env := range envs {
		opPath, _, err := op.ProbeAndWrite(paths.Ops, env, bodies[i], fsLock)
		if err != nil {
			rollback()
			return fmt.Errorf("cli: write op %d/%d: %w", i+1, len(envs), err)
		}
		written = append(written, opPath)
	}

	if opts.NoCommit {
		return nil
	}

	// Step 3: stage every op file.
	for _, p := range written {
		if err := gops.StageOpFile(p); err != nil {
			rollback()
			return fmt.Errorf("cli: stage: %w", err)
		}
	}

	// Step 4: single commit.
	if err := gops.Commit(commitMessage); err != nil {
		rollback()
		return fmt.Errorf("cli: commit: %w", err)
	}

	// Step 5: optional push (after successful commit).
	if opts.Push {
		if err := gops.Push(); err != nil {
			return fmt.Errorf("cli: push: %w", err)
		}
	}
	return nil
}

// RefreshIndexForIssue refolds a single issue from on-disk ops and upserts
// the resulting state into the live SQLite index at paths.IndexDB. Call this
// after a successful op write so the index stays in sync with the op log
// without requiring a doctor --fix rebuild. Errors are surfaced verbatim;
// callers typically ignore them (since the op log is the source of truth)
// but may choose to log them for observability.
//
// If the index file does not yet exist or the schema has not been applied,
// this helper opens, applies, and closes a handle on each call. The cost is
// dominated by the per-issue fold, which is bounded by the issue's op count.
func RefreshIndexForIssue(paths config.LayoutPaths, issueID string) error {
	state, err := fold.FoldIssue(paths.Ops, issueID, fold.ApplyDispatch)
	if err != nil {
		return fmt.Errorf("cli: refresh index: fold %s: %w", issueID, err)
	}
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		return fmt.Errorf("cli: refresh index: open: %w", err)
	}
	defer func() { _ = idx.Close() }()
	if err := idx.ApplySchema(); err != nil {
		return fmt.Errorf("cli: refresh index: apply schema: %w", err)
	}
	if err := idx.Upsert(state); err != nil {
		return fmt.Errorf("cli: refresh index: upsert %s: %w", issueID, err)
	}
	return nil
}

// unstage runs `git restore --staged <opPath>` via the gitops runner. The
// operation is best-effort; failures are returned but typical callers
// ignore the result because the original commit error is the user-facing
// signal.
func unstage(g *gitops.GitOps, opPath string) error {
	// We deliberately reuse the public StageOpFile-like indirection by
	// running `git restore --staged` directly. To avoid extending the
	// public API surface for one call site, we shell out via exec here.
	return runUnstage(g.RepoRoot, opPath)
}
