package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
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
}

// CloseResult is the JSON-serialisable success envelope for a write that
// actually emitted a new close op.
type CloseResult struct {
	ID         string `json:"id"`
	ShortID    string `json:"short_id"`
	OpsWritten int    `json:"ops_written"`
	Committed  bool   `json:"committed"`
	Reason     string `json:"reason"`
}

// CloseAlreadyClosed is the JSON-serialisable envelope returned when the
// target issue is already closed; per spec §"Edge cases" the operation is
// idempotent and emits no op.
type CloseAlreadyClosed struct {
	ID            string `json:"id"`
	AlreadyClosed bool   `json:"already_closed"`
}

// CloseErrorOutput is the structured failure envelope.
type CloseErrorOutput struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// closeReasonMaxBytes mirrors spec §"Edge cases": reason >4KB exits 2.
const closeReasonMaxBytes = 4096

// RunClose implements `act close`.
//
// Steps:
//  1. Require a git working tree + initialised .act/. Missing → exit 3.
//  2. Resolve opts.ID via the prefix pipeline.
//  3. Fold the issue (via index rebuild). If status is already "closed",
//     return idempotent exit 0 with `{id, already_closed:true}` and write
//     no op.
//  4. Build a close envelope carrying ClosePayload{Reason: opts.Reason}.
//  5. Write the op file; run the post-close hook (pre-commit per the
//     hooks contract); auto-commit unless --no-commit. The commit
//     subject contains `(<short_id>)` so doctor's orphan-close grep
//     can correlate.
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
			}, 3
		}
		var amb *ids.ErrAmbiguousID
		if errors.As(rerr, &amb) {
			return CloseErrorOutput{
				Error:   "ambiguous_id",
				Message: rerr.Error(),
			}, 2
		}
		return CloseErrorOutput{
			Error:   "issue_not_found",
			Message: rerr.Error(),
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

	payload := op.ClosePayload{Reason: opts.Reason}
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

	// Compute the short id for the commit subject. We use the on-disk
	// prefix-of-id length (4 hex chars after `act-`) so doctor's
	// orphan-close grep `(act-XXXX)` always matches.
	short := full
	if len(full) > len("act-")+ids.MinShortHexLen {
		short = full[:len("act-")+ids.MinShortHexLen]
	}

	// Step 5: write op file + (optionally) commit. We do NOT route through
	// WriteOpAndAutoCommit because the close commit subject must embed
	// `(<short_id>)` for doctor's orphan-close grep — a format the shared
	// helper does not produce.
	opPath, _, werr := op.ProbeAndWrite(paths.Ops, env, body, func() (func(), error) { return func() {}, nil })
	if werr != nil {
		return CloseErrorOutput{
			Error:   "write_failed",
			Message: werr.Error(),
		}, 1
	}

	committed := false
	if !opts.NoCommit {
		gops := gitops.NewGitOps(repoRoot)
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
			}
			if err := hooks.Run(hctx, hookPath, hookTimeout); err != nil {
				_ = runUnstage(gops.RepoRoot, opPath)
				_ = os.Remove(opPath)
				return CloseErrorOutput{
					Error:   "hook_failed",
					Message: err.Error(),
				}, 1
			}
		}

		// Commit subject embeds `(<short_id>)` so doctor's
		// orphan-close grep can correlate the closed issue with a
		// commit referencing its short id.
		msg := fmt.Sprintf("act-op: (%s) close", short)
		if err := gops.Commit(msg); err != nil {
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
		ID:         full,
		ShortID:    short,
		OpsWritten: 1,
		Committed:  committed,
		Reason:     opts.Reason,
	}, 0
}

// FormatCloseHuman renders a CloseResult as a single human-friendly line.
func FormatCloseHuman(res CloseResult) string {
	if res.Reason == "" {
		return fmt.Sprintf("Closed %s\n", res.ShortID)
	}
	return fmt.Sprintf("Closed %s: %s\n", res.ShortID, res.Reason)
}

// FormatCloseAlreadyClosedHuman renders a CloseAlreadyClosed envelope.
func FormatCloseAlreadyClosedHuman(res CloseAlreadyClosed) string {
	return fmt.Sprintf("Already closed: %s\n", res.ID)
}
