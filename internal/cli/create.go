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

// CreateOptions captures the flags accepted by `act create`. Zero-valued
// fields imply "not supplied"; RunCreate fills in spec defaults (type=task,
// priority=1) when the corresponding field is empty/zero.
type CreateOptions struct {
	// Title is the positional argument; required, non-empty, ≤256 bytes.
	Title string
	// Priority is the spec priority enum (0..3). nil means "not supplied"
	// and is normalised to the spec default (1) before payload construction.
	// A non-nil pointer to 0 (i.e. `-p 0`) is preserved as priority=0.
	Priority *int
	// Type is the issue-type enum (task|bug|epic|chore). Empty defaults to
	// "task".
	Type string
	// Parent is an optional id (full or prefix). Resolved via the
	// id-resolution pipeline before the payload is built.
	Parent string
	// Description is an optional free-text body.
	Description string
	// Accept is the (in-order) list of acceptance criteria.
	Accept []string
	// AsJSON toggles JSON envelope output. The closed-parent warning is
	// suppressed from stderr when AsJSON is true (per §5.C.4).
	AsJSON bool
	// NoCommit, Push, Isolated mirror the universal write flags.
	NoCommit bool
	Push     bool
	Isolated bool
}

// CreateResult is the JSON shape returned on success. Field order is
// irrelevant for the JSON encoder; the spec example renders id, prefix,
// title, warnings (when present).
type CreateResult struct {
	ID       string   `json:"id"`
	ShortID  string   `json:"short_id"`
	Title    string   `json:"title"`
	Warnings []string `json:"warnings,omitempty"`
}

// CreateErrorOutput is the structured shape returned on failure. Candidates
// is non-nil only on the id_ambiguous path (resolving --parent); it is also
// mirrored under Details["candidates"] so the on-the-wire JSON envelope
// matches spec §"Errors" (`details.candidates[]`).
type CreateErrorOutput struct {
	Error      string         `json:"error"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	Candidates []string       `json:"-"`
}

// validCreateTypes mirrors op.validIssueTypes; replicated locally to avoid
// exporting the latter just for flag-validation.
var validCreateTypes = map[string]bool{
	"task":  true,
	"bug":   true,
	"epic":  true,
	"chore": true,
}

// RunCreate implements `act create`. It validates flags, resolves parent
// (if supplied), derives an issue id via PickUnique, builds and writes the
// create-op envelope, runs the post-create hook (via WriteOpAndAutoCommit),
// and op-commits unless --no-commit.
//
// Returns:
//   - output: CreateResult on success, CreateErrorOutput on failure.
//   - exitCode: 0 success; 2 bad flags / empty title / invalid type /
//     bad write-flag combo; 3 missing repo / missing .act/ / parent not
//     found; 1 hash collisions exhausted or commit/push failure.
func RunCreate(repoRoot string, opts CreateOptions) (output any, exitCode int) {
	// Step 1: require a git working tree + initialized .act/.
	if !hasGitDir(repoRoot) {
		return CreateErrorOutput{
			Error:   "not_in_git",
			Message: fmt.Sprintf("act create: %s is not inside a git working tree", repoRoot),
		}, 3
	}
	paths := config.Layout(repoRoot)
	if _, err := os.Stat(paths.ConfigJSON); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return CreateErrorOutput{
				Error:   "act_not_initialized",
				Message: fmt.Sprintf("act create: %s/.act not initialized; run `act init` first", repoRoot),
			}, 3
		}
		return CreateErrorOutput{
			Error:   "act_not_initialized",
			Message: fmt.Sprintf("act create: stat %s: %v", paths.ConfigJSON, err),
		}, 3
	}

	// Step 2: validate flags.
	if opts.Title == "" {
		return CreateErrorOutput{
			Error:   "bad_flag",
			Message: "act create: <title> is required and must be non-empty",
		}, 2
	}
	if len(opts.Title) > 256 {
		return CreateErrorOutput{
			Error:   "bad_flag",
			Message: fmt.Sprintf("act create: title length %d > 256 bytes", len(opts.Title)),
		}, 2
	}
	typ := opts.Type
	if typ == "" {
		typ = "task"
	}
	if !validCreateTypes[typ] {
		return CreateErrorOutput{
			Error:   "bad_flag",
			Message: fmt.Sprintf("act create: --type %q: must be one of task|bug|epic|chore", typ),
		}, 2
	}
	// Priority defaults to 1 when the caller did not pass --priority. An
	// explicit -p 0 (Priority pointing at 0) is preserved verbatim so the
	// payload records priority=0 and not the default.
	priority := 1
	if opts.Priority != nil {
		priority = *opts.Priority
	}
	if priority < 0 || priority > 3 {
		return CreateErrorOutput{
			Error:   "bad_flag",
			Message: fmt.Sprintf("act create: --priority %d out of range [0,3]", priority),
		}, 2
	}
	// Universal write-flag conflict combinations exit 2 per spec §4.
	if opts.NoCommit && opts.Push {
		return CreateErrorOutput{
			Error:   "bad_flag",
			Message: "act create: --no-commit and --push are mutually exclusive",
		}, 2
	}
	if opts.Isolated && opts.Push {
		return CreateErrorOutput{
			Error:   "bad_flag",
			Message: "act create: --isolated and --push are mutually exclusive",
		}, 2
	}

	// Step 3: enumerate known full ids via .act/ops/.
	knownIDs, err := listIssueIDs(paths.Ops)
	if err != nil {
		return CreateErrorOutput{
			Error:   "ops_scan_failed",
			Message: err.Error(),
		}, 1
	}

	// Step 4: resolve --parent if supplied. A closed parent surfaces a
	// "parent_closed" warning; an unknown parent is exit 3.
	var (
		parentFull string
		warnings   []string
	)
	if opts.Parent != "" {
		full, rerr := ids.Resolve(opts.Parent, knownIDs)
		if rerr != nil {
			if errors.Is(rerr, ids.ErrNotFound) {
				return CreateErrorOutput{
					Error:   "issue_not_found",
					Message: fmt.Sprintf("act create: --parent %q: no matching id", opts.Parent),
					Details: map[string]any{"query": opts.Parent},
				}, 3
			}
			var amb *ids.ErrAmbiguousID
			if errors.As(rerr, &amb) {
				candidates := amb.Candidates()
				// Exit 2 (usage): see resolve_helpers.go for the spec rationale.
				return CreateErrorOutput{
					Error:   "id_ambiguous",
					Message: fmt.Sprintf("act create: --parent %q matches %d issues", opts.Parent, len(candidates)),
					Details: map[string]any{
						"prefix":     opts.Parent,
						"candidates": candidates,
					},
					Candidates: candidates,
				}, 2
			}
			return CreateErrorOutput{
				Error:   "issue_not_found",
				Message: rerr.Error(),
				Details: map[string]any{"query": opts.Parent},
			}, 3
		}
		parentFull = full
		// Probe the parent's status via a freshly built index.
		if closed, perr := parentIsClosed(paths, full); perr == nil && closed {
			warnings = append(warnings, "parent_closed")
		}
	}

	// Step 5: build the create-op payload + derive the issue id by
	// retrying nonces up to 8 times (spec §"Edge cases").
	cfg, cerr := config.ReadConfig(paths)
	if cerr != nil {
		return CreateErrorOutput{
			Error:   "config_read_failed",
			Message: cerr.Error(),
		}, 1
	}

	knownSet := make(map[string]bool, len(knownIDs))
	for _, id := range knownIDs {
		knownSet[id] = true
	}
	exists := func(id string) bool { return knownSet[id] }

	var (
		issueID string
		payload ids.CreatePayload
	)
	for attempt := 0; attempt < 8; attempt++ {
		nonce, nerr := ids.NewNonce()
		if nerr != nil {
			return CreateErrorOutput{
				Error:   "nonce_failed",
				Message: nerr.Error(),
			}, 1
		}
		payload = ids.CreatePayload{
			Title:       opts.Title,
			Description: opts.Description,
			Priority:    priority,
			Type:        typ,
			Parent:      parentFull,
			Accept:      append([]string(nil), opts.Accept...),
			Nonce:       nonce,
		}
		id, perr := ids.PickUnique(payload, exists)
		if perr == nil {
			issueID = id
			break
		}
	}
	if issueID == "" {
		return CreateErrorOutput{
			Error:   "id_collision",
			Message: "act create: 8 nonce retries exhausted",
		}, 1
	}

	// Step 6: marshal the payload (canonical JSON) and assemble the
	// envelope. The op-package CreatePayload mirrors ids.CreatePayload's
	// wire shape; we serialize via canonicaljson directly so the on-disk
	// bytes are deterministic.
	bodyPayload, perr := canonicaljson.Marshal(payload)
	if perr != nil {
		return CreateErrorOutput{
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
		OpType:        "create",
		IssueID:       issueID,
		Payload:       bodyPayload,
		HLC:           stamp,
		NodeID:        cfg.NodeID,
	}
	if verr := env.Validate(); verr != nil {
		return CreateErrorOutput{
			Error:   "envelope_invalid",
			Message: verr.Error(),
		}, 1
	}
	body, merr := env.Marshal()
	if merr != nil {
		return CreateErrorOutput{
			Error:   "marshal_failed",
			Message: merr.Error(),
		}, 1
	}

	// Step 7: write + auto-commit (and run post-create hook).
	var gops *gitops.GitOps
	if !opts.NoCommit {
		gops = gitops.NewGitOps(repoRoot)
	}
	werr := WriteOpAndAutoCommit(env, body, paths, gops, WriteOpts{
		NoCommit: opts.NoCommit,
		Push:     opts.Push,
		Isolated: opts.Isolated,
	})
	if werr != nil {
		if errors.Is(werr, ErrInvalidFlags) {
			return CreateErrorOutput{
				Error:   "bad_flag",
				Message: werr.Error(),
			}, 2
		}
		return CreateErrorOutput{
			Error:   "write_failed",
			Message: werr.Error(),
		}, 1
	}

	// Refresh the live SQLite index so doctor's index-divergence check
	// passes immediately after a successful create. The op log on disk is
	// the source of truth; the index is a derived cache.
	if err := RefreshIndexForIssue(paths, issueID); err != nil {
		return CreateErrorOutput{
			Error:   "index_update_failed",
			Message: err.Error(),
		}, 1
	}

	// Step 8: success envelope. The short id mirrors the on-disk prefix
	// of the issue id (the first 4 hex chars after `act-`).
	short := issueID
	if len(issueID) > len("act-")+ids.MinShortHexLen {
		short = issueID[:len("act-")+ids.MinShortHexLen]
	}
	return CreateResult{
		ID:       issueID,
		ShortID:  short,
		Title:    opts.Title,
		Warnings: warnings,
	}, 0
}

// FormatCreateHuman renders a CreateResult in the human-friendly form:
// `Created <short> "<title>"\n`. A trailing newline is included so the
// caller can pipe directly to stdout.
func FormatCreateHuman(res CreateResult) string {
	return fmt.Sprintf("Created %s %q\n", res.ShortID, res.Title)
}

// parentIsClosed reports whether parentID resolves to a closed issue. It
// rebuilds the index against the on-disk op log (so newly written ops are
// observable) and consults the issues table. Errors and a missing parent
// row are surfaced as (false, err); callers treat err==nil + closed=false
// as "not closed".
func parentIsClosed(paths config.LayoutPaths, parentID string) (bool, error) {
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		return false, err
	}
	defer func() { _ = idx.Close() }()
	if err := idx.Rebuild(paths.Ops); err != nil {
		return false, err
	}
	row, err := idx.Get(parentID)
	if err != nil {
		return false, err
	}
	return row.Status == "closed", nil
}
