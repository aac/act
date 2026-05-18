package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/op"
)

// DeleteOptions captures the flag knobs for `act delete`.
//
// Per spec §3 `act delete <id>`: positional <id> with `--reason TEXT`,
// `--cascade`, `--json` plus the universal write flags. The op_type
// emitted is `tombstone` (issue-level deletion) per the spec's op-type
// table.
type DeleteOptions struct {
	// ID is the positional argument: the issue id (full or unique prefix)
	// to tombstone. Resolved via the standard prefix pipeline.
	ID string
	// Reason is an optional free-text rationale. Currently retained only
	// in the commit subject for human auditing; not persisted in the
	// tombstone payload (the spec's TombstonePayload carries deleted_at
	// only).
	Reason string
	// Cascade, when true, walks the parent edge and tombstones every
	// non-tombstoned descendant in a single git commit. Without this
	// flag, deleting an issue that has live descendants exits 1 with
	// has_descendants.
	Cascade bool
	// AsJSON toggles JSON envelope rendering. The cli return shape is
	// identical regardless; main.go decides how to render.
	AsJSON bool
	// NoCommit, Push, Isolated, Verify mirror the universal write flags.
	NoCommit bool
	Push     bool
	Isolated bool
	Verify   bool
}

// DeleteResult is the JSON-serialisable success envelope.
//
//	{"id":"...","short_id":"...","ops_written":N,"committed":true,
//	 "tombstoned":["act-...","act-..."]}
//
// Tombstoned lists every id that received a tombstone op in this run,
// sorted lexicographically.
type DeleteResult struct {
	ID         string   `json:"id"`
	ShortID    string   `json:"short_id"`
	OpsWritten int      `json:"ops_written"`
	Committed  bool     `json:"committed"`
	Tombstoned []string `json:"tombstoned"`
	Reason     string   `json:"reason,omitempty"`
}

// DeleteErrorOutput is the structured failure envelope. Candidates is
// non-nil only on the id_ambiguous path; it is also mirrored under
// Details["candidates"] so the on-the-wire JSON envelope matches spec
// §"Errors" (`details.candidates[]`). Details["descendants"] carries the
// id list for the has_descendants error.
type DeleteErrorOutput struct {
	Error      string         `json:"error"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	Candidates []string       `json:"-"`
}

// deleteReasonMaxBytes mirrors the close-reason cap at 4 KiB so an
// agent cannot stash an entire diff in the audit trail.
const deleteReasonMaxBytes = 4096

// RunDelete implements `act delete`.
//
// Steps:
//  1. Require a git working tree + initialised .act/. Missing → exit 3.
//  2. Resolve opts.ID via the prefix pipeline.
//  3. Fold the repo. Build a child→parent map across all live issues.
//  4. If the target is already tombstoned, return idempotent exit 0
//     with ops_written=0, committed=false.
//  5. Find non-tombstoned children of the target. If any exist and
//     !Cascade: return has_descendants exit 1 with details.descendants.
//  6. Build tombstone envelopes for the target plus any descendants
//     (recursively, on --cascade). A single git commit batches all of
//     them.
//  7. Emit DeleteResult on success.
//
// Returns:
//   - output: DeleteResult on success, DeleteErrorOutput on failure.
//   - exitCode: 0 success or already-tombstoned no-op; 1 has_descendants
//     without --cascade / write failure; 2 bad flags / ambiguous prefix /
//     reason too long; 3 missing repo / missing .act/ / unknown id.
func RunDelete(repoRoot string, opts DeleteOptions) (output any, exitCode int) {
	// Step 1: repo + .act/ required.
	if !hasGitDir(repoRoot) {
		return DeleteErrorOutput{
			Error:   "not_in_git",
			Message: fmt.Sprintf("act delete: %s is not inside a git working tree", repoRoot),
		}, 3
	}
	paths := config.Layout(repoRoot)
	if _, err := os.Stat(paths.ConfigJSON); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DeleteErrorOutput{
				Error:   "act_not_initialized",
				Message: fmt.Sprintf("act delete: %s/.act not initialized; run `act init` first", repoRoot),
			}, 3
		}
		return DeleteErrorOutput{
			Error:   "act_not_initialized",
			Message: fmt.Sprintf("act delete: stat %s: %v", paths.ConfigJSON, err),
		}, 3
	}

	// Step 2a: positional arg required.
	if opts.ID == "" {
		return DeleteErrorOutput{
			Error:   "bad_flag",
			Message: "act delete: <id> is required",
		}, 2
	}
	// Step 2b: reason length cap.
	if len(opts.Reason) > deleteReasonMaxBytes {
		return DeleteErrorOutput{
			Error:   "bad_flag",
			Message: fmt.Sprintf("act delete: --reason length %d > %d bytes", len(opts.Reason), deleteReasonMaxBytes),
		}, 2
	}
	// Step 2c: universal-write-flag conflicts (per spec §4).
	if opts.NoCommit && opts.Push {
		return DeleteErrorOutput{
			Error:   "bad_flag",
			Message: "act delete: --no-commit and --push are mutually exclusive",
		}, 2
	}
	if opts.Isolated && opts.Push {
		return DeleteErrorOutput{
			Error:   "bad_flag",
			Message: "act delete: --isolated and --push are mutually exclusive",
		}, 2
	}

	// Step 2d: enumerate known ids and resolve the target.
	knownIDs, err := listIssueIDs(paths.Ops)
	if err != nil {
		return DeleteErrorOutput{
			Error:   "ops_scan_failed",
			Message: err.Error(),
		}, 1
	}
	full, rerr := ids.Resolve(opts.ID, knownIDs)
	if rerr != nil {
		if errors.Is(rerr, ids.ErrNotFound) {
			return DeleteErrorOutput{
				Error:   "issue_not_found",
				Message: fmt.Sprintf("act delete: %q: no matching id", opts.ID),
				Details: map[string]any{"query": opts.ID},
			}, 3
		}
		var amb *ids.ErrAmbiguousID
		if errors.As(rerr, &amb) {
			candidates := amb.Candidates()
			// Exit 2 (usage): see resolve_helpers.go for the spec rationale.
			return DeleteErrorOutput{
				Error:   "id_ambiguous",
				Message: fmt.Sprintf("act delete: prefix %q matches %d issues", opts.ID, len(candidates)),
				Details: map[string]any{
					"prefix":     opts.ID,
					"candidates": candidates,
				},
				Candidates: candidates,
			}, 2
		}
		return DeleteErrorOutput{
			Error:   "issue_not_found",
			Message: rerr.Error(),
			Details: map[string]any{"query": opts.ID},
		}, 3
	}

	// Step 3: full fold so we can see every issue's parent field and
	// tombstone state. Tombstoned issues never appear as live descendants
	// even if their parent points at the target.
	fr, ferr := fold.Fold(paths.Ops, fold.ApplyDispatch)
	if ferr != nil {
		return DeleteErrorOutput{
			Error:   "fold_failed",
			Message: ferr.Error(),
		}, 1
	}

	// Step 4: idempotent already-tombstoned no-op.
	if st, ok := fr.Issues[full]; ok && st != nil && st.Tombstoned {
		return DeleteResult{
			ID:         full,
			ShortID:    ShortIssueID(full),
			OpsWritten: 0,
			Committed:  false,
			Tombstoned: []string{full},
			Reason:     opts.Reason,
		}, 0
	}

	// Step 5: build a parent→[]child map across all live (non-tombstoned)
	// issues so we can walk descendants transitively.
	childrenOf := map[string][]string{}
	for id, st := range fr.Issues {
		if st == nil || st.Tombstoned {
			continue
		}
		parent, _ := st.Fields["parent"].(string)
		if parent == "" {
			continue
		}
		childrenOf[parent] = append(childrenOf[parent], id)
	}

	// Direct (immediate) live children determine the has_descendants
	// gate: if any exist and !Cascade, fail.
	directChildren := append([]string(nil), childrenOf[full]...)
	sort.Strings(directChildren)

	if len(directChildren) > 0 && !opts.Cascade {
		return DeleteErrorOutput{
			Error:   "has_descendants",
			Message: fmt.Sprintf("act delete: %s has %d non-tombstoned descendant(s); pass --cascade to also tombstone them", full, len(directChildren)),
			Details: map[string]any{
				"id":          full,
				"descendants": directChildren,
			},
		}, 1
	}

	// Step 6a: collect every issue id to tombstone. With --cascade we
	// recurse via BFS so a deep tree is fully covered. Without --cascade
	// we only tombstone the target (we already know it has no live
	// children at this point).
	var allTargets []string
	if opts.Cascade {
		allTargets = collectDescendantsBFS(full, childrenOf)
	} else {
		allTargets = []string{full}
	}
	// Deterministic order: target first, then descendants sorted
	// lexicographically so the tombstone op file timestamps are
	// reproducible across equivalent runs.
	rest := make([]string, 0, len(allTargets))
	for _, id := range allTargets {
		if id == full {
			continue
		}
		rest = append(rest, id)
	}
	sort.Strings(rest)
	ordered := append([]string{full}, rest...)

	// Step 6b: build envelopes.
	cfg, cerr := config.ReadConfig(paths)
	if cerr != nil {
		return DeleteErrorOutput{
			Error:   "config_read_failed",
			Message: cerr.Error(),
		}, 1
	}
	clock := hlc.NewClock(cfg.NodeID, func() int64 { return time.Now().UnixMilli() })

	envs := make([]op.Envelope, 0, len(ordered))
	bodies := make([][]byte, 0, len(ordered))
	for _, id := range ordered {
		stamp := clock.Send()
		stamp.NodeID = cfg.NodeID

		payload := op.TombstonePayload{
			DeletedAt: formatRFC3339Millis(stamp.Wall),
		}
		if verr := payload.Validate(); verr != nil {
			return DeleteErrorOutput{
				Error:   "payload_invalid",
				Message: verr.Error(),
			}, 1
		}
		bodyPayload, perr := canonicaljson.Marshal(payload)
		if perr != nil {
			return DeleteErrorOutput{
				Error:   "marshal_failed",
				Message: perr.Error(),
			}, 1
		}

		env := op.Envelope{
			OpVersion:     op.CurrentOpVersion,
			SchemaVersion: op.CurrentSchemaVersion,
			WriterVersion: op.WriterVersion,
			OpType:        "tombstone",
			IssueID:       id,
			Payload:       bodyPayload,
			HLC:           stamp,
			NodeID:        cfg.NodeID,
		}
		if verr := env.Validate(); verr != nil {
			return DeleteErrorOutput{
				Error:   "envelope_invalid",
				Message: verr.Error(),
			}, 1
		}
		body, merr := env.Marshal()
		if merr != nil {
			return DeleteErrorOutput{
				Error:   "marshal_failed",
				Message: merr.Error(),
			}, 1
		}
		envs = append(envs, env)
		bodies = append(bodies, body)
	}

	// Step 6c: write + (optionally) commit. Always batch through
	// WriteOpsAndAutoCommit (single op or many) so the rollback path is
	// a single helper and the commit subject format stays consistent.
	// Subject is built by BuildBatchCommitMessage on the *first* envelope
	// (the head of the cascade): canonical form is
	// `act-op: (act-XXXX) tombstone [+N]`. See act-d3a5.
	commitMsg := BuildBatchCommitMessage(envs[0], len(envs))

	var gops *gitops.ActGitOps
	if !opts.NoCommit {
		gops = gitops.NewActGitOps(repoRoot)
		gops.Verify = opts.Verify
	}

	werr := WriteOpsAndAutoCommit(envs, bodies, paths, gops, WriteOpts{
		NoCommit: opts.NoCommit,
		Push:     opts.Push,
		Isolated: opts.Isolated,
	}, commitMsg)
	if werr != nil {
		if errors.Is(werr, ErrInvalidFlags) {
			return DeleteErrorOutput{
				Error:   "bad_flag",
				Message: werr.Error(),
			}, 2
		}
		return DeleteErrorOutput{
			Error:   "write_failed",
			Message: werr.Error(),
		}, 1
	}

	// Step 6d: refresh the live SQLite index for every tombstoned id so
	// downstream readers (list/show/doctor) see the post-delete state
	// without requiring a full rebuild. RefreshIndexForIssue removes the
	// row for any tombstoned issue (per index.upsertTx).
	for _, id := range ordered {
		if err := RefreshIndexForIssue(paths, id); err != nil {
			return DeleteErrorOutput{
				Error:   "index_update_failed",
				Message: err.Error(),
			}, 1
		}
	}

	// Step 7: success envelope.
	tombstoned := append([]string(nil), ordered...)
	sort.Strings(tombstoned)
	return DeleteResult{
		ID:         full,
		ShortID:    ShortIssueID(full),
		OpsWritten: len(envs),
		Committed:  !opts.NoCommit,
		Tombstoned: tombstoned,
		Reason:     opts.Reason,
	}, 0
}

// formatRFC3339Millis renders unix-ms as the canonical RFC3339Millis
// form ("2006-01-02T15:04:05.000Z") used by tombstone payloads. Mirrors
// the unexported helper in internal/fold/apply.go so the wire shape
// matches the rest of the writer pipeline.
func formatRFC3339Millis(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// collectDescendantsBFS returns the target id plus every transitive
// non-tombstoned descendant reachable through the parent edge. The
// caller is responsible for filtering tombstoned issues out of the
// adjacency map before passing it in (childrenOf already does so).
func collectDescendantsBFS(root string, childrenOf map[string][]string) []string {
	seen := map[string]bool{root: true}
	out := []string{root}
	queue := []string{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range childrenOf[cur] {
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
			queue = append(queue, c)
		}
	}
	return out
}

// FormatDeleteHuman renders a DeleteResult as a single human-friendly
// line suitable for stdout. The trailing newline is included.
func FormatDeleteHuman(res DeleteResult) string {
	if res.OpsWritten == 0 {
		return fmt.Sprintf("Already deleted: %s\n", res.ID)
	}
	if len(res.Tombstoned) > 1 {
		return fmt.Sprintf("Deleted %s and %d descendant(s)\n", res.ShortID, len(res.Tombstoned)-1)
	}
	return fmt.Sprintf("Deleted %s\n", res.ShortID)
}
