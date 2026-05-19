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
	// NoCommit, Push, Isolated, Offline mirror the universal write flags.
	NoCommit bool
	Push     bool
	Isolated bool
	// Offline (Phase 2 ticket 3b): commit locally, skip the push, append
	// a pending-push record. The next non-offline close (or any other
	// non-offline write) flushes the deferred push before its own.
	Offline bool
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
// Under Phase 1, every close that commits does so standalone in the nested
// .act/ git repo. Committed=true on a successful commit; Committed=false
// only when --no-commit was set (op file written but not staged).
type CloseResult struct {
	ID           string `json:"id"`
	ShortID      string `json:"short_id"`
	OpsWritten   int    `json:"ops_written"`
	Committed    bool   `json:"committed"`
	CommitMarker string `json:"commit_marker,omitempty"`
	Reason       string `json:"reason"`
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

// closeReasonMaxBytes mirrors the documented cap (act help close:
// "--reason is capped at 500 bytes") and the write-time enforcement in
// internal/op.ClosePayload.Validate. The CLI layer here is
// defense-in-depth for direct library callers; the cmd/act layer
// validates upfront at flag-parse time so the operator learns the cap
// before any op file is written.
const closeReasonMaxBytes = 500

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
//     hooks contract); stage the op file. Then commit standalone in the
//     nested .act/ git repo (or skip the commit entirely when --no-commit
//     was set).
//
//     The commit subject contains `(<short_id>)` so doctor's orphan-close
//     grep can correlate.
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
	if opts.Offline && opts.Push {
		return CloseErrorOutput{
			Error:   "bad_flag",
			Message: "act close: --offline and --push are mutually exclusive",
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

	committed := false
	if !opts.NoCommit {
		// Phase 1: writes target the nested .act/ git repo. Close
		// always commits standalone in the nested repo.
		gops := gitops.NewActGitOps(paths.Root)

		// Stage the close op file.
		if err := gops.StageOpFile(opPath); err != nil {
			return CloseErrorOutput{
				Error:   "stage_failed",
				Message: err.Error(),
			}, 1
		}

		// Pre-commit hook: post-close per spec §Hooks contract.
		if hookPath, ok := hooks.ResolveHook(paths.Hooks, env.OpType); ok {
			opID, herr := env.Hash()
			if herr != nil {
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

		// Commit standalone in the nested .act/ repo. The bundle-into-
		// host-work-commit machinery (formerly per_session, act-a659) no
		// longer applies under Phase 1 because the close commit lives in
		// the nested repo and is invisible to the host repo's log.
		msg := BuildOpCommitMessage(env)
		// Phase 2 ticket 3b: timed via CommitOp so slow-write
		// observation fires on close commits too. op_id is the
		// close envelope's hash; op_type is "close".
		opIDForSlow, _ := env.Hash()
		swCtx := gitops.SlowWriteContext{
			OpType:    env.OpType,
			OpID:      opIDForSlow,
			StateRoot: gops.RepoRoot,
		}
		if err := gops.CommitOp(msg, swCtx); err != nil {
			_ = runUnstage(gops.RepoRoot, opPath)
			return CloseErrorOutput{
				Error:   "commit_failed",
				Message: err.Error(),
			}, 1
		}
		committed = true

		// Phase 2 ticket 3b: --offline → defer the publish via a
		// pending-push record. The next non-offline write flushes
		// the entry before its own push.
		if opts.Offline {
			if err := RecordPendingPush(gops, gops.RepoRoot, env.OpType); err != nil {
				return CloseErrorOutput{
					Error:   "record_pending_push_failed",
					Message: err.Error(),
				}, 1
			}
		} else {
			// Phase 2 ticket 3b: flush any prior --offline backlog
			// before this close's own push.
			if err := FlushPendingPushes(gops, gops.RepoRoot); err != nil {
				return closeErrorForPushFailure(err)
			}
			// Phase 2 ticket 3a: synchronous publish on every close that
			// commits. If origin is configured, AutoPushAfterCommit invokes
			// PushWithRetry. On *PushExhaustedError we surface envelope
			// `push_exhausted` (exit 4 per spec §error-envelope) with
			// details.retry_count / shallow_unshallow_attempted populated
			// from the structured error. On ErrFetchFailed (and only that
			// sentinel) we surface envelope `remote_unreachable` (also exit
			// 4). Other push errors surface as the legacy `push_failed`
			// (exit 1) so the test suite for pre-3a behavior keeps passing.
			// The commit has already landed; on push failure we do NOT roll
			// it back — the close op is on disk locally and recoverable
			// via the harvest path.
			if err := gops.AutoPushAfterCommit(); err != nil {
				return closeErrorForPushFailure(err)
			}
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
		ID:           full,
		ShortID:      short,
		OpsWritten:   1,
		Committed:    committed,
		CommitMarker: WorkCommitMarker(full),
		Reason:       opts.Reason,
	}, 0
}

// FormatCloseHuman renders a CloseResult as a one-or-two-line human message.
//
// Two cases:
//   - Committed: "Closed act-XXXX[: reason]" (standalone close commit in
//     the nested .act/ repo).
//   - !Committed (--no-commit): "Closed act-XXXX[: reason] (op file written,
//     not staged)" so the caller knows downstream staging is on them.
func FormatCloseHuman(res CloseResult) string {
	head := fmt.Sprintf("Closed %s", res.ShortID)
	if res.Reason != "" {
		head = fmt.Sprintf("Closed %s: %s", res.ShortID, res.Reason)
	}
	if !res.Committed {
		return fmt.Sprintf("%s (op file written, not staged)\n", head)
	}
	return head + "\n"
}

// FormatCloseAlreadyClosedHuman renders a CloseAlreadyClosed envelope.
func FormatCloseAlreadyClosedHuman(res CloseAlreadyClosed) string {
	return fmt.Sprintf("Already closed: %s\n", res.ID)
}

// closeErrorForPushFailure classifies a push failure returned by
// AutoPushAfterCommit and produces the right CloseErrorOutput shape +
// exit code. *PushExhaustedError → envelope `push_exhausted` with
// details.retry_count and details.shallow_unshallow_attempted (exit 4
// per spec §error-envelope). gitops.ErrFetchFailed → `remote_unreachable`
// (also exit 4). Everything else → legacy `push_failed` (exit 1) so the
// pre-Phase-2 behavior of "any other push class is a generic failure"
// stays compatible.
func closeErrorForPushFailure(err error) (any, int) {
	var pe *gitops.PushExhaustedError
	if errors.As(err, &pe) {
		details := map[string]any{
			"retry_count":                 pe.RetryCount,
			"shallow_unshallow_attempted": pe.ShallowUnshallowAttempted,
		}
		if pe.LastError != nil {
			details["last_error"] = pe.LastError.Error()
		}
		return CloseErrorOutput{
			Error:   ErrPushExhausted,
			Message: fmt.Sprintf("push retries exhausted after %d attempts; last error: %v", pe.RetryCount, pe.LastError),
			Details: details,
		}, 4
	}
	if errors.Is(err, gitops.ErrFetchFailed) {
		return CloseErrorOutput{
			Error:   ErrRemoteUnreachable,
			Message: fmt.Sprintf("git fetch failed: %v", err),
			Details: map[string]any{
				"stderr_tail": CaptureStderrTail(err.Error()),
			},
		}, 4
	}
	return CloseErrorOutput{
		Error:   "push_failed",
		Message: err.Error(),
	}, 1
}
