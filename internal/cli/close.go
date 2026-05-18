package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/hooks"
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/index"
	"github.com/aac/act/internal/op"
)

// CloseOptions captures the flag knobs for `act close`.
//
// Per spec §3 `act close <id>`: positional <id> and flags `--reason TEXT`,
// `--json`, plus the universal write flags.
type CloseOptions struct {
	// ID is the positional argument: the issue id (full or unique prefix)
	// to close. Resolved via the standard prefix pipeline.
	ID string
	// Reason is the optional `closed_reason`. Empty is allowed; payloads
	// with `len(Reason) > 4096` are rejected with exit 2.
	Reason string
	// AsJSON toggles JSON envelope rendering. The cli return shape is
	// identical regardless; main.go decides how to render.
	AsJSON bool
	// NoCommit, Push, Isolated mirror the universal write flags.
	NoCommit bool
	Push     bool
	Isolated bool
	// NoCode marks this close as legitimately producing no code change
	// (tracking-only, wrong-claim retraction, doc correction, etc.).
	// Plumbed into ClosePayload.NoCode; doctor's reconcile-lite case (b)
	// suppresses warnings for closes with this flag set. See
	// docs/coordination-plane-design.md "Doctor reconciliation" (act-37f7).
	NoCode bool
}

// CloseResult is the JSON-serialisable success envelope for a write that
// actually emitted a new close op.
//
// Committed and StagedForCommit are mutually exclusive:
//   - Committed=true, StagedForCommit=false: act close created its own commit
//     (clean working tree outside .act/, or per_op strategy).
//   - Committed=false, StagedForCommit=true: the close op file is staged but
//     not yet committed; the agent's next git commit will subsume it together
//     with their working-tree changes (act-a659).
//   - Committed=false, StagedForCommit=false: --no-commit was set; nothing is
//     staged.
type CloseResult struct {
	ID              string `json:"id"`
	ShortID         string `json:"short_id"`
	OpsWritten      int    `json:"ops_written"`
	Committed       bool   `json:"committed"`
	StagedForCommit bool   `json:"staged_for_commit,omitempty"`
	CommitMarker    string `json:"commit_marker,omitempty"`
	Reason          string `json:"reason"`
}

// CloseAlreadyClosed is the JSON-serialisable envelope returned when the
// target issue is already closed; per spec §"Edge cases" the operation is
// idempotent and emits no op.
type CloseAlreadyClosed struct {
	ID            string `json:"id"`
	AlreadyClosed bool   `json:"already_closed"`
}

// CloseErrorOutput is the structured failure envelope. Candidates is non-nil
// only on the id_ambiguous path; it is also mirrored under
// Details["candidates"] so the on-the-wire JSON envelope matches spec
// §"Errors" (`details.candidates[]`).
type CloseErrorOutput struct {
	Error      string         `json:"error"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	Candidates []string       `json:"-"`
}

// closeReasonMaxBytes mirrors spec §"Edge cases": reason >4KB exits 2.
const closeReasonMaxBytes = 4096

// RunClose implements `act close`.
//
// Steps:
//
//  1. Require a git working tree + initialised .act/. Missing → exit 3.
//
//  2. Resolve opts.ID via the prefix pipeline.
//
//  3. Fold the issue (via index rebuild). If status is already "closed",
//     return idempotent exit 0 with `{id, already_closed:true}` and write
//     no op.
//
//  4. Build a close envelope carrying ClosePayload{Reason: opts.Reason}.
//
//  5. Write the op file; run the post-close hook (pre-commit per the
//     hooks contract); stage the op file. Then decide whether to commit:
//
//     - `--no-commit`: skip staging+commit entirely (op file written only).
//     - `per_op` strategy: always commit standalone.
//     - `per_session` strategy (default): if the working tree has any
//     non-.act changes the agent's next `git commit -am '<msg>
//     (act-XXXX)'` will subsume the staged close op (act-a659); this
//     yields one work-commit-with-close instead of work + close. If the
//     working tree is clean outside .act/, commit standalone (preserves
//     the no-code-close UX).
//
//     The commit subject (when we commit) contains `(<short_id>)` so
//     doctor's orphan-close grep can correlate. When the agent subsumes
//     the staged op, their work-commit subject must carry the same
//     marker — the FormatCloseHuman hint reminds them.
//
//  6. Return CloseResult on success.
//
// Returns:
//   - output: CloseResult on a true close, CloseAlreadyClosed on the
//     idempotent path, CloseErrorOutput on failure.
//   - exitCode: 0 success or idempotent no-op; 1 hook reject / write
//     failure; 2 bad flags / ambiguous prefix / reason too long;
//     3 missing repo / missing .act/ / unknown id.
func RunClose(repoRoot string, opts CloseOptions) (output any, exitCode int) {
	// Step 1: repo + .act/ required.
	if !hasGitDir(repoRoot) {
		return CloseErrorOutput{
			Error:   "not_in_git",
			Message: fmt.Sprintf("act close: %s is not inside a git working tree", repoRoot),
		}, 3
	}
	paths := config.Layout(repoRoot)
	if _, err := os.Stat(paths.ConfigJSON); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return CloseErrorOutput{
				Error:   "act_not_initialized",
				Message: fmt.Sprintf("act close: %s/.act not initialized; run `act init` first", repoRoot),
			}, 3
		}
		return CloseErrorOutput{
			Error:   "act_not_initialized",
			Message: fmt.Sprintf("act close: stat %s: %v", paths.ConfigJSON, err),
		}, 3
	}

	// Step 2a: positional arg required.
	if opts.ID == "" {
		return CloseErrorOutput{
			Error:   "bad_flag",
			Message: "act close: <id> is required",
		}, 2
	}
	// Step 2b: reason length cap.
	if len(opts.Reason) > closeReasonMaxBytes {
		return CloseErrorOutput{
			Error:   "bad_flag",
			Message: fmt.Sprintf("act close: --reason length %d > %d bytes", len(opts.Reason), closeReasonMaxBytes),
		}, 2
	}
	// Step 2c: universal-write-flag conflicts (per spec §4).
	if opts.NoCommit && opts.Push {
		return CloseErrorOutput{
			Error:   "bad_flag",
			Message: "act close: --no-commit and --push are mutually exclusive",
		}, 2
	}
	if opts.Isolated && opts.Push {
		return CloseErrorOutput{
			Error:   "bad_flag",
			Message: "act close: --isolated and --push are mutually exclusive",
		}, 2
	}

	// Step 2d: enumerate known ids and resolve the target.
	knownIDs, err := listIssueIDs(paths.Ops)
	if err != nil {
		return CloseErrorOutput{
			Error:   "ops_scan_failed",
			Message: err.Error(),
		}, 1
	}
	full, rerr := ids.Resolve(opts.ID, knownIDs)
	if rerr != nil {
		if errors.Is(rerr, ids.ErrNotFound) {
			return CloseErrorOutput{
				Error:   "issue_not_found",
				Message: fmt.Sprintf("act close: %q: no matching id", opts.ID),
				Details: map[string]any{"query": opts.ID},
			}, 3
		}
		var amb *ids.ErrAmbiguousID
		if errors.As(rerr, &amb) {
			candidates := amb.Candidates()
			// Exit 2 (usage): see resolve_helpers.go for the spec rationale.
			return CloseErrorOutput{
				Error:   "id_ambiguous",
				Message: fmt.Sprintf("act close: prefix %q matches %d issues", opts.ID, len(candidates)),
				Details: map[string]any{
					"prefix":     opts.ID,
					"candidates": candidates,
				},
				Candidates: candidates,
			}, 2
		}
		return CloseErrorOutput{
			Error:   "issue_not_found",
			Message: rerr.Error(),
			Details: map[string]any{"query": opts.ID},
		}, 3
	}

	// Step 3: fold the issue and check current status. If already closed,
	// return idempotent no-op (exit 0, no op file written).
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		return CloseErrorOutput{
			Error:   "index_open_failed",
			Message: err.Error(),
		}, 1
	}
	defer func() { _ = idx.Close() }()
	if err := idx.Rebuild(paths.Ops); err != nil {
		return CloseErrorOutput{
			Error:   "index_rebuild_failed",
			Message: err.Error(),
		}, 1
	}
	row, err := idx.Get(full)
	if err != nil {
		return CloseErrorOutput{
			Error:   "issue_not_found",
			Message: fmt.Sprintf("act close: %s: %v", full, err),
		}, 3
	}
	if row.Status == "closed" {
		return CloseAlreadyClosed{
			ID:            full,
			AlreadyClosed: true,
		}, 0
	}

	// Step 4: build and write the close envelope.
	cfg, cerr := config.ReadConfig(paths)
	if cerr != nil {
		return CloseErrorOutput{
			Error:   "config_read_failed",
			Message: cerr.Error(),
		}, 1
	}

	payload := op.ClosePayload{Reason: opts.Reason, NoCode: opts.NoCode}
	if verr := payload.Validate(); verr != nil {
		return CloseErrorOutput{
			Error:   "payload_invalid",
			Message: verr.Error(),
		}, 1
	}
	bodyPayload, perr := canonicaljson.Marshal(payload)
	if perr != nil {
		return CloseErrorOutput{
			Error:   "marshal_failed",
			Message: perr.Error(),
		}, 1
	}

	clock := hlc.NewClock(cfg.NodeID, func() int64 { return time.Now().UnixMilli() })
	stamp := clock.Send()
	stamp.NodeID = cfg.NodeID

	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "close",
		IssueID:       full,
		Payload:       bodyPayload,
		HLC:           stamp,
		NodeID:        cfg.NodeID,
	}
	if verr := env.Validate(); verr != nil {
		return CloseErrorOutput{
			Error:   "envelope_invalid",
			Message: verr.Error(),
		}, 1
	}
	body, merr := env.Marshal()
	if merr != nil {
		return CloseErrorOutput{
			Error:   "marshal_failed",
			Message: merr.Error(),
		}, 1
	}

	// Compute the short id for the JSON result. Doctor's orphan-close
	// grep keys on the same `(act-XXXX)` marker that BuildOpCommitMessage
	// produces, so the JSON shape and the commit subject stay aligned.
	short := ShortIssueID(full)

	// Step 5: write op file + (optionally) commit. The close path stays
	// out of WriteOpAndAutoCommit because it threads a custom hook
	// invocation; the commit subject itself is the canonical
	// BuildOpCommitMessage form, identical to every other write op.
	opPath, _, werr := op.ProbeAndWrite(paths.Ops, env, body, func() (func(), error) { return func() {}, nil })
	if werr != nil {
		return CloseErrorOutput{
			Error:   "write_failed",
			Message: werr.Error(),
		}, 1
	}

	// Determine whether we should bundle pending op files (per_session strategy).
	// In per_session mode the close commit collects all deferred op files that
	// were written during the claim→close window for this specific issue (they
	// were written to disk but not committed, per InClaimWindowForNode semantics).
	var pendingPaths []string
	if cfg.EffectiveBundleStrategy() == config.BundleStrategyPerSession && !opts.NoCommit {
		pp, perr := ListPendingOpFilesForIssue(repoRoot, paths.Ops, full)
		if perr != nil {
			_ = os.Remove(opPath)
			return CloseErrorOutput{
				Error:   "pending_ops_scan_failed",
				Message: perr.Error(),
			}, 1
		}
		// pendingPaths may include the close op file we just wrote (it is
		// untracked). Filter it out so we stage it explicitly below and
		// avoid double-staging.
		for _, p := range pp {
			if p != opPath {
				pendingPaths = append(pendingPaths, p)
			}
		}
	}

	committed := false
	stagedForCommit := false
	if !opts.NoCommit {
		// Phase 1: writes target the nested .act/ git repo. Under this
		// reframe, the close commit is in the nested repo and invisible
		// to the host repo's log, so the bundle-into-host-work-commit
		// machinery (act-a659) no longer applies — close always commits
		// standalone in the nested repo. The HasNonActChanges branch
		// below stays dead code under Phase 1 because nested-repo cwd
		// has no non-.act paths by construction.
		gops := gitops.NewActGitOps(paths.Root)

		// Stage any deferred op files first (they have no associated hook).
		rollbackPending := func() {
			for _, p := range pendingPaths {
				_ = runUnstage(gops.RepoRoot, p)
			}
		}
		for _, p := range pendingPaths {
			if err := gops.StageOpFile(p); err != nil {
				rollbackPending()
				_ = os.Remove(opPath)
				return CloseErrorOutput{
					Error:   "stage_failed",
					Message: fmt.Sprintf("stage pending op %s: %v", p, err),
				}, 1
			}
		}

		// Stage the close op file itself.
		if err := gops.StageOpFile(opPath); err != nil {
			rollbackPending()
			return CloseErrorOutput{
				Error:   "stage_failed",
				Message: err.Error(),
			}, 1
		}

		// Pre-commit hook: post-close per spec §Hooks contract.
		// Runs even when we end up not committing (the close-op is staged
		// either way) so .act/hooks/close still gates the close decision.
		if hookPath, ok := hooks.ResolveHook(paths.Hooks, env.OpType); ok {
			opID, herr := env.Hash()
			if herr != nil {
				rollbackPending()
				_ = runUnstage(gops.RepoRoot, opPath)
				return CloseErrorOutput{
					Error:   "hash_failed",
					Message: herr.Error(),
				}, 1
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
				rollbackPending()
				_ = runUnstage(gops.RepoRoot, opPath)
				_ = os.Remove(opPath)
				msg, details, _ := HookFailureDetails(err)
				return CloseErrorOutput{
					Error:   "hook_failed",
					Message: msg,
					Details: details,
				}, 1
			}
		}

		// Commit decision under Phase 1 (docs/coordination-plane-design.md
		// "Consequence for act-a659"): the close commit lives in the
		// nested .act/ repo, which is invisible to the host repo's log,
		// so the bundle-into-host-work-commit machinery that justified
		// per_session's deferred-close path no longer applies. Close
		// always commits standalone in the nested repo regardless of
		// strategy; the per_session config knob is dead under Phase 1
		// and slated for removal in a follow-up.
		commitNow := true

		if commitNow {
			// Commit subject is built by BuildOpCommitMessage; canonical
			// format is `act-op: (act-XXXX) close`. Doctor's orphan-close
			// grep keys on the parenthesized short id. See act-d3a5.
			// When bundling, include the count of bundled ops in the message
			// (including the close op itself).
			var msg string
			if len(pendingPaths) > 0 {
				msg = BuildBatchCommitMessage(env, len(pendingPaths)+1)
			} else {
				msg = BuildOpCommitMessage(env)
			}
			if err := gops.Commit(msg); err != nil {
				rollbackPending()
				_ = runUnstage(gops.RepoRoot, opPath)
				return CloseErrorOutput{
					Error:   "commit_failed",
					Message: err.Error(),
				}, 1
			}
			committed = true

			if opts.Push {
				if err := gops.Push(); err != nil {
					return CloseErrorOutput{
						Error:   "push_failed",
						Message: err.Error(),
					}, 1
				}
			}
		} else {
			// Close op is staged; the agent's next `git commit -am` will
			// pick it up alongside their code changes (one work commit
			// instead of work + close). Push is meaningless here — there's
			// nothing new on HEAD yet — so we surface a flag-conflict
			// error to keep the contract crisp. Full rollback (unstage +
			// remove the op file) so the issue is NOT folded as closed
			// from on-disk ops; otherwise re-running `act close` would
			// short-circuit to {already_closed:true} and the agent would
			// have no way to recover.
			if opts.Push {
				rollbackPending()
				_ = runUnstage(gops.RepoRoot, opPath)
				_ = os.Remove(opPath)
				return CloseErrorOutput{
					Error:   "push_without_commit",
					Message: "act close: --push requires the close to commit standalone, but the working tree has uncommitted non-.act changes. Commit your work first (git commit -am '<msg> (act-XXXX)'), then re-run `act close <id> --push` (or omit --push and push manually after the commit subsumes the close op).",
				}, 2
			}
			stagedForCommit = true
		}
	}

	// Refresh the live SQLite index so it reflects the post-close state
	// without requiring a doctor --fix rebuild. The op log on disk is the
	// source of truth; a refresh failure here is non-fatal but is surfaced
	// as a write failure since the contract is that index-divergence is
	// zero after a successful close.
	if err := RefreshIndexForIssue(paths, full); err != nil {
		return CloseErrorOutput{
			Error:   "index_update_failed",
			Message: err.Error(),
		}, 1
	}

	return CloseResult{
		ID:              full,
		ShortID:         short,
		OpsWritten:      1,
		Committed:       committed,
		StagedForCommit: stagedForCommit,
		CommitMarker:    WorkCommitMarker(full),
		Reason:          opts.Reason,
	}, 0
}

// FormatCloseHuman renders a CloseResult as a one-or-two-line human message.
//
// Three cases (mutually exclusive on Committed/StagedForCommit/neither):
//   - Committed: "Closed act-XXXX[: reason]" (legacy single-commit close).
//   - StagedForCommit: "Closed act-XXXX[: reason]" plus a hint line telling
//     the agent to subsume the staged close op into the work commit, with
//     the `Act-Id: act-XXXXXX` trailer in the commit body. Two `-m` flags
//     produce the subject and trailer paragraphs. This is the act-a659
//     work-commit-with-close path; act-c4c5 switched the marker form from
//     subject-line `(act-XXXX)` to body-trailer `Act-Id: act-XXXXXX`.
//   - Neither (--no-commit): "Closed act-XXXX[: reason] (op file written, not
//     staged)" so the user knows downstream staging is on them.
func FormatCloseHuman(res CloseResult) string {
	head := fmt.Sprintf("Closed %s", res.ShortID)
	if res.Reason != "" {
		head = fmt.Sprintf("Closed %s: %s", res.ShortID, res.Reason)
	}
	switch {
	case res.StagedForCommit:
		return fmt.Sprintf("%s\n  Close op staged. Include in your next commit:\n  git commit -a -m '<subject>' -m '%s'\n", head, res.CommitMarker)
	case !res.Committed:
		return fmt.Sprintf("%s (op file written, not staged)\n", head)
	default:
		return head + "\n"
	}
}

// FormatCloseAlreadyClosedHuman renders a CloseAlreadyClosed envelope.
func FormatCloseAlreadyClosedHuman(res CloseAlreadyClosed) string {
	return fmt.Sprintf("Already closed: %s\n", res.ID)
}
