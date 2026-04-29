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
	"github.com/aac/act/internal/index"
	"github.com/aac/act/internal/op"
)

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
//  3. If !opts.NoCommit: stage the new file and commit with the spec's
//     `act-op: <id_short> <op_type>` message.
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
	idShort := env.IssueID
	if len(idShort) > 4 {
		// The shortest-unique-prefix layer (act-6991) materializes a
		// shorter prefix when one is unique. At write time we do not yet
		// have access to the prefix index, so we fall back to the first 4
		// hex characters of the id (matching the on-disk prefix-of-id
		// convention) for human readability of the commit subject. The
		// full id is always present in the op file's path.
		idShort = env.IssueID[:4]
	}
	msg := fmt.Sprintf("act-op: %s %s", idShort, env.OpType)
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
