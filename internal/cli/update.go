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

	// Repeatables.
	Accept []string
	DepRm  []string
	// ExtAdd is the list of opaque external-tracker refs to attach as
	// blocking external dependencies. Each entry generates one
	// add_external_dep op. Re-adding a ref already on the issue is a no-op
	// at the apply layer.
	ExtAdd []string
	// ExtRm is the list of opaque refs to clear. Each entry generates one
	// remove_external_dep op. Clearing a not-present ref is a no-op — the
	// orchestrator owns the lifecycle and may double-fire safely.
	ExtRm []string

	// Mode flags.
	Claim       bool
	Wait        bool
	WaitTimeout time.Duration

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
type UpdateClaimResult struct {
	OK         bool     `json:"ok"`
	Claimed    bool     `json:"claimed"`
	ID         string   `json:"id"`
	Winner     string   `json:"winner"`
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
// `--claim` (§5.B.3).
var validUpdateStatuses = map[string]bool{
	"open":    true,
	"blocked": true,
}

// RunUpdate implements `act update <id>`.
//
// Returns:
//   - output: UpdateResult on non-claim success, UpdateClaimResult on
//     claim, UpdateErrorOutput on failure.
//   - exitCode: 0 success / claim win; 1 claim loss or logical failure;
//     2 bad flags / forbidden combinations; 3 missing repo / unknown id.
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
		if len(opts.Accept) > 0 {
			conflicts = append(conflicts, "--accept")
		}
		if len(opts.DepRm) > 0 {
			conflicts = append(conflicts, "--dep-rm")
		}
		if len(opts.ExtAdd) > 0 {
			conflicts = append(conflicts, "--ext-add")
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
	if opts.Status == nil && opts.Priority == nil && opts.Assignee == nil && opts.Description == nil && len(opts.Accept) == 0 && len(opts.DepRm) == 0 && len(opts.ExtAdd) == 0 && len(opts.ExtRm) == 0 {
		return UpdateErrorOutput{
			Error:   "bad_flag",
			Message: "act update: at least one of --status, --priority, --assignee, --description, --accept, --dep-rm, --ext-add, --ext-rm, or --claim must be supplied",
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

	var rows []index.Row
	if len(opts.DepRm) > 0 {
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
		val, _ := json.Marshal(*opts.Status)
		if errOut, code := addOp("update_field", op.UpdateFieldPayload{Field: "status", Value: val}); code != 0 {
			return errOut, code
		}
	}
	if opts.Priority != nil {
		val, _ := json.Marshal(*opts.Priority)
		if errOut, code := addOp("update_field", op.UpdateFieldPayload{Field: "priority", Value: val}); code != 0 {
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
	for _, c := range opts.Accept {
		if errOut, code := addOp("add_accept", op.AddAcceptPayload{Criterion: c}); code != 0 {
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
				Error:   "dep_not_found",
				Message: fmt.Sprintf("act update: --dep-rm: edge %s --[%s]--> %s does not exist", full, edgeType, parentFull),
			}, 1
		}
		if errOut, code := addOp("remove_dep", op.RemoveDepPayload{Parent: parentFull, EdgeType: edgeType}); code != 0 {
			return errOut, code
		}
	}
	// External-dep adds. Each generates one add_external_dep op. The apply
	// layer is the idempotency boundary, so re-adding an already-present ref
	// is a no-op at fold time but still writes a fresh op file. We do not
	// short-circuit here; producing the op preserves the audit trail and
	// matches the wire-level contract for the orchestrator. Payload-level
	// validation (empty, length cap, control chars) is gated up-front so a
	// bad ref fails the entire update before any op hits disk.
	for _, ref := range opts.ExtAdd {
		pl := op.AddExternalDepPayload{Ref: ref}
		if verr := pl.Validate(); verr != nil {
			return UpdateErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act update: --ext-add: %v", verr),
			}, 2
		}
		if errOut, code := addOp("add_external_dep", pl); code != 0 {
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
// On loss (Claimed == false): exit 1 with the loss envelope.
// On hard error (drift / write / commit / pull-rebase): exit 1 with an
// UpdateErrorOutput.
func runUpdateClaim(repoRoot, full string, opts UpdateOptions) (any, int) {
	paths := config.Layout(repoRoot)
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
	return UpdateClaimResult{
		OK:      false,
		Claimed: false,
		ID:      res.IssueID,
		Winner:  res.Winner,
		Reason:  "lost-race",
	}, 1
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
