package cli

import (
	"errors"
	"fmt"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/op"
)

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
