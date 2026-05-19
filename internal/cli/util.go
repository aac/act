package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/hlc"
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

// WorkCommitTrailerKey is the trailer key used by act's work-commit marker.
// Strict `Act-Id:` (capitalized, case-sensitive) — matches `git interpret-
// trailers` semantics so external tooling can parse the marker if desired.
// The trailer goes in the commit body, not the subject; this is the only
// emission form going forward (see act-c4c5 and docs/coordination-plane-
// design.md v2.1 "Marker placement").
const WorkCommitTrailerKey = "Act-Id"

// WorkCommitMarker returns the canonical work-commit marker string an agent
// embeds in their work-commit message for the given full issue id. The
// shape is `Act-Id: act-XXXXXX` (trailer form), where the hex tail is the
// same canonical short id ShortIssueID returns. This marker goes in the
// commit BODY (separated from the subject by a blank line) — it is no
// longer appended to the subject line.
//
// `act show --commit-marker`, the CloseResult.commit_marker field, and the
// MCP act_next response all use this helper so every emission point shares
// the exact same string. Doctor's grep accepts this form (and the historical
// `(act-XXXX)` subject form) when correlating work commits with issues.
func WorkCommitMarker(fullID string) string {
	return WorkCommitTrailerKey + ": " + ShortIssueID(fullID)
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
// invocation. Set to 300s: the dogfood close hook for this repo runs
// gofmt + vet + go test ./..., which after Phase 2's additions lands
// around 140s on a cold cache. The earlier 120s ceiling (act-8277)
// became the limiting factor for every close after the Phase 2 work
// landed (act-492b5b). 300s keeps closes inside a single human-scale
// wait while leaving headroom as the suite grows.
//
// Repos with test suites that legitimately exceed 300s should split
// quick-gate work into the hook and the long tail into CI rather
// than pushing this number further.
const hookTimeout = 300 * time.Second

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
	// Offline, when true, commits locally and skips the synchronous push
	// (Phase 2 ticket 3b). The commit's SHA is appended to
	// `.act/.pending-pushes` so a subsequent non-offline write flushes
	// the deferred publish before its own. Mutually exclusive with
	// --push (ErrInvalidFlags). Combination with --no-commit is silently
	// reduced to --no-commit semantics (no commit → nothing to defer).
	Offline bool
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
// provide a configured *gitops.ActGitOps.
func WriteOpAndAutoCommit(env op.Envelope, body []byte, paths config.LayoutPaths, gops *gitops.ActGitOps, opts WriteOpts) error {
	if opts.NoCommit && opts.Push {
		return fmt.Errorf("%w: --no-commit and --push are mutually exclusive", ErrInvalidFlags)
	}
	if opts.Isolated && opts.Push {
		return fmt.Errorf("%w: --isolated and --push are mutually exclusive", ErrInvalidFlags)
	}
	if opts.Offline && opts.Push {
		return fmt.Errorf("%w: --offline and --push are mutually exclusive", ErrInvalidFlags)
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
			// Phase 1 contract: cwd=host repo root, $ACT_STATE_PATH=
			// nested .act/ dir. paths.Root is "<hostRoot>/.act"; its
			// parent is the host repo root.
			HostRepoRoot: filepath.Dir(paths.Root),
			ActStatePath: paths.Root,
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
	// Phase 2 ticket 3b: use CommitOp so the stage→commit duration is
	// measured and a slow write triggers the stderr warning + structured
	// log append. opID for the slow-write record is the op envelope's
	// hash (full sha256); op_type maps the envelope's OpType verbatim.
	// The hash error is non-fatal — losing the op_id annotation on the
	// rare slow-write record is acceptable when the underlying envelope
	// is malformed enough to fail hashing.
	opIDForSlow, _ := env.Hash()
	swCtx := gitops.SlowWriteContext{
		OpType:    env.OpType,
		OpID:      opIDForSlow,
		StateRoot: gops.RepoRoot,
	}
	if err := gops.CommitOp(msg, swCtx); err != nil {
		// Best-effort un-stage so the working tree returns to its
		// pre-attempt state; the op file is intentionally left on disk so
		// the user can retry without rebuilding the envelope.
		_ = unstage(gops, opPath)
		return fmt.Errorf("cli: commit: %w", err)
	}

	// Phase 2 ticket 3b: --offline path. Defer the push by appending a
	// pending-push record. The commit has already landed locally; the
	// next non-offline write will flush this entry (and any others
	// queued by previous offline writes) before its own push.
	if opts.Offline {
		if err := RecordPendingPush(gops, gops.RepoRoot, env.OpType); err != nil {
			return fmt.Errorf("cli: record pending-push: %w", err)
		}
		return nil
	}

	// Phase 2 ticket 3b: flush any pending pushes from prior --offline
	// writes BEFORE the current write's own push. The flush is a single
	// `git push` (covers all backlog commits reachable from HEAD), so
	// the cost is one round trip regardless of queue size. Flush
	// failures surface unchanged — we do NOT swallow them, because a
	// stale pending-pushes file would silently mask an ongoing remote
	// outage. The current commit stays local-only on flush failure;
	// the next attempt re-flushes from the persisted state.
	if err := FlushPendingPushes(gops, gops.RepoRoot); err != nil {
		return fmt.Errorf("cli: flush pending-pushes: %w", err)
	}

	// Phase 2 ticket 3a: synchronous publish. If origin is configured we
	// invoke PushWithRetry; ErrPushExhausted and ErrFetchFailed bubble up
	// for the caller to translate into envelopes `push_exhausted` /
	// `remote_unreachable` (exit 4 per spec §error-envelope). The local
	// op file is intentionally left on disk on push failure — the commit
	// succeeded, so the op is recoverable via the harvest path even if
	// the publish step didn't. The legacy `--push` flag is now redundant
	// when origin is set (auto-publish covers it); we keep the field on
	// WriteOpts for callers that haven't migrated, but the publish
	// happens regardless of opts.Push.
	if err := gops.AutoPushAfterCommit(); err != nil {
		return fmt.Errorf("cli: push: %w", err)
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
func WriteOpsAndAutoCommit(envs []op.Envelope, bodies [][]byte, paths config.LayoutPaths, gops *gitops.ActGitOps, opts WriteOpts, commitMessage string) error {
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
	if opts.Offline && opts.Push {
		return fmt.Errorf("%w: --offline and --push are mutually exclusive", ErrInvalidFlags)
	}
	if !opts.NoCommit && gops == nil {
		return fmt.Errorf("cli: gitops is required unless --no-commit is set")
	}
	if !opts.NoCommit && commitMessage == "" {
		return fmt.Errorf("cli: WriteOpsAndAutoCommit: empty commit message")
	}

	// Step 2: write all op files. Track each path so we can roll back on
	// failure. `staged` is tracked separately from `written` so the rollback
	// only unstages files that successfully passed StageOpFile (act-c22b);
	// running `git restore --staged` on a never-staged path exits non-zero
	// and would leak a stderr line to anyone wiring exec.Cmd.Stderr through
	// (today runUnstage discards stderr by default, but the asymmetry was a
	// latent bug — see the writeBlockOpsViaInterface pattern in
	// internal/mcp/composed.go).
	fsLock := func() (func(), error) { return func() {}, nil }
	written := make([]string, 0, len(envs))
	staged := make([]string, 0, len(envs))
	rollback := func() {
		if gops != nil {
			for _, p := range staged {
				_ = unstage(gops, p)
			}
		}
		for _, p := range written {
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

	// Step 3: stage every op file. Append to `staged` only after a
	// successful StageOpFile so a partial-stage failure rolls back exactly
	// the entries that were actually staged (no spurious unstage calls).
	for _, p := range written {
		if err := gops.StageOpFile(p); err != nil {
			rollback()
			return fmt.Errorf("cli: stage: %w", err)
		}
		staged = append(staged, p)
	}

	// Step 4: single commit. Phase 2 ticket 3b: timed via CommitOp so the
	// slow-write observation fires on batch commits too. The batch
	// represents a single logical op for slow-write attribution; we use
	// the first envelope's id / op_type as the record annotation.
	opIDForSlow, _ := envs[0].Hash()
	swCtx := gitops.SlowWriteContext{
		OpType:    envs[0].OpType,
		OpID:      opIDForSlow,
		StateRoot: gops.RepoRoot,
	}
	if err := gops.CommitOp(commitMessage, swCtx); err != nil {
		rollback()
		return fmt.Errorf("cli: commit: %w", err)
	}

	// Step 5a: --offline → defer the push by recording a pending-push
	// entry for the just-landed commit. Same semantics as the single-op
	// path; the batch is one local commit so one entry is sufficient.
	if opts.Offline {
		if err := RecordPendingPush(gops, gops.RepoRoot, envs[0].OpType); err != nil {
			return fmt.Errorf("cli: record pending-push: %w", err)
		}
		return nil
	}

	// Step 5b: flush any prior --offline backlog before this batch's
	// own push. See the single-op rationale in WriteOpAndAutoCommit.
	if err := FlushPendingPushes(gops, gops.RepoRoot); err != nil {
		return fmt.Errorf("cli: flush pending-pushes: %w", err)
	}

	// Step 5: Phase 2 ticket 3a synchronous publish. See WriteOpAndAutoCommit
	// above for the rationale; the rollback path is intentionally NOT
	// triggered on push failure because the commit landed locally and the
	// op is recoverable via harvest.
	if err := gops.AutoPushAfterCommit(); err != nil {
		return fmt.Errorf("cli: push: %w", err)
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

// InClaimWindowForNode reports whether the given issue currently has an active
// claim from the given nodeID, meaning a claim op from this node exists and no
// close op has been written since that claim.
//
// Detection algorithm: walk all op files for the issue. Find the latest close
// HLC (if any). Then check for any claim op from nodeID with HLC strictly
// greater than the latest close. If found, the issue is in an active claim
// window for this node.
//
// Returns (true, nil) when in a claim window, (false, nil) when not, and
// (false, err) on a read/parse error. Missing issue dir returns (false, nil).
func InClaimWindowForNode(opsDir, issueID, nodeID string) (bool, error) {
	pattern := filepath.Join(opsDir, issueID, "*", "*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return false, fmt.Errorf("cli: glob ops for %s: %w", issueID, err)
	}

	var latestCloseHLC hlc.HLC
	haveClose := false
	var claimHLC hlc.HLC
	haveClaim := false

	for _, path := range matches {
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return false, fmt.Errorf("cli: read op %s: %w", path, rerr)
		}
		env, uerr := op.Unmarshal(body)
		if uerr != nil {
			continue // skip unparseable files; fold does the same
		}
		switch env.OpType {
		case "close":
			if !haveClose || latestCloseHLC.Less(env.HLC) {
				latestCloseHLC = env.HLC
				haveClose = true
			}
		case "claim":
			var p op.ClaimPayload
			if jerr := json.Unmarshal(env.Payload, &p); jerr != nil {
				continue
			}
			if p.Assignee != nodeID {
				continue
			}
			if !haveClaim || claimHLC.Less(env.HLC) {
				claimHLC = env.HLC
				haveClaim = true
			}
		}
	}

	if !haveClaim {
		return false, nil
	}
	// A claim exists from our node; check that no close has happened
	// after it.
	if haveClose && !latestCloseHLC.Less(claimHLC) {
		// latestCloseHLC >= claimHLC: the claim is closed
		return false, nil
	}
	return true, nil
}

// ListPendingOpFilesForIssue returns all untracked op files for a specific
// issue under opsDir. Only files under .act/ops/<issueID>/ are returned.
//
// This is used by the per_session close path to collect the batch of
// deferred op files for this issue that should be bundled into the close commit.
func ListPendingOpFilesForIssue(repoRoot, opsDir, issueID string) ([]string, error) {
	return runListPendingOpFilesForIssue(repoRoot, opsDir, issueID)
}

// unstage runs `git restore --staged <opPath>` via the gitops runner. The
// operation is best-effort; failures are returned but typical callers
// ignore the result because the original commit error is the user-facing
// signal. Routes through runUnstage so the existing runUnstageFn test
// seam (writeops_rollback_test.go) continues to record rollback calls;
// runUnstageReal is responsible for the nested-repo --git-dir override
// (act-784b).
func unstage(g *gitops.ActGitOps, opPath string) error {
	return runUnstage(g.RepoRoot, opPath)
}
