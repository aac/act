package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/claim"
	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/index"
	"github.com/aac/act/internal/op"
)

// UpdateOptions captures the flag knobs accepted by `act update`.
//
// Per spec §3 `act update <id>` (lines 639-669) and §5.A.4, §5.B.3,
// §5.C.2, §5.C.3. Pointer fields (*string) record explicit caller
// presence so the empty string ("") can clear a field (e.g. --assignee "")
// while a nil pointer means the flag was not supplied.
type UpdateOptions struct {
	// ID is the positional <id> argument (full or prefix).
	ID string

	// Field flags. nil ⇒ not supplied; non-nil ⇒ explicit user choice
	// (including the clearing form `--assignee ""`).
	Status      *string
	Priority    *int
	Assignee    *string
	Description *string

	// Title, Type and Parent complete the six LWW-per-field updatable
	// fields the spec's §3 fold table names (title, description,
	// priority, type, assignee, parent). The fold and the op-layer
	// `validUpdateFields` set have always accepted all six; only the
	// write path was short three of them (act-3e21b8, act-3e2986), so
	// `act update` could not fix a title that had gone stale — the
	// field every `act list` / `act ready` row shows and every
	// dispatcher reads first.
	//
	// Title is required non-empty when supplied: unlike Assignee and
	// Description, "" is not a meaningful title, and `act create`
	// rejects it too. Cap is 256 bytes, matching `act create`.
	Title *string

	// Type accepts the same enum as `act create --type`
	// (task|bug|epic|chore). A mistyped value is rejected up front
	// rather than written as an op the fold would happily store and
	// every type filter would then miss.
	Type *string

	// Parent sets the hierarchy parent (NOT a dep edge — see
	// `act dep add` for blocking). The value is id-resolved like any
	// other id argument, so a prefix works. The empty string clears the
	// parent, which is the only way to detach a child that was created
	// under the wrong epic.
	//
	// Guarded against self-parenting and against closing a cycle in the
	// parent chain: `act delete --cascade`'s descendant walk is
	// cycle-safe, but a parent cycle is nonsense state that no reader
	// can render, so it is rejected at the write path the same way
	// `act dep add` rejects a cycle in the blocks subgraph.
	Parent *string

	// DescriptionAppend, when non-nil, appends its text to the issue's
	// CURRENT description rather than replacing it (act-a79d66). act
	// resolves the existing description server-side and writes a single
	// update_field{description} op carrying the concatenation, so the
	// caller does not have to read-modify-write the whole body — the
	// round-trip that made `act update --description-file` an unnatural
	// home for a one-line annotation, and that pushed two agents into
	// `act log <id> "message"` instead.
	//
	// Additive sibling to Description, mirroring how AcceptAdd sits
	// beside Accept. Mutually exclusive with Description: "replace with
	// X" and "append X" are contradictory instructions, so the pair is
	// rejected rather than silently ordered.
	//
	// Separator: exactly one blank line between the existing body and
	// the appended text, so successive appends read as paragraphs. An
	// empty existing description yields the appended text alone (no
	// leading blank lines).
	DescriptionAppend *string

	// Repeatables.
	//
	// Accept, when non-nil, REPLACES the acceptance list with exactly these
	// criteria (one set_accept op). A nil slice means "--accept not supplied"
	// — distinct from an empty non-nil slice, which clears all criteria. This
	// is the replace primitive: repeated `act update --accept` edits set the
	// list rather than unioning with prior criteria.
	Accept []string
	// AcceptSet records whether --accept was supplied at all, so an explicit
	// `--accept ""`-equivalent (zero criteria) can clear the list while an
	// absent flag is a no-op. The CLI layer (cmd/act/update.go) sets this from
	// flag presence; the MCP layer sets it whenever the JSON `accept` key is
	// present (including an empty array).
	AcceptSet bool
	// AcceptAdd appends criteria to the existing list (one add_accept op per
	// entry) — the additive flow preserved for `--accept-add`. create-then-add
	// remains the canonical additive path; this is its update-time sibling.
	AcceptAdd []string
	// AcceptRm removes individual criteria by zero-based index against the
	// current effective (visible) list (one remove_accept op per entry). This
	// is the non-destructive remove/replace affordance.
	AcceptRm []int
	DepRm    []string
	// Unclaim, when true, releases a claim: it writes one unclaim op,
	// returning an in_progress issue to open and clearing the assignee
	// (act-086781). Mutually exclusive with Claim. On a not-in_progress
	// issue the op folds to a no-op (idempotent), same as ExtRm on absence.
	Unclaim bool
	// ExtRm is the list of opaque refs to clear. Each entry generates one
	// remove_external_dep op. Clearing a not-present ref is a no-op — the
	// orchestrator owns the lifecycle and may double-fire safely.
	ExtRm []string

	// Mode flags.
	Claim       bool
	Wait        bool
	WaitTimeout time.Duration

	// Force overrides the external-dep gate on the --claim path (act-5e36).
	// When true and the issue has ≥1 open external dep, a WARNING is emitted
	// to os.Stderr and the claim proceeds normally. Without --force, open
	// external deps cause exit 2 with envelope `blocked_by_external_dep`.
	Force bool

	// Universal write flags.
	Push     bool
	NoCommit bool
	Isolated bool
	// Offline (Phase 2 ticket 3b): commit locally, skip push, append
	// pending-push record.
	Offline bool
	// Branch, when non-empty, names the branch in the nested .act/ repo
	// the auto-commit lands on and the push targets. See
	// cli.WriteOpts.Branch and act-5d6a. On the --claim path, the value
	// is honored by the claim wrapper so the win-commit lands on the
	// same branch.
	Branch string
	AsJSON bool
	Verify bool
}

// UpdateResult is the JSON shape returned on successful non-claim runs:
//
//	{"id": "...", "ops_written": N, "committed": true|false}
type UpdateResult struct {
	ID         string `json:"id"`
	OpsWritten int    `json:"ops_written"`
	Committed  bool   `json:"committed"`
}

// UpdateClaimResult is the JSON shape returned by `act update --claim`.
// Field tags mirror spec §3 `act update` JSON output examples.
//
// On a claim loss the result carries Error=claim_lost so the JSON envelope
// surfaces the canonical error slug (spec §error-envelope: claim_lost, exit
// 5) alongside the structured claim fields (claimed:false, winner, reason).
// The Error field is omitted on the win path.
type UpdateClaimResult struct {
	OK         bool     `json:"ok"`
	Claimed    bool     `json:"claimed"`
	ID         string   `json:"id"`
	Winner     string   `json:"winner"`
	Error      string   `json:"error,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	OpsWritten []string `json:"ops_written,omitempty"`
}

// UpdateErrorOutput is the structured failure envelope. Candidates is non-nil
// only on the id_ambiguous path; it is also mirrored under
// Details["candidates"] so the on-the-wire JSON envelope matches spec
// §"Errors" (`details.candidates[]`).
type UpdateErrorOutput struct {
	Error      string         `json:"error"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	Candidates []string       `json:"-"`
}

// validUpdateStatuses is the closed set of values acceptable on the
// non-claim --status flag. `closed` is intentionally absent (§5.C.2:
// always exit 2). `in_progress` is also absent: the user must use
// `--claim` (§5.B.3). `blocked` is present here for syntax validation
// but requires a backing dep edge at the CLI layer — see the
// blocked_requires_dep check after id-resolution.
var validUpdateStatuses = map[string]bool{
	"open":    true,
	"blocked": true,
}

// RunUpdate implements `act update <id>`.
//
// Returns:
//   - output: UpdateResult on non-claim success, UpdateClaimResult on
//     claim, UpdateErrorOutput on failure.
//   - exitCode: 0 success / claim win; 5 claim loss (envelope claim_lost);
//     1 other logical failure; 2 bad flags / forbidden combinations;
//     3 missing repo / unknown id.
func RunUpdate(repoRoot string, opts UpdateOptions) (output any, exitCode int) {
	// Step 1: repo + .act/ required.
	if !hasGitDir(repoRoot) {
		return UpdateErrorOutput{
			Error:   "not_in_git",
			Message: fmt.Sprintf("act update: %s is not inside a git working tree", repoRoot),
		}, 3
	}
	paths := config.Layout(repoRoot)
	if _, err := os.Stat(paths.ConfigJSON); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return UpdateErrorOutput{
				Error:   "act_not_initialized",
				Message: fmt.Sprintf("act update: %s/.act not initialized; run `act init` first", repoRoot),
			}, 3
		}
		return UpdateErrorOutput{
			Error:   "act_not_initialized",
			Message: fmt.Sprintf("act update: stat %s: %v", paths.ConfigJSON, err),
		}, 3
	}

	// Step 2a: the universal write-flag conflict pair always trumps other
	// validation (per spec §4 + §5.A.4). Surface as bad_flag exit 2.
	if opts.NoCommit && opts.Push {
		return UpdateErrorOutput{
			Error:   "bad_flag",
			Message: "act update: --no-commit and --push are mutually exclusive",
		}, 2
	}
	if opts.Isolated && opts.Push {
		return UpdateErrorOutput{
			Error:   "bad_flag",
			Message: "act update: --isolated and --push are mutually exclusive",
		}, 2
	}
	if opts.Offline && opts.Push {
		return UpdateErrorOutput{
			Error:   "bad_flag",
			Message: "act update: --offline and --push are mutually exclusive",
		}, 2
	}

	// Step 2a.2: --description and --description-append are contradictory
	// (replace vs. extend). Reject rather than pick an order (act-a79d66).
	if opts.Description != nil && opts.DescriptionAppend != nil {
		return UpdateErrorOutput{
			Error:   "bad_flag",
			Message: "act update: --description and --description-append are mutually exclusive",
		}, 2
	}

	// Step 2b: --status closed is exit 2 unconditionally per §5.C.2; check
	// before id-resolution so the failure is independent of repo state.
	if opts.Status != nil {
		if *opts.Status == "closed" {
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: "act update: --status closed is not supported; use `act close`",
			}, 2
		}
		if *opts.Status == "in_progress" {
			// §5.B.3: status=in_progress only via --claim.
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: "act update: --status in_progress is not supported; use `act update --claim`",
			}, 2
		}
		if !validUpdateStatuses[*opts.Status] {
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act update: --status %q: must be one of open|blocked (use --claim for in_progress, `act close` for closed)", *opts.Status),
			}, 2
		}
	}

	// Step 2c: --wait requires --claim.
	if opts.Wait && !opts.Claim {
		return UpdateErrorOutput{
			Error:   "bad_flag",
			Message: "act update: --wait requires --claim",
		}, 2
	}

	// Step 2c.2: --claim and --unclaim are direct opposites (act-086781).
	if opts.Claim && opts.Unclaim {
		return UpdateErrorOutput{
			Error:   "bad_flag",
			Message: "act update: --claim and --unclaim are mutually exclusive",
		}, 2
	}

	// Step 2d: --claim mutually-exclusive guard. Most field flags conflict
	// with --claim because the claim protocol writes its own op type.
	// --json, --push, --wait, --wait-timeout, --isolated, --no-commit, and
	// --verify remain compatible.
	if opts.Claim {
		var conflicts []string
		if opts.Status != nil {
			conflicts = append(conflicts, "--status")
		}
		if opts.Priority != nil {
			conflicts = append(conflicts, "--priority")
		}
		if opts.Assignee != nil {
			conflicts = append(conflicts, "--assignee")
		}
		if opts.Description != nil {
			conflicts = append(conflicts, "--description")
		}
		if opts.DescriptionAppend != nil {
			conflicts = append(conflicts, "--description-append")
		}
		if opts.Title != nil {
			conflicts = append(conflicts, "--title")
		}
		if opts.Type != nil {
			conflicts = append(conflicts, "--type")
		}
		if opts.Parent != nil {
			conflicts = append(conflicts, "--parent")
		}
		if opts.AcceptSet {
			conflicts = append(conflicts, "--accept")
		}
		if len(opts.AcceptAdd) > 0 {
			conflicts = append(conflicts, "--accept-add")
		}
		if len(opts.AcceptRm) > 0 {
			conflicts = append(conflicts, "--accept-rm")
		}
		if len(opts.DepRm) > 0 {
			conflicts = append(conflicts, "--dep-rm")
		}
		if len(opts.ExtRm) > 0 {
			conflicts = append(conflicts, "--ext-rm")
		}
		if len(conflicts) > 0 {
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act update: --claim is mutually exclusive with: %s", strings.Join(conflicts, ", ")),
			}, 2
		}
	}

	// Step 2e: priority range.
	if opts.Priority != nil {
		if *opts.Priority < 0 || *opts.Priority > 3 {
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act update: --priority %d out of range [0,3]", *opts.Priority),
			}, 2
		}
	}

	// Step 2f: --title is non-empty and length-capped. Both mirror
	// `act create`, whose title argument carries the same rules — a
	// retitle should not be able to reach a state create could not.
	if opts.Title != nil {
		if *opts.Title == "" {
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: "act update: --title: title is empty (a title cannot be cleared; supply replacement text)",
			}, 2
		}
		if len(*opts.Title) > 256 {
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act update: --title: length %d > 256 bytes", len(*opts.Title)),
			}, 2
		}
	}

	// Step 2g: --type is the same closed enum `act create --type` takes.
	if opts.Type != nil {
		switch *opts.Type {
		case "task", "bug", "epic", "chore":
		default:
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act update: --type %q: not one of task, bug, epic, chore", *opts.Type),
			}, 2
		}
	}

	// Step 3: resolve <id>.
	if opts.ID == "" {
		return UpdateErrorOutput{
			Error:   "bad_flag",
			Message: "act update: <id> is required",
		}, 2
	}
	knownIDs, err := listIssueIDs(paths.Ops)
	if err != nil {
		return UpdateErrorOutput{
			Error:   "ops_scan_failed",
			Message: err.Error(),
		}, 1
	}
	full, rerr := ids.Resolve(opts.ID, knownIDs)
	if rerr != nil {
		if errors.Is(rerr, ids.ErrNotFound) {
			return UpdateErrorOutput{
				Error:   "issue_not_found",
				Message: fmt.Sprintf("act update: %q: no matching id", opts.ID),
				Details: map[string]any{"query": opts.ID},
			}, 3
		}
		var amb *ids.ErrAmbiguousID
		if errors.As(rerr, &amb) {
			candidates := amb.Candidates()
			// Exit 2 (usage): see resolve_helpers.go for the spec rationale.
			return UpdateErrorOutput{
				Error:   "id_ambiguous",
				Message: fmt.Sprintf("act update: prefix %q matches %d issues", opts.ID, len(candidates)),
				Details: map[string]any{
					"prefix":     opts.ID,
					"candidates": candidates,
				},
				Candidates: candidates,
			}, 2
		}
		return UpdateErrorOutput{
			Error:   "issue_not_found",
			Message: rerr.Error(),
			Details: map[string]any{"query": opts.ID},
		}, 3
	}

	// Step 4: claim mode dispatch.
	if opts.Claim {
		return runUpdateClaim(repoRoot, full, opts)
	}

	// Step 5: non-claim mutation. We must have at least one mutating flag.
	if opts.Status == nil && opts.Priority == nil && opts.Assignee == nil && opts.Description == nil && opts.DescriptionAppend == nil && opts.Title == nil && opts.Type == nil && opts.Parent == nil && !opts.AcceptSet && len(opts.AcceptAdd) == 0 && len(opts.AcceptRm) == 0 && len(opts.DepRm) == 0 && len(opts.ExtRm) == 0 && !opts.Unclaim {
		return UpdateErrorOutput{
			Error:   "bad_flag",
			Message: "act update: at least one of --title, --status, --priority, --type, --parent, --assignee, --description, --description-append, --accept, --accept-add, --accept-rm, --dep-rm, --ext-rm, --claim, or --unclaim must be supplied",
		}, 2
	}

	// Step 6: read config (for node id) and rebuild the index so --dep-rm
	// can verify existing edges.
	cfg, cerr := config.ReadConfig(paths)
	if cerr != nil {
		return UpdateErrorOutput{
			Error:   "config_read_failed",
			Message: cerr.Error(),
		}, 1
	}

	// Step 6b: --status=blocked requires a backing blocked-by dep edge.
	// 'blocked' is derived from open blocks dep edges set via
	// `act dep add --blocked-by`; a direct --status=blocked with no such
	// edge would write a no-op op (applyUpdateField silently ignores
	// status updates) and report false success. Reject it early so the
	// caller gets a clear error instead of a silent no-op.
	if opts.Status != nil && *opts.Status == "blocked" {
		idx, ierr := index.Open(paths.IndexDB)
		if ierr != nil {
			return UpdateErrorOutput{
				Error:   "index_open_failed",
				Message: ierr.Error(),
			}, 1
		}
		if rerr := idx.Rebuild(paths.Ops); rerr != nil {
			_ = idx.Close()
			return UpdateErrorOutput{
				Error:   "index_rebuild_failed",
				Message: rerr.Error(),
			}, 1
		}
		row, gerr := idx.Get(full)
		_ = idx.Close()
		if gerr != nil {
			return UpdateErrorOutput{
				Error:   "index_query_failed",
				Message: gerr.Error(),
			}, 1
		}
		hasBlocksDep := false
		for _, d := range row.Deps {
			if d.EdgeType == "blocks" {
				hasBlocksDep = true
				break
			}
		}
		if !hasBlocksDep {
			return UpdateErrorOutput{
				Error:   "blocked_requires_dep",
				Message: fmt.Sprintf("act update: --status blocked: %s has no blocked-by dep edge; add one first with `act dep add %s <blocker-id> --type blocks`", full, full),
			}, 2
		}
	}

	// Step 6c: --status open releases a claim (act-bf9e9d). It routes to the
	// unclaim op below (see op assembly) so an in_progress issue returns to
	// open AND the claim high-water mark is cleared — without which the issue
	// never reappears in `act ready` and can't be re-claimed. Historically
	// --status open wrote an update_field{status:open} op that applyUpdateField
	// silently ignores (status LWW is governed only by close/reopen), so the
	// command reported success while the projection stayed in_progress: a stale
	// claim that was unreleasable through the surface an agent naturally reaches
	// for. Before routing to unclaim, reject the one transition unclaim can't
	// make: `open` on a closed issue is a reopen (its own verb resets the
	// close/claim HLCs); silently no-oping it — the old bug's sibling — would
	// report success while nothing changed. Point the caller at `act reopen`.
	if opts.Status != nil && *opts.Status == "open" {
		idx, ierr := index.Open(paths.IndexDB)
		if ierr != nil {
			return UpdateErrorOutput{
				Error:   "index_open_failed",
				Message: ierr.Error(),
			}, 1
		}
		if rerr := idx.Rebuild(paths.Ops); rerr != nil {
			_ = idx.Close()
			return UpdateErrorOutput{
				Error:   "index_rebuild_failed",
				Message: rerr.Error(),
			}, 1
		}
		row, gerr := idx.Get(full)
		_ = idx.Close()
		if gerr != nil {
			return UpdateErrorOutput{
				Error:   "index_query_failed",
				Message: gerr.Error(),
			}, 1
		}
		if row.Status == "closed" {
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act update: --status open on the closed issue %s is not supported; use `act reopen %s`", full, full),
			}, 2
		}
	}

	// The folded view is needed by --dep-rm (to verify the edge exists)
	// and by a non-clearing --parent (to walk the existing parent chain
	// for a cycle). One rebuild serves both.
	var rows []index.Row
	if len(opts.DepRm) > 0 || (opts.Parent != nil && *opts.Parent != "") {
		idx, ierr := index.Open(paths.IndexDB)
		if ierr != nil {
			return UpdateErrorOutput{
				Error:   "index_open_failed",
				Message: ierr.Error(),
			}, 1
		}
		if rerr := idx.Rebuild(paths.Ops); rerr != nil {
			_ = idx.Close()
			return UpdateErrorOutput{
				Error:   "index_rebuild_failed",
				Message: rerr.Error(),
			}, 1
		}
		all, lerr := idx.ListAll(index.Filter{})
		_ = idx.Close()
		if lerr != nil {
			return UpdateErrorOutput{
				Error:   "index_query_failed",
				Message: lerr.Error(),
			}, 1
		}
		rows = all
	}

	// Step 6c2: resolve and guard --parent. The empty string clears the
	// parent and needs no resolution. A non-empty value is id-resolved
	// (so a prefix works, like every other id argument), then checked
	// for the two states no reader can make sense of: an issue that is
	// its own parent, and a cycle in the parent chain.
	//
	// The cycle walk climbs from the PROPOSED parent upward through the
	// existing hierarchy; if it reaches this issue, adding the edge
	// would close the loop. `act dep add` rejects a cycle in the blocks
	// subgraph for the same reason, and `act doctor`'s cycle check only
	// covers blocks — nothing downstream would report a parent cycle.
	parentValue := ""
	if opts.Parent != nil && *opts.Parent != "" {
		resolved, rerr := ids.Resolve(*opts.Parent, knownIDs)
		if rerr != nil {
			if errors.Is(rerr, ids.ErrNotFound) {
				return UpdateErrorOutput{
					Error:   "issue_not_found",
					Message: fmt.Sprintf("act update: --parent %q: no matching id", *opts.Parent),
					Details: map[string]any{"query": *opts.Parent},
				}, 3
			}
			var amb *ids.ErrAmbiguousID
			if errors.As(rerr, &amb) {
				candidates := amb.Candidates()
				return UpdateErrorOutput{
					Error:   "id_ambiguous",
					Message: fmt.Sprintf("act update: --parent %q matches %d issues", *opts.Parent, len(candidates)),
					Details: map[string]any{
						"prefix":     *opts.Parent,
						"candidates": candidates,
					},
					Candidates: candidates,
				}, 2
			}
			return UpdateErrorOutput{
				Error:   "issue_not_found",
				Message: rerr.Error(),
				Details: map[string]any{"query": *opts.Parent},
			}, 3
		}
		if resolved == full {
			return UpdateErrorOutput{
				Error:   "cycle_detected",
				Message: fmt.Sprintf("act update: --parent: %s cannot be its own parent", ShortIssueID(full)),
			}, 2
		}
		if path, cyclic := parentChainReaches(rows, resolved, full); cyclic {
			return UpdateErrorOutput{
				Error:   "cycle_detected",
				Message: fmt.Sprintf("act update: --parent: cycle detected in the parent hierarchy: %s", strings.Join(path, " -> ")),
				Details: map[string]any{"path": path},
			}, 2
		}
		parentValue = resolved
	}

	// Step 6d: resolve --description-append against the CURRENT description
	// (act-a79d66). This is the read half of the read-modify-write that the
	// caller would otherwise have to perform with --description-file; doing
	// it here means the append is one command instead of a round-trip, and
	// the caller never has to hold the whole body.
	//
	// Note this is a read-then-write against the folded snapshot, not an
	// atomic append: two concurrent appends to the same issue resolve
	// last-write-wins on the description field, same as two concurrent
	// --description writes. That matches the existing update_field
	// semantics rather than inventing a new concurrency contract; a true
	// append-only annotation stream would need its own op type.
	if opts.DescriptionAppend != nil {
		idx, ierr := index.Open(paths.IndexDB)
		if ierr != nil {
			return UpdateErrorOutput{
				Error:   "index_open_failed",
				Message: ierr.Error(),
			}, 1
		}
		if rerr := idx.Rebuild(paths.Ops); rerr != nil {
			_ = idx.Close()
			return UpdateErrorOutput{
				Error:   "index_rebuild_failed",
				Message: rerr.Error(),
			}, 1
		}
		row, gerr := idx.Get(full)
		_ = idx.Close()
		if gerr != nil {
			return UpdateErrorOutput{
				Error:   "index_query_failed",
				Message: gerr.Error(),
			}, 1
		}
		merged := appendDescription(row.Description, *opts.DescriptionAppend)
		opts.Description = &merged
	}

	// Step 7: assemble per-flag op envelopes. Each non-empty mutating flag
	// produces one op (per spec §3 `act update`: "Each non-`--claim`
	// field flag generates one op").
	clock := hlc.NewClock(cfg.NodeID, func() int64 { return time.Now().UnixMilli() })

	var envelopes []op.Envelope
	var bodies [][]byte

	addOp := func(opType string, payload any) (UpdateErrorOutput, int) {
		bodyPayload, perr := canonicaljson.Marshal(payload)
		if perr != nil {
			return UpdateErrorOutput{
				Error:   "marshal_failed",
				Message: perr.Error(),
			}, 1
		}
		stamp := clock.Send()
		stamp.NodeID = cfg.NodeID
		env := op.Envelope{
			OpVersion:     op.CurrentOpVersion,
			SchemaVersion: op.CurrentSchemaVersion,
			WriterVersion: op.WriterVersion,
			OpType:        opType,
			IssueID:       full,
			Payload:       bodyPayload,
			HLC:           stamp,
			NodeID:        cfg.NodeID,
		}
		if verr := env.Validate(); verr != nil {
			return UpdateErrorOutput{
				Error:   "envelope_invalid",
				Message: verr.Error(),
			}, 1
		}
		body, merr := env.Marshal()
		if merr != nil {
			return UpdateErrorOutput{
				Error:   "marshal_failed",
				Message: merr.Error(),
			}, 1
		}
		envelopes = append(envelopes, env)
		bodies = append(bodies, body)
		return UpdateErrorOutput{}, 0
	}

	// Order matches spec narrative: status, priority, assignee, description,
	// accept (in supplied order), dep-rm (in supplied order). Each flag
	// produces an update_field op (or add_accept / remove_dep for the
	// list-mutating cases).
	if opts.Status != nil {
		if *opts.Status == "open" {
			// `--status open` releases a claim: emit an unclaim op (the
			// release primitive) rather than an update_field{status:open},
			// which applyUpdateField ignores — the act-bf9e9d no-op bug. On an
			// already-open issue the unclaim folds to a no-op, matching
			// --unclaim's idempotent semantics; a closed issue was rejected in
			// Step 6c above. Skip if --unclaim was also supplied so we don't
			// write a redundant second unclaim op.
			if !opts.Unclaim {
				if errOut, code := addOp("unclaim", op.UnclaimPayload{}); code != 0 {
					return errOut, code
				}
			}
		} else {
			// --status blocked: 'blocked' is derived from open blocks dep
			// edges (gated in Step 6b). The update_field op is a projection
			// no-op but kept for the audit record.
			val, _ := json.Marshal(*opts.Status)
			if errOut, code := addOp("update_field", op.UpdateFieldPayload{Field: "status", Value: val}); code != 0 {
				return errOut, code
			}
		}
	}
	if opts.Title != nil {
		val, _ := json.Marshal(*opts.Title)
		if errOut, code := addOp("update_field", op.UpdateFieldPayload{Field: "title", Value: val}); code != 0 {
			return errOut, code
		}
	}
	if opts.Priority != nil {
		val, _ := json.Marshal(*opts.Priority)
		if errOut, code := addOp("update_field", op.UpdateFieldPayload{Field: "priority", Value: val}); code != 0 {
			return errOut, code
		}
	}
	if opts.Type != nil {
		val, _ := json.Marshal(*opts.Type)
		if errOut, code := addOp("update_field", op.UpdateFieldPayload{Field: "type", Value: val}); code != 0 {
			return errOut, code
		}
	}
	// parentValue is the RESOLVED id (or "" for the clearing form), set
	// and cycle-checked in Step 6c2.
	if opts.Parent != nil {
		val, _ := json.Marshal(parentValue)
		if errOut, code := addOp("update_field", op.UpdateFieldPayload{Field: "parent", Value: val}); code != 0 {
			return errOut, code
		}
	}
	if opts.Assignee != nil {
		val, _ := json.Marshal(*opts.Assignee)
		if errOut, code := addOp("update_field", op.UpdateFieldPayload{Field: "assignee", Value: val}); code != 0 {
			return errOut, code
		}
	}
	if opts.Description != nil {
		val, _ := json.Marshal(*opts.Description)
		if errOut, code := addOp("update_field", op.UpdateFieldPayload{Field: "description", Value: val}); code != 0 {
			return errOut, code
		}
	}
	// --accept REPLACES the acceptance list (one set_accept op). Empty (but
	// supplied) clears it. Payload validation (per-criterion non-empty +
	// 500-byte cap) is gated up-front so a bad criterion fails the whole
	// update before any op hits disk.
	if opts.AcceptSet {
		pl := op.SetAcceptPayload{Criteria: opts.Accept}
		if verr := pl.Validate(); verr != nil {
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act update: --accept: %v", verr),
			}, 2
		}
		if errOut, code := addOp("set_accept", pl); code != 0 {
			return errOut, code
		}
	}
	// --accept-add APPENDS criteria (one add_accept op each). The additive
	// flow, preserved for callers building a list incrementally.
	for _, c := range opts.AcceptAdd {
		pl := op.AddAcceptPayload{Criterion: c}
		if verr := pl.Validate(); verr != nil {
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act update: --accept-add: %v", verr),
			}, 2
		}
		if errOut, code := addOp("add_accept", pl); code != 0 {
			return errOut, code
		}
	}
	// --accept-rm removes individual criteria by zero-based index (one
	// remove_accept op each). Indices resolve against the current effective
	// (visible) list at apply time; an out-of-range index is an idempotent
	// no-op at fold time (per remove_accept semantics).
	for _, idx := range opts.AcceptRm {
		pl := op.RemoveAcceptPayload{Index: idx}
		if verr := pl.Validate(); verr != nil {
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act update: --accept-rm: %v", verr),
			}, 2
		}
		if errOut, code := addOp("remove_accept", pl); code != 0 {
			return errOut, code
		}
	}
	for _, raw := range opts.DepRm {
		// Accepts either "id" (defaults to blocks) or "id:edge_type".
		id, edgeType := splitDepRm(raw)
		parentFull, code, errOut := resolveDepIDForUpdate(id, knownIDs)
		if code != 0 {
			return errOut, code
		}
		// Verify the edge exists in the folded view; missing → exit 1
		// (logical, not usage) per acceptance criteria.
		if !depEdgeExists(rows, full, parentFull, edgeType) {
			return UpdateErrorOutput{
				// Direction note: the prior `%s --[%s]--> %s` arrow read as
				// "child blocks parent", the inverse of the real semantic
				// (child is blocked BY parent). Rendered in natural English
				// via depAddSummary so the message can't be mistaken for the
				// opposite edge during direction probing.
				Error:   "dep_not_found",
				Message: fmt.Sprintf("act update: --dep-rm: no %s-edge to remove: %s does not exist", edgeType, depAddSummary(full, parentFull, edgeType)),
			}, 1
		}
		if errOut, code := addOp("remove_dep", op.RemoveDepPayload{Parent: parentFull, EdgeType: edgeType}); code != 0 {
			return errOut, code
		}
	}
	// External-dep removes. Unlike --dep-rm we do NOT validate presence: the
	// caller owns the lifecycle of the ref in its source-of-truth tracker
	// and may double-clear safely. The apply layer absorbs the absence. The
	// payload shape itself is still validated (same rules as add) so an
	// empty or oversized ref can't slip through.
	for _, ref := range opts.ExtRm {
		pl := op.RemoveExternalDepPayload{Ref: ref}
		if verr := pl.Validate(); verr != nil {
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act update: --ext-rm: %v", verr),
			}, 2
		}
		if errOut, code := addOp("remove_external_dep", pl); code != 0 {
			return errOut, code
		}
	}
	// Unclaim: release a claim (in_progress → open). Like --ext-rm we do NOT
	// pre-check the issue's status; the op is always written for the audit
	// trail and the apply layer makes it idempotent (a no-op on a
	// not-in_progress or closed issue). See fold.applyUnclaim (act-086781).
	if opts.Unclaim {
		if errOut, code := addOp("unclaim", op.UnclaimPayload{}); code != 0 {
			return errOut, code
		}
	}

	// Step 8: write each op via WriteOpAndAutoCommit. Each call performs
	// its own auto-commit (unless --no-commit); so we end up with N commits
	// when N flags were supplied. The JSON contract: committed=true means
	// at least one commit happened.
	var gops *gitops.ActGitOps
	if !opts.NoCommit {
		// Phase 1: writes target the nested .act/ git repo (delta item 2).
		gops = gitops.NewActGitOps(paths.Root)
		gops.Verify = opts.Verify
	}
	for i, env := range envelopes {
		werr := WriteOpAndAutoCommit(env, bodies[i], paths, gops, WriteOpts{
			NoCommit: opts.NoCommit,
			// Defer the push to AFTER all ops are written so we don't push
			// a partial state mid-batch.
			Push:     false,
			Isolated: opts.Isolated,
			Offline:  opts.Offline,
			Branch:   opts.Branch,
		})
		if werr != nil {
			if errors.Is(werr, ErrInvalidFlags) {
				return UpdateErrorOutput{
					Error:   "bad_flag",
					Message: werr.Error(),
				}, 2
			}
			if msg, details, isHook := HookFailureDetails(werr); isHook {
				return UpdateErrorOutput{
					Error:   "hook_failed",
					Message: msg,
					Details: details,
				}, 1
			}
			if msg, details, isLock := StaleLockDetails(werr); isLock {
				return UpdateErrorOutput{
					Error:   ErrStaleGitLock,
					Message: msg,
					Details: details,
				}, 1
			}
			return UpdateErrorOutput{
				Error:   "write_failed",
				Message: werr.Error(),
			}, 1
		}
	}

	// Step 10: optional push (after all ops committed). When --branch is
	// supplied (act-5d6a) the explicit push targets that branch on origin
	// so a stale tracking config can't route the op commit to main.
	if opts.Push && gops != nil {
		if perr := gops.PushToBranch(opts.Branch); perr != nil {
			return UpdateErrorOutput{
				Error:   "push_failed",
				Message: perr.Error(),
			}, 1
		}
	}

	// Refresh the live SQLite index so doctor's index-divergence check
	// passes immediately after a successful update. The op log on disk
	// remains the source of truth; the index is a derived cache.
	if err := RefreshIndexForIssue(paths, full); err != nil {
		return UpdateErrorOutput{
			Error:   "index_update_failed",
			Message: err.Error(),
		}, 1
	}

	return UpdateResult{
		ID:         full,
		OpsWritten: len(envelopes),
		Committed:  !opts.NoCommit,
	}, 0
}

// runUpdateClaim dispatches the --claim flow via internal/claim.RunClaim.
// On win (Result.Claimed == true): exit 0 with the win envelope.
// On loss (Claimed == false): exit 5 with the loss envelope (error
// claim_lost), per spec §error-envelope's universal exit-code table.
// On hard error (drift / write / commit / pull-rebase): exit 1 with an
// UpdateErrorOutput.
func runUpdateClaim(repoRoot, full string, opts UpdateOptions) (any, int) {
	paths := config.Layout(repoRoot)

	// External-dep gate (act-5e36): reject claim when ≥1 open external dep
	// is present unless --force overrides it. This check runs BEFORE the
	// claim protocol to ensure the gate fires on a directly-targeted id,
	// not just on the `act ready` filter that ordinarily hides such issues.
	{
		idx, ierr := index.Open(paths.IndexDB)
		if ierr != nil {
			return UpdateErrorOutput{
				Error:   "index_open_failed",
				Message: ierr.Error(),
			}, 1
		}
		if rerr := idx.Rebuild(paths.Ops); rerr != nil {
			_ = idx.Close()
			return UpdateErrorOutput{
				Error:   "index_rebuild_failed",
				Message: rerr.Error(),
			}, 1
		}
		gateRes, gerr := CheckExternalDepGate(idx, full)
		_ = idx.Close()
		if gerr != nil {
			return UpdateErrorOutput{
				Error:   "index_query_failed",
				Message: gerr.Error(),
			}, 1
		}
		if gateRes.Blocked {
			if !opts.Force {
				return UpdateBlockedByExtDepErrorOutput("act update --claim", full, gateRes.ExternalDeps), 2
			}
			EmitExtDepForceWarning(nil, full, gateRes.ExternalDeps)
		}
	}

	cfg, cerr := config.ReadConfig(paths)
	if cerr != nil {
		return UpdateErrorOutput{
			Error:   "config_read_failed",
			Message: cerr.Error(),
		}, 1
	}
	clock := hlc.NewClock(cfg.NodeID, func() int64 { return time.Now().UnixMilli() })

	// Default WaitTimeout if --wait is set but no timeout supplied.
	wait := opts.WaitTimeout
	if opts.Wait && wait == 0 {
		wait = 60 * time.Second
	}

	// Phase 1: writes target the nested .act/ git repo, so the wrapper
	// stages `ops` (relative to the nested repo) instead of `.act/ops`
	// (which would resolve into a nested-inside-nested path).
	gops := gitops.NewActGitOps(paths.Root)
	gops.Verify = opts.Verify

	// claim's GitOps interface comment ("Commit stages the ops subtree
	// and creates a single commit") is satisfied by wrapping the production
	// gitops with a staging step. Production gitops.Commit only commits;
	// the claim package writes ops directly via op.ProbeAndWrite (skipping
	// WriteOpAndAutoCommit's StageOpFile), so we must stage the ops dir
	// before the commit fires. Under Phase 1 the wrapper's cwd is the
	// nested .act/ working tree so the staging path is plain "ops".
	wrapped := &claimGitOps{inner: gops, branch: opts.Branch}

	res, err := claim.RunClaim(repoRoot, full, claim.Options{
		Assignee:    cfg.NodeID, // assignee defaults to the local node id
		Wait:        opts.Wait,
		WaitTimeout: wait,
		Isolated:    opts.Isolated,
		Push:        opts.Push,
	}, clock, wrapped)
	if err != nil {
		// Hard failure: drift / write / pull-rebase / commit. These surface
		// as exit 1 (logical) per §5.C.3 + spec §3 update. Per spec §error-
		// envelope, raw subprocess stderr does NOT belong in `message`; we
		// extract the trailing `(output: ...)` blob (set by gitops/claim's
		// runGit wrapper) into `details.stderr_tail` so JSON consumers get
		// a clean human message and a separate diagnostic field.
		message, tail := SplitWrappedError(err.Error())
		details := map[string]any{}
		if tail != "" {
			details["stderr_tail"] = CaptureStderrTail(tail)
		}
		return UpdateErrorOutput{
			Error:   "claim_failed",
			Message: message,
			Details: details,
		}, 1
	}
	if res.Claimed {
		// Refresh the live SQLite index so doctor's index-divergence check
		// passes immediately after a successful claim. Loss path skips the
		// refresh because no op was written.
		if rerr := RefreshIndexForIssue(paths, res.IssueID); rerr != nil {
			return UpdateErrorOutput{
				Error:   "index_update_failed",
				Message: rerr.Error(),
			}, 1
		}
		return UpdateClaimResult{
			OK:         true,
			Claimed:    true,
			ID:         res.IssueID,
			Winner:     res.Winner,
			OpsWritten: []string{"claim"},
		}, 0
	}
	// Claim loss: exit 5 with envelope error `claim_lost` per spec
	// §error-envelope (the universal exit-code table) and §3 `act update`.
	// The structured fields (claimed:false, winner, reason:"lost-race")
	// are preserved so JSON consumers keep the winner id and the
	// lost-race detail; Error carries the canonical slug.
	return UpdateClaimResult{
		OK:      false,
		Claimed: false,
		ID:      res.IssueID,
		Winner:  res.Winner,
		Error:   ErrClaimLost,
		Reason:  "lost-race",
	}, 5
}

// splitDepRm parses a "--dep-rm" argument into (id, edge_type). The
// accepted forms are:
//
//	"<id>"                 → edge_type defaults to "blocks"
//	"<id>:<edge_type>"     → explicit edge_type
//
// IDs themselves contain a hyphen but no colon, so the colon split is
// unambiguous.
func splitDepRm(raw string) (id, edgeType string) {
	if i := strings.IndexByte(raw, ':'); i >= 0 {
		return raw[:i], raw[i+1:]
	}
	return raw, "blocks"
}

// resolveDepIDForUpdate is the local mirror of resolveDepID (depadd.go),
// re-implemented here so the error envelope shape (`UpdateErrorOutput`)
// matches the rest of `act update`.
func resolveDepIDForUpdate(arg string, knownIDs []string) (string, int, any) {
	full, rerr := ids.Resolve(arg, knownIDs)
	if rerr == nil {
		return full, 0, nil
	}
	if errors.Is(rerr, ids.ErrNotFound) {
		return "", 3, UpdateErrorOutput{
			Error:   "issue_not_found",
			Message: fmt.Sprintf("act update: --dep-rm %q: no matching id", arg),
			Details: map[string]any{"query": arg},
		}
	}
	var amb *ids.ErrAmbiguousID
	if errors.As(rerr, &amb) {
		candidates := amb.Candidates()
		// Exit 2 (usage): see resolve_helpers.go for the spec rationale.
		return "", 2, UpdateErrorOutput{
			Error:   "id_ambiguous",
			Message: fmt.Sprintf("act update: --dep-rm %q matches %d issues", arg, len(candidates)),
			Details: map[string]any{
				"prefix":     arg,
				"candidates": candidates,
			},
			Candidates: candidates,
		}
	}
	return "", 3, UpdateErrorOutput{
		Error:   "issue_not_found",
		Message: rerr.Error(),
		Details: map[string]any{"query": arg},
	}
}

// parentChainReaches climbs the parent hierarchy from `start` and reports
// whether it reaches `target`. Used to reject `act update --parent` edges
// that would close a cycle: if the proposed parent already has `target`
// somewhere above it, making `target`'s parent the proposed one loops.
//
// The returned path is the chain walked (target first, so it reads
// child -> parent -> ... -> back to child) for the error message. The
// walk carries a visited set, so a pre-existing cycle in the store
// terminates rather than spinning.
func parentChainReaches(rows []index.Row, start, target string) ([]string, bool) {
	parentOf := make(map[string]string, len(rows))
	for _, r := range rows {
		if r.Parent != "" {
			parentOf[r.ID] = r.Parent
		}
	}
	path := []string{ShortIssueID(target)}
	seen := map[string]bool{}
	for cur := start; cur != ""; cur = parentOf[cur] {
		if seen[cur] {
			return nil, false
		}
		seen[cur] = true
		path = append(path, ShortIssueID(cur))
		if cur == target {
			return path, true
		}
	}
	return nil, false
}

// depEdgeExists reports whether (childID --[edgeType]--> parentID) is a
// live edge in the folded index `rows`.
func depEdgeExists(rows []index.Row, childID, parentID, edgeType string) bool {
	for _, r := range rows {
		if r.ID != childID {
			continue
		}
		for _, d := range r.Deps {
			if d.Parent == parentID && d.EdgeType == edgeType {
				return true
			}
		}
		return false
	}
	return false
}

// FormatUpdateHuman renders an UpdateResult as a single human-friendly
// line; the trailing newline is included so callers can pipe directly to
// stdout. For claim results, FormatUpdateClaimHuman is used instead.
func FormatUpdateHuman(res UpdateResult) string {
	verb := "wrote"
	if !res.Committed {
		verb = "staged"
	}
	if res.OpsWritten == 1 {
		return fmt.Sprintf("Updated %s (%s 1 op)\n", res.ID, verb)
	}
	return fmt.Sprintf("Updated %s (%s %d ops)\n", res.ID, verb, res.OpsWritten)
}

// claimGitOps wraps a production *gitops.ActGitOps so its Commit method
// stages `.act/ops` first. The claim package writes the new op file
// directly to disk (bypassing WriteOpAndAutoCommit's explicit StageOpFile
// call), so without this wrapper the subsequent `git commit` finds an
// empty index and fails. PullRebase / Push pass through unchanged.
type claimGitOps struct {
	inner *gitops.ActGitOps
	// branch, when non-empty, names the nested .act/ branch the claim
	// auto-commit lands on and the optional --push targets on origin
	// (act-5d6a). Empty preserves the historical HEAD/tracking-config
	// behavior.
	branch string
}

func (c *claimGitOps) Commit(message string) error {
	// act-5d6a: switch the nested repo to --branch <ref> (creating if
	// missing) before staging so the claim commit lands on that branch.
	// EnsureBranch is a no-op when c.branch is empty.
	if err := c.inner.EnsureBranch(c.branch); err != nil {
		return fmt.Errorf("claimGitOps: ensure branch: %w", err)
	}
	// Stage the entire ops/ subtree so newly-written op files (and the
	// corresponding shard directories) are picked up. The path is plain
	// "ops" because under Phase 1 the wrapper's cwd is the nested .act/
	// working tree; "ops" inside it resolves to <hostRoot>/.act/ops, the
	// directory the op writer actually lays files into.
	//
	// Route the stage through c.inner.StageOpFile (which is `git add -- ops`
	// via *gitops.ActGitOps.run) so it inherits the SAME runner seam AND
	// --git-dir/--work-tree override every other act-state mutation uses.
	// A prior inline `git add` here (claimGitOps.runGit) shelled out with
	// only cmd.Dir set, so in a worktree context git's cwd-discovery could
	// walk up and stage into the WRONG repo's index (act-784b class — see
	// act-f64d6e). StageOpFile pins it to the nested .act/.git.
	if err := c.inner.StageOpFile("ops"); err != nil {
		return fmt.Errorf("claimGitOps: stage ops: %w", err)
	}
	return c.inner.Commit(message)
}

func (c *claimGitOps) PullRebase() error { return c.inner.PullRebase() }
func (c *claimGitOps) Push() error       { return c.inner.PushToBranch(c.branch) }

// FormatUpdateClaimHuman renders an UpdateClaimResult as a single
// human-friendly line.
func FormatUpdateClaimHuman(res UpdateClaimResult) string {
	if res.Claimed {
		return fmt.Sprintf("Claimed %s (winner=%s)\n", res.ID, res.Winner)
	}
	return fmt.Sprintf("Lost claim race for %s (winner=%s)\n", res.ID, res.Winner)
}

// appendDescription joins an existing description and an appended
// fragment with exactly one blank line between them (act-a79d66).
//
// Trailing whitespace on the existing body is trimmed first so repeated
// appends produce a uniform one-blank-line separator rather than an
// accumulating gap that depends on whether the previous author ended
// their text with a newline. An empty (or whitespace-only) existing
// description yields the fragment alone, with no leading blank lines.
func appendDescription(existing, addition string) string {
	existing = strings.TrimRight(existing, "\n\r\t ")
	if existing == "" {
		return addition
	}
	return existing + "\n\n" + addition
}
