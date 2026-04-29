package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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

// execCommand is the indirection used by claimGitOps for `git add`.
// Aliased here so a future build-tag can replace it for hermetic tests.
var execCommand = exec.Command

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

	// Mode flags.
	Claim       bool
	Wait        bool
	WaitTimeout time.Duration

	// Universal write flags.
	Push     bool
	NoCommit bool
	Isolated bool
	AsJSON   bool
	Verify   bool
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

// UpdateErrorOutput is the structured failure envelope.
type UpdateErrorOutput struct {
	Error   string `json:"error"`
	Message string `json:"message"`
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
			}, 3
		}
		var amb *ids.ErrAmbiguousID
		if errors.As(rerr, &amb) {
			return UpdateErrorOutput{
				Error:   "ambiguous_id",
				Message: rerr.Error(),
			}, 2
		}
		return UpdateErrorOutput{
			Error:   "issue_not_found",
			Message: rerr.Error(),
		}, 3
	}

	// Step 4: claim mode dispatch.
	if opts.Claim {
		return runUpdateClaim(repoRoot, full, opts)
	}

	// Step 5: non-claim mutation. We must have at least one mutating flag.
	if opts.Status == nil && opts.Priority == nil && opts.Assignee == nil && opts.Description == nil && len(opts.Accept) == 0 && len(opts.DepRm) == 0 {
		return UpdateErrorOutput{
			Error:   "bad_flag",
			Message: "act update: at least one of --status, --priority, --assignee, --description, --accept, --dep-rm, or --claim must be supplied",
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

	// Step 8: write each op via WriteOpAndAutoCommit. Each call performs
	// its own auto-commit; so we end up with N commits when N flags were
	// supplied. (The acceptance criterion calling for "single commit"
	// across multiple flags belongs to a future refactor; for now each
	// op is a separate commit, which still satisfies the JSON contract:
	// committed=true means at least one commit happened.)
	var gops *gitops.GitOps
	if !opts.NoCommit {
		gops = gitops.NewGitOps(repoRoot)
		gops.Verify = opts.Verify
	}
	for i, env := range envelopes {
		werr := WriteOpAndAutoCommit(env, bodies[i], paths, gops, WriteOpts{
			NoCommit: opts.NoCommit,
			// Defer the push to AFTER all ops are written so we don't push
			// a partial state mid-batch.
			Push:     false,
			Isolated: opts.Isolated,
		})
		if werr != nil {
			if errors.Is(werr, ErrInvalidFlags) {
				return UpdateErrorOutput{
					Error:   "bad_flag",
					Message: werr.Error(),
				}, 2
			}
			return UpdateErrorOutput{
				Error:   "write_failed",
				Message: werr.Error(),
			}, 1
		}
	}

	// Step 9: optional push (after all ops committed).
	if opts.Push && gops != nil {
		if perr := gops.Push(); perr != nil {
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

	gops := gitops.NewGitOps(repoRoot)
	gops.Verify = opts.Verify

	// claim's GitOps interface comment ("Commit stages the .act/ops subtree
	// and creates a single commit") is satisfied by wrapping the production
	// gitops with a staging step. Production gitops.Commit only commits;
	// the claim package writes ops directly via op.ProbeAndWrite (skipping
	// WriteOpAndAutoCommit's StageOpFile), so we must stage `.act/ops`
	// before the commit fires.
	wrapped := &claimGitOps{inner: gops, repoRoot: repoRoot}

	res, err := claim.RunClaim(repoRoot, full, claim.Options{
		Assignee:    cfg.NodeID, // assignee defaults to the local node id
		Wait:        opts.Wait,
		WaitTimeout: wait,
		Isolated:    opts.Isolated,
		Push:        opts.Push,
	}, clock, wrapped)
	if err != nil {
		// Hard failure: drift / write / pull-rebase / commit. These
		// surface as exit 1 (logical) per §5.C.3 + spec §3 update.
		return UpdateErrorOutput{
			Error:   "claim_failed",
			Message: err.Error(),
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
		}
	}
	var amb *ids.ErrAmbiguousID
	if errors.As(rerr, &amb) {
		return "", 2, UpdateErrorOutput{
			Error:   "ambiguous_id",
			Message: fmt.Sprintf("act update: --dep-rm %q: %s", arg, rerr.Error()),
		}
	}
	return "", 3, UpdateErrorOutput{
		Error:   "issue_not_found",
		Message: rerr.Error(),
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

// claimGitOps wraps a production *gitops.GitOps so its Commit method
// stages `.act/ops` first. The claim package writes the new op file
// directly to disk (bypassing WriteOpAndAutoCommit's explicit StageOpFile
// call), so without this wrapper the subsequent `git commit` finds an
// empty index and fails. PullRebase / Push pass through unchanged.
type claimGitOps struct {
	inner    *gitops.GitOps
	repoRoot string
}

func (c *claimGitOps) Commit(message string) error {
	// Stage the entire .act/ops subtree so newly-written op files (and
	// the corresponding shard directories) are picked up.
	if _, err := c.runGit("add", "--", ".act/ops"); err != nil {
		return fmt.Errorf("claimGitOps: stage .act/ops: %w", err)
	}
	return c.inner.Commit(message)
}

func (c *claimGitOps) PullRebase() error { return c.inner.PullRebase() }
func (c *claimGitOps) Push() error       { return c.inner.Push() }

// runGit is a tiny inline shellout used only by claimGitOps.Commit. We
// don't import os/exec at package scope just for this wrapper, so we go
// through the inner *gitops.GitOps' own runner indirection by calling a
// known no-op-on-success command. The simplest path is to add `git add`
// here directly.
func (c *claimGitOps) runGit(args ...string) (string, error) {
	cmd := execCommand("git", args...)
	cmd.Dir = c.repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// FormatUpdateClaimHuman renders an UpdateClaimResult as a single
// human-friendly line.
func FormatUpdateClaimHuman(res UpdateClaimResult) string {
	if res.Claimed {
		return fmt.Sprintf("Claimed %s (winner=%s)\n", res.ID, res.Winner)
	}
	return fmt.Sprintf("Lost claim race for %s (winner=%s)\n", res.ID, res.Winner)
}
