package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// closeMarkerLookback is the number of most-recent host commits the
// post-close commit-marker correlation check scans. 50 covers a normal
// agent loop's recent work history without crawling the entire log; if
// the marker isn't in the last 50 it almost certainly isn't there at
// all, and emitting the warning earlier (rather than searching forever)
// keeps close's latency budget intact.
const closeMarkerLookback = 50

// CloseOptions captures the flag knobs for `act close`.
//
// Per spec §3 `act close <id>`: positional <id> and flags `--reason TEXT`,
// `--json`, plus the universal write flags.
type CloseOptions struct {
	// ID is the positional argument: the issue id (full or unique prefix)
	// to close. Resolved via the standard prefix pipeline.
	ID string
	// Reason is the optional `closed_reason`. Empty is allowed; payloads
	// with `len(Reason) > closeReasonMaxBytes` (500) are rejected with
	// exit 2 — see `act help workflow` and TestDocClaim_CloseReasonCap_*.
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
	// Branch, when non-empty, names the branch in the nested .act/ repo
	// that the close auto-commit lands on and the push targets. See
	// WriteOpts.Branch (util.go) and act-5d6a for the worktree-subagent
	// rationale.
	Branch string
	// NoCode marks this close as legitimately producing no code change
	// (tracking-only, wrong-claim retraction, doc correction, etc.).
	// Plumbed into ClosePayload.NoCode; doctor's reconcile-lite case (b)
	// suppresses warnings for closes with this flag set. See
	// docs/coordination-plane-design.md "Doctor reconciliation" (act-37f7).
	NoCode bool
	// NoDoctor skips the post-close single-issue commit-marker correlation
	// check. Default (false) runs the check after a successful close and
	// emits a stderr warning if no host commit in the last
	// closeMarkerLookback carries an `Act-Id: act-XXXXXX` trailer for the
	// closed issue. The check is informational — it never changes exit
	// code. See act-f2ea: catching bare-id-slicing errors and marker-
	// construction bugs locally is cheaper than discovering them at the
	// next doctor run.
	NoDoctor bool
	// Force overrides the external-dep gate (act-5e36). When true and the
	// issue has ≥1 open external dep, a WARNING is emitted to Stderr and
	// the close proceeds normally. Without --force, open external deps
	// cause exit 2 with envelope `blocked_by_external_dep`. This is a
	// rare escape hatch; the warning is intentionally verbose for audit
	// visibility.
	Force bool
	// Stderr is the destination for the post-close marker-correlation
	// warning and (when --force fires) the external-dep override warning.
	// Default (nil) is os.Stderr; tests inject a bytes.Buffer to capture
	// the emitted lines. The cmd-level dispatcher leaves it nil because
	// its own stderr is the right destination.
	Stderr io.Writer
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

	// Step 3b: external-dep gate (act-5e36). An issue with ≥1 open
	// external dep is blocked from being closed until the dep is cleared
	// via `act update --ext-rm`. Pass --force to override; a WARNING is
	// emitted to stderr when the override fires.
	gateRes, gerr := CheckExternalDepGate(idx, full)
	if gerr != nil {
		return CloseErrorOutput{
			Error:   "index_query_failed",
			Message: gerr.Error(),
		}, 1
	}
	if gateRes.Blocked {
		if !opts.Force {
			return BlockedByExtDepErrorOutput("act close", full, gateRes.ExternalDeps), 2
		}
		EmitExtDepForceWarning(opts.Stderr, full, gateRes.ExternalDeps)
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

		// act-5d6a: --branch <ref> switches the nested repo to the
		// named branch (creating it if missing) before staging so the
		// close lands on that ref. Empty branch is a no-op.
		if err := gops.EnsureBranch(opts.Branch); err != nil {
			return CloseErrorOutput{
				Error:   "branch_failed",
				Message: err.Error(),
			}, 1
		}

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
			// from the structured error; a fetch failure during the retry
			// loop is carried inside that error's last_error (act-6d9546 —
			// PushWithRetry never bubbles a bare ErrFetchFailed, so the close
			// path cannot emit `remote_unreachable`). Other push errors
			// surface as the legacy `push_failed` (exit 1). The commit has
			// already landed; on push failure we do NOT roll it back — the
			// close op is on disk locally and recoverable via the harvest
			// path.
			if err := gops.AutoPushAfterCommitToBranch(opts.Branch); err != nil {
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

	// Step 7 (act-f2ea): single-issue commit-marker correlation check.
	// After a successful close, scan the host repo's recent log for an
	// `Act-Id: act-XXXXXX` trailer matching this issue. The check is
	// informational — the close itself has already succeeded — but
	// catching marker-construction bugs (bare-id slicing, wrong-id
	// trailer) here is cheaper than at the next doctor run.
	//
	// Skipped when:
	//   - --no-doctor was passed (opt-out for trusted-fast paths).
	//   - --no-commit was passed (op file written but not committed; the
	//     agent's work commit hasn't been built yet either, so there is
	//     nothing to correlate against).
	if !opts.NoDoctor && committed {
		emitMarkerCorrelationWarning(opts.Stderr, paths.Root, full, short)
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

// emitMarkerCorrelationWarning runs the single-issue commit-marker
// correlation check against the host repo and writes a warning to stderr
// when no matching commit is found in the last closeMarkerLookback host
// commits. The check is best-effort — a git invocation failure (no HEAD,
// detached state, etc.) is silently ignored because the failure mode is
// no different from "no marker found yet" and noisy false positives would
// degrade the signal value of the warning itself.
//
// actStateRoot is the `.act/` directory (paths.Root); the host repo root
// is its parent. shortID is the canonical `act-XXXXXX` short id from
// ShortIssueID, used in the warning text; fullID is the full issue id,
// from which we strip the `act-` prefix to get the hex tail the gitops
// grep expects.
func emitMarkerCorrelationWarning(stderr io.Writer, actStateRoot, fullID, shortID string) {
	dst := stderr
	if dst == nil {
		dst = os.Stderr
	}
	hostRoot := filepath.Dir(actStateRoot)
	host := gitops.NewHostGitOps(hostRoot)
	// Strip the `act-` prefix; WorkCommitsForIssue keys on the hex tail.
	// shortID is `act-` + MinShortHexLen hex chars, so the trim is total.
	markerHex := strings.TrimPrefix(shortID, "act-")
	if len(markerHex) < 4 {
		// Defensive: a non-canonical id (shorter than the syntax floor)
		// would make gitops return an error. Skip the check; the close
		// itself already succeeded.
		return
	}
	commits, err := host.WorkCommitsForIssue(markerHex, closeMarkerLookback)
	if err != nil {
		// Best-effort: ignore git errors. The close itself succeeded.
		return
	}
	if len(commits) > 0 {
		return
	}
	fmt.Fprintf(dst,
		"act close: no host commit with '%s: %s' trailer found in last %d commits; consider amending or filing a follow-up\n",
		WorkCommitTrailerKey, shortID, closeMarkerLookback,
	)
	_ = fullID
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
// per spec §error-envelope). Everything else → legacy `push_failed`
// (exit 1).
//
// Note (act-6d9546): the close push path can NOT surface
// `remote_unreachable`. PushWithRetry stores a mid-loop ErrFetchFailed in
// lastErr and retries to exhaustion, so a genuine fetch failure during the
// retry loop surfaces as `push_exhausted` (with the fetch error carried in
// details.last_error), never as a bare ErrFetchFailed. `remote_unreachable`
// is reachable only from `act bootstrap-worker` (clone failure, exit 3 — see
// bootstrap_worker.go and the spec error table). There is therefore no
// ErrFetchFailed branch here.
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
	return CloseErrorOutput{
		Error:   "push_failed",
		Message: err.Error(),
	}, 1
}
