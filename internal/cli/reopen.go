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
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/index"
	"github.com/aac/act/internal/op"
)

// ReopenOptions captures the flag knobs for `act reopen`.
//
// Per spec §5.B.4: positional <id> plus optional --reason TEXT.
// Universal write flags apply.
type ReopenOptions struct {
	// ID is the positional argument: the issue id (full or unique prefix).
	ID string
	// Reason is the optional explanatory text recorded in the reopen op
	// payload. Bounded to 500 chars by ReopenPayload.Validate().
	Reason string
	// AsJSON toggles JSON envelope rendering at the call site.
	AsJSON bool
	// NoCommit, Push, Isolated mirror the universal write flags.
	NoCommit bool
	Push     bool
	Isolated bool
	// Verify, when true, runs git commit hooks rather than --no-verify.
	Verify bool
}

// ReopenResult is the JSON-serialisable success envelope for a write
// that emitted a new reopen op.
type ReopenResult struct {
	ID         string `json:"id"`
	ShortID    string `json:"short_id"`
	OpsWritten int    `json:"ops_written"`
	Committed  bool   `json:"committed"`
	Reason     string `json:"reason,omitempty"`
}

// ReopenAlreadyOpen is the JSON-serialisable envelope returned when the
// target issue is not currently closed; per the gap-issue spec the
// operation is idempotent and emits no op.
type ReopenAlreadyOpen struct {
	ID          string `json:"id"`
	AlreadyOpen bool   `json:"already_open"`
}

// ReopenErrorOutput is the structured failure envelope.
type ReopenErrorOutput struct {
	Error      string         `json:"error"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	Candidates []string       `json:"-"`
}

// reopenReasonMaxBytes mirrors close: bound to 500 to match
// op.ReopenPayload.Validate().
const reopenReasonMaxBytes = 500

// RunReopen implements `act reopen <id>`.
//
// Steps:
//  1. Require a git working tree + initialised .act/.
//  2. Resolve opts.ID via the prefix pipeline.
//  3. Fold the issue. If status != "closed", return idempotent
//     ReopenAlreadyOpen exit 0 with no op written.
//  4. Build a reopen envelope carrying ReopenPayload{Reason}.
//  5. Write the op file via WriteOpAndAutoCommit; refresh the live index.
//  6. Return ReopenResult.
func RunReopen(repoRoot string, opts ReopenOptions) (output any, exitCode int) {
	// Step 1: repo + .act/ required.
	if !hasGitDir(repoRoot) {
		return ReopenErrorOutput{
			Error:   "not_in_git",
			Message: fmt.Sprintf("act reopen: %s is not inside a git working tree", repoRoot),
		}, 3
	}
	paths := config.Layout(repoRoot)
	if _, err := os.Stat(paths.ConfigJSON); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ReopenErrorOutput{
				Error:   "act_not_initialized",
				Message: fmt.Sprintf("act reopen: %s/.act not initialized; run `act init` first", repoRoot),
			}, 3
		}
		return ReopenErrorOutput{
			Error:   "act_not_initialized",
			Message: fmt.Sprintf("act reopen: stat %s: %v", paths.ConfigJSON, err),
		}, 3
	}

	// Step 2a: positional arg required.
	if opts.ID == "" {
		return ReopenErrorOutput{
			Error:   "bad_flag",
			Message: "act reopen: <id> is required",
		}, 2
	}
	// Step 2b: reason length cap.
	if len(opts.Reason) > reopenReasonMaxBytes {
		return ReopenErrorOutput{
			Error:   "bad_flag",
			Message: fmt.Sprintf("act reopen: --reason length %d > %d bytes", len(opts.Reason), reopenReasonMaxBytes),
		}, 2
	}
	// Step 2c: universal-write-flag conflicts.
	if opts.NoCommit && opts.Push {
		return ReopenErrorOutput{
			Error:   "bad_flag",
			Message: "act reopen: --no-commit and --push are mutually exclusive",
		}, 2
	}
	if opts.Isolated && opts.Push {
		return ReopenErrorOutput{
			Error:   "bad_flag",
			Message: "act reopen: --isolated and --push are mutually exclusive",
		}, 2
	}

	// Step 2d: enumerate known ids and resolve the target.
	knownIDs, err := listIssueIDs(paths.Ops)
	if err != nil {
		return ReopenErrorOutput{
			Error:   "ops_scan_failed",
			Message: err.Error(),
		}, 1
	}
	full, rerr := ids.Resolve(opts.ID, knownIDs)
	if rerr != nil {
		if errors.Is(rerr, ids.ErrNotFound) {
			return ReopenErrorOutput{
				Error:   "issue_not_found",
				Message: fmt.Sprintf("act reopen: %q: no matching id", opts.ID),
				Details: map[string]any{"query": opts.ID},
			}, 3
		}
		var amb *ids.ErrAmbiguousID
		if errors.As(rerr, &amb) {
			candidates := amb.Candidates()
			// Exit 2 (usage): see resolve_helpers.go for the spec rationale.
			return ReopenErrorOutput{
				Error:   "id_ambiguous",
				Message: fmt.Sprintf("act reopen: prefix %q matches %d issues", opts.ID, len(candidates)),
				Details: map[string]any{
					"prefix":     opts.ID,
					"candidates": candidates,
				},
				Candidates: candidates,
			}, 2
		}
		return ReopenErrorOutput{
			Error:   "issue_not_found",
			Message: rerr.Error(),
			Details: map[string]any{"query": opts.ID},
		}, 3
	}

	// Step 3: fold the issue and check status. If not closed, return
	// idempotent no-op (exit 0).
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		return ReopenErrorOutput{
			Error:   "index_open_failed",
			Message: err.Error(),
		}, 1
	}
	if err := idx.Rebuild(paths.Ops); err != nil {
		_ = idx.Close()
		return ReopenErrorOutput{
			Error:   "index_rebuild_failed",
			Message: err.Error(),
		}, 1
	}
	row, gerr := idx.Get(full)
	_ = idx.Close()
	if gerr != nil {
		return ReopenErrorOutput{
			Error:   "issue_not_found",
			Message: fmt.Sprintf("act reopen: %s: %v", full, gerr),
		}, 3
	}
	if row.Status != "closed" {
		return ReopenAlreadyOpen{
			ID:          full,
			AlreadyOpen: true,
		}, 0
	}

	// Step 4: build and write the reopen envelope.
	cfg, cerr := config.ReadConfig(paths)
	if cerr != nil {
		return ReopenErrorOutput{
			Error:   "config_read_failed",
			Message: cerr.Error(),
		}, 1
	}
	payload := op.ReopenPayload{Reason: opts.Reason}
	if verr := payload.Validate(); verr != nil {
		return ReopenErrorOutput{
			Error:   "payload_invalid",
			Message: verr.Error(),
		}, 1
	}
	bodyPayload, perr := canonicaljson.Marshal(payload)
	if perr != nil {
		return ReopenErrorOutput{
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
		OpType:        "reopen",
		IssueID:       full,
		Payload:       bodyPayload,
		HLC:           stamp,
		NodeID:        cfg.NodeID,
	}
	if verr := env.Validate(); verr != nil {
		return ReopenErrorOutput{
			Error:   "envelope_invalid",
			Message: verr.Error(),
		}, 1
	}
	body, merr := env.Marshal()
	if merr != nil {
		return ReopenErrorOutput{
			Error:   "marshal_failed",
			Message: merr.Error(),
		}, 1
	}

	// Step 5: write + auto-commit.
	var gops *gitops.GitOps
	if !opts.NoCommit {
		gops = gitops.NewGitOps(repoRoot)
		gops.Verify = opts.Verify
	}
	werr := WriteOpAndAutoCommit(env, body, paths, gops, WriteOpts{
		NoCommit: opts.NoCommit,
		Push:     opts.Push,
		Isolated: opts.Isolated,
	})
	if werr != nil {
		if errors.Is(werr, ErrInvalidFlags) {
			return ReopenErrorOutput{
				Error:   "bad_flag",
				Message: werr.Error(),
			}, 2
		}
		return ReopenErrorOutput{
			Error:   "write_failed",
			Message: werr.Error(),
		}, 1
	}

	// Step 6: refresh the live index so doctor's index-divergence check
	// passes immediately after a successful reopen.
	if err := RefreshIndexForIssue(paths, full); err != nil {
		return ReopenErrorOutput{
			Error:   "index_update_failed",
			Message: err.Error(),
		}, 1
	}

	short := full
	if len(full) > len("act-")+ids.MinShortHexLen {
		short = full[:len("act-")+ids.MinShortHexLen]
	}
	return ReopenResult{
		ID:         full,
		ShortID:    short,
		OpsWritten: 1,
		Committed:  !opts.NoCommit,
		Reason:     opts.Reason,
	}, 0
}

// FormatReopenHuman renders a ReopenResult as a single human-friendly line.
func FormatReopenHuman(res ReopenResult) string {
	if res.Reason == "" {
		return fmt.Sprintf("Reopened %s\n", res.ShortID)
	}
	return fmt.Sprintf("Reopened %s: %s\n", res.ShortID, res.Reason)
}

// FormatReopenAlreadyOpenHuman renders a ReopenAlreadyOpen envelope.
func FormatReopenAlreadyOpenHuman(res ReopenAlreadyOpen) string {
	return fmt.Sprintf("Already open: %s\n", res.ID)
}
