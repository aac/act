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
	// NoCommit, Push, Isolated, Offline mirror the universal write flags.
	NoCommit bool
	Push     bool
	Isolated bool
	// Offline (Phase 2 ticket 3b).
	Offline bool
	// Branch, when non-empty, names the branch in the nested .act/ repo
	// that the auto-commit lands on and the push targets on origin.
	// See cli.WriteOpts.Branch (util.go) and act-5d6a.
	Branch string
	// BlockedBy, when non-empty, attaches one add_dep (type=blocks) op per
	// id alongside the create op. Each id is resolved via the prefix
	// pipeline; duplicates resolving to the same full id are folded to one
	// edge. Edge direction is new→id (the new issue is blocked by id),
	// matching the semantic of `act_block`'s `blocked_by` parameter — so
	// agents who already know `act_block` read the flag the same way.
	BlockedBy []string
	// Blocks, when non-empty, attaches one add_dep (type=blocks) op per
	// id alongside the create op, mirroring BlockedBy in the inverse
	// direction: the new issue blocks each <id>. Each add_dep envelope is
	// written under the existing target's shard (IssueID=<existing>,
	// payload.parent=<new>) because the existing issue's deps[] is what
	// grows — symmetric to `act dep add <existing> --blocks <new>`.
	//
	// Mutual constraints:
	//   - An id appearing in BOTH Blocks and BlockedBy is bad_flag (would
	//     record a 2-cycle and immediately deadlock both issues in ready).
	//   - Self-loop guard: an id resolving to the new issue's id is
	//     bad_flag, matching the BlockedBy guard.
	//   - Duplicates within Blocks fold to a single edge.
	Blocks []string
}

// CreateResult is the JSON shape returned on success. Field order is
// irrelevant for the JSON encoder; the spec example renders id, prefix,
// title, warnings (when present).
// CreateResult is the JSON shape returned on success. Spec-v2.md §"act
// create" mandates the {ok, id, prefix, op_id, committed, pushed} core;
// title and warnings are additive (the spec doesn't forbid extras and
// they're load-bearing for human-readable output and op-rejection
// signaling).
type CreateResult struct {
	Ok        bool     `json:"ok"`
	ID        string   `json:"id"`
	Prefix    string   `json:"prefix"`
	OpID      string   `json:"op_id"`
	Committed bool     `json:"committed"`
	Pushed    bool     `json:"pushed"`
	Title     string   `json:"title"`
	Warnings  []string `json:"warnings,omitempty"`
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
	// Priority defaults to 2 when the caller did not pass --priority — the
	// value spec-v2.md §"Issue model" and §"act create" both mandate. An
	// explicit -p 0 (Priority pointing at 0) is preserved verbatim so the
	// payload records priority=0 and not the default. Bumped from the
	// v0.1.0 in-code 1 to 2 (act-d9c7) so default-filed follow-ups sort
	// below intentional p=1 work, matching the docs.
	priority := 2
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
	if opts.Offline && opts.Push {
		return CreateErrorOutput{
			Error:   "bad_flag",
			Message: "act create: --offline and --push are mutually exclusive",
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

	// Step 6b: resolve --blocked-by and --blocks ids (if any) into add_dep
	// envelopes that ride alongside the create op in a single atomic batch.
	//
	// Direction is the only thing that differs between the two flags:
	//   --blocked-by <id>: new issue's deps grow → envelope.IssueID=<new>,
	//                      payload.parent=<id> (the new issue is blocked by id)
	//   --blocks <id>:     existing issue's deps grow → envelope.IssueID=<id>,
	//                      payload.parent=<new> (the new issue blocks id)
	//
	// Duplicates within each flag are folded; an id appearing in BOTH
	// flags is bad_flag (a 2-cycle: new blocks X AND X blocks new would
	// deadlock both in `act ready`). Resolution errors and self-loop
	// guard match for symmetry — see the inlined helper.
	var depEnvs []op.Envelope
	var depBodies [][]byte
	seenBlockedBy := make(map[string]bool, len(opts.BlockedBy))
	seenBlocks := make(map[string]bool, len(opts.Blocks))

	resolveDepID := func(flagName, raw string) (string, *CreateErrorOutput, int) {
		if raw == "" {
			return "", &CreateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act create: %s: id is required and must be non-empty", flagName),
			}, 2
		}
		full, rerr := ids.Resolve(raw, knownIDs)
		if rerr != nil {
			if errors.Is(rerr, ids.ErrNotFound) {
				return "", &CreateErrorOutput{
					Error:   "issue_not_found",
					Message: fmt.Sprintf("act create: %s %q: no matching id", flagName, raw),
					Details: map[string]any{"query": raw},
				}, 3
			}
			var amb *ids.ErrAmbiguousID
			if errors.As(rerr, &amb) {
				candidates := amb.Candidates()
				return "", &CreateErrorOutput{
					Error:   "id_ambiguous",
					Message: fmt.Sprintf("act create: %s %q matches %d issues", flagName, raw, len(candidates)),
					Details: map[string]any{
						"prefix":     raw,
						"candidates": candidates,
					},
					Candidates: candidates,
				}, 2
			}
			return "", &CreateErrorOutput{
				Error:   "issue_not_found",
				Message: rerr.Error(),
				Details: map[string]any{"query": raw},
			}, 3
		}
		// Defensive self-loop guard. Under the single-writer model this
		// is unreachable because knownIDs is snapshotted before PickUnique
		// generates issueID — ids.Resolve cannot return issueID. But a
		// concurrent writer's create op between the snapshot and PickUnique
		// could theoretically smuggle our new id into a future call's
		// knownIDs; guarding here matches act_block (composed.go:339).
		if full == issueID {
			return "", &CreateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act create: %s cannot reference the issue being created (self-loop)", flagName),
			}, 2
		}
		return full, nil, 0
	}

	// buildDepEnvelope constructs the add_dep envelope+body pair. issueID
	// of the envelope is the issue whose deps[] is growing; payload.parent
	// is the OTHER side of the edge.
	buildDepEnvelope := func(envIssueID, parentID string) (op.Envelope, []byte, *CreateErrorOutput, int) {
		depPayload := op.AddDepPayload{Parent: parentID, EdgeType: "blocks"}
		if verr := depPayload.Validate(); verr != nil {
			return op.Envelope{}, nil, &CreateErrorOutput{Error: "payload_invalid", Message: verr.Error()}, 1
		}
		depBody, perr := canonicaljson.Marshal(depPayload)
		if perr != nil {
			return op.Envelope{}, nil, &CreateErrorOutput{Error: "marshal_failed", Message: perr.Error()}, 1
		}
		depStamp := clock.Send()
		depStamp.NodeID = cfg.NodeID
		depEnv := op.Envelope{
			OpVersion:     op.CurrentOpVersion,
			SchemaVersion: op.CurrentSchemaVersion,
			WriterVersion: op.WriterVersion,
			OpType:        "add_dep",
			IssueID:       envIssueID,
			Payload:       depBody,
			HLC:           depStamp,
			NodeID:        cfg.NodeID,
		}
		if verr := depEnv.Validate(); verr != nil {
			return op.Envelope{}, nil, &CreateErrorOutput{Error: "envelope_invalid", Message: verr.Error()}, 1
		}
		depEnvBody, merr := depEnv.Marshal()
		if merr != nil {
			return op.Envelope{}, nil, &CreateErrorOutput{Error: "marshal_failed", Message: merr.Error()}, 1
		}
		return depEnv, depEnvBody, nil, 0
	}

	for _, raw := range opts.BlockedBy {
		parent, errOut, code := resolveDepID("--blocked-by", raw)
		if errOut != nil {
			return *errOut, code
		}
		if seenBlockedBy[parent] {
			continue
		}
		seenBlockedBy[parent] = true
		depEnv, depEnvBody, errOut, code := buildDepEnvelope(issueID, parent)
		if errOut != nil {
			return *errOut, code
		}
		depEnvs = append(depEnvs, depEnv)
		depBodies = append(depBodies, depEnvBody)
	}

	// blocksTargets tracks the existing issues whose deps[] grew, so we
	// can refresh the index for each after the batch commits. The new
	// issue's index refresh is unconditional (see below).
	var blocksTargets []string
	for _, raw := range opts.Blocks {
		existing, errOut, code := resolveDepID("--blocks", raw)
		if errOut != nil {
			return *errOut, code
		}
		// Reject if this id is already on the --blocked-by side: would
		// be a 2-cycle (new blocks X and X blocks new), immediately
		// deadlocking both issues in `act ready`.
		if seenBlockedBy[existing] {
			return CreateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act create: id %s appears in both --blocked-by and --blocks (would record a 2-cycle)", ShortIssueID(existing)),
			}, 2
		}
		if seenBlocks[existing] {
			continue
		}
		seenBlocks[existing] = true
		depEnv, depEnvBody, errOut, code := buildDepEnvelope(existing, issueID)
		if errOut != nil {
			return *errOut, code
		}
		depEnvs = append(depEnvs, depEnv)
		depBodies = append(depBodies, depEnvBody)
		blocksTargets = append(blocksTargets, existing)
	}

	// Step 7: write + auto-commit. With no dep flags, take the
	// hook-aware single-op path. With one or more, batch the create +
	// add_dep ops into a single atomic commit; on any failure between
	// op-write and commit-success, rollback unstages and removes every
	// written op file so the bug never exists with no edge.
	//
	// Multi-op batches do not fire per-op-type hooks (matching the
	// composed `act_block` pattern); projects with a `create` hook will
	// observe it on the no-dep-flag path only.
	var gops *gitops.ActGitOps
	if !opts.NoCommit {
		// Phase 1: writes target the nested .act/ git repo, not the host
		// repo (docs/coordination-plane-design.md delta item 2). paths.Root
		// is <hostRoot>/.act, the working tree of the nested repo set up
		// by act init.
		gops = gitops.NewActGitOps(paths.Root)
	}
	var werr error
	if len(depEnvs) == 0 {
		werr = WriteOpAndAutoCommit(env, body, paths, gops, WriteOpts{
			NoCommit: opts.NoCommit,
			Push:     opts.Push,
			Isolated: opts.Isolated,
			Offline:  opts.Offline,
			Branch:   opts.Branch,
		})
	} else {
		envs := append([]op.Envelope{env}, depEnvs...)
		bodies := append([][]byte{body}, depBodies...)
		commitMsg := BuildBatchCommitMessage(env, len(envs))
		werr = WriteOpsAndAutoCommit(envs, bodies, paths, gops, WriteOpts{
			NoCommit: opts.NoCommit,
			Push:     opts.Push,
			Isolated: opts.Isolated,
			Offline:  opts.Offline,
			Branch:   opts.Branch,
		}, commitMsg)
	}
	if werr != nil {
		if errors.Is(werr, ErrInvalidFlags) {
			return CreateErrorOutput{
				Error:   "bad_flag",
				Message: werr.Error(),
			}, 2
		}
		if msg, details, isHook := HookFailureDetails(werr); isHook {
			return CreateErrorOutput{
				Error:   "hook_failed",
				Message: msg,
				Details: details,
			}, 1
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
	// --blocks targets' deps[] grew; their cached rows must be refreshed
	// too or `act ready` will continue to surface them until the next
	// rebuild. Skip in --no-commit (consistent with the rest: --no-commit
	// is the bootstrap/migration escape hatch where the caller drives
	// indexing).
	if !opts.NoCommit {
		for _, target := range blocksTargets {
			if err := RefreshIndexForIssue(paths, target); err != nil {
				return CreateErrorOutput{
					Error:   "index_update_failed",
					Message: err.Error(),
				}, 1
			}
		}
	}

	// Step 8: success envelope. Prefix mirrors the marker that
	// BuildOpCommitMessage embeds in the auto-commit subject so doctor's
	// orphan-close grep stays aligned with the JSON output. op_id is the
	// short hash form (env.Hash); for the full sha256, callers can refold
	// the op file.
	opID, _ := env.Hash()
	return CreateResult{
		Ok:        true,
		ID:        issueID,
		Prefix:    ShortIssueID(issueID),
		OpID:      opID,
		Committed: !opts.NoCommit,
		Pushed:    opts.Push && !opts.Isolated,
		Title:     opts.Title,
		Warnings:  warnings,
	}, 0
}

// FormatCreateHuman renders a CreateResult in the human-friendly form:
// `Created <short> "<title>"\n`. A trailing newline is included so the
// caller can pipe directly to stdout.
func FormatCreateHuman(res CreateResult) string {
	return fmt.Sprintf("Created %s %q\n", res.Prefix, res.Title)
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
