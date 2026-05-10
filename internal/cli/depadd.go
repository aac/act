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

// DepAddOptions captures the flag knobs for `act dep add`.
//
// Per spec §3 `act dep add`: positional <child> and <parent>, plus
// `--type T`, `--json`, and the universal write flags. EdgeType empty
// is normalised to "blocks" before validation.
type DepAddOptions struct {
	// Child is the issue id (full or prefix) on the child side of the
	// edge — i.e. the issue whose deps[] grows by one entry.
	Child string
	// Parent is the issue id (full or prefix) on the parent side of the
	// edge — i.e. the target referenced from the child's deps[].
	Parent string
	// EdgeType is one of {blocks, relates, supersedes}. Empty defaults
	// to "blocks".
	EdgeType string
	// AsJSON toggles the JSON envelope rendering (mirrors other write
	// commands; the cli return shape is identical regardless).
	AsJSON bool
	// NoCommit, Push, Isolated mirror the universal write flags.
	NoCommit bool
	Push     bool
	Isolated bool
}

// DepAddResult is the JSON-serialisable success envelope.
//
// Field ordering follows spec §`act dep add`:
//
//	{"ok":true,"child":"...","parent":"...","type":"...","committed":true}
type DepAddResult struct {
	OK        bool   `json:"ok"`
	Child     string `json:"child"`
	Parent    string `json:"parent"`
	EdgeType  string `json:"type"`
	Committed bool   `json:"committed"`
}

// DepAddErrorOutput is the structured failure envelope (non-cycle).
// Candidates is non-nil only on the id_ambiguous path; it is also mirrored
// under Details["candidates"] so the on-the-wire JSON envelope matches spec
// §"Errors" (`details.candidates[]`).
type DepAddErrorOutput struct {
	Error      string         `json:"error"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	Candidates []string       `json:"-"`
}

// DepAddCycleOutput is the structured cycle failure envelope. The path
// includes the proposed new edge (so it ends with the child id again).
type DepAddCycleOutput struct {
	Error DepAddCycleError `json:"error"`
}

// DepAddCycleError is the inner shape of the cycle error envelope.
type DepAddCycleError struct {
	Kind string   `json:"kind"`
	Path []string `json:"path"`
}

// validDepAddEdgeTypes mirrors op.validEdgeTypes, replicated locally to
// avoid exporting that package-private symbol.
var validDepAddEdgeTypes = map[string]bool{
	"blocks":     true,
	"relates":    true,
	"supersedes": true,
}

// RunDepAdd implements `act dep add`.
//
// Steps:
//  1. Require a git working tree + initialised .act/. Missing → exit 3.
//  2. Resolve <child> and <parent> via the prefix pipeline. Ambiguous
//     or self-edge → exit 2; unknown id → exit 3.
//  3. Validate --type. Empty defaults to "blocks"; anything else is exit 2.
//  4. Cycle check (only for type=blocks): build the directed `blocks`
//     graph from the folded index, add the proposed edge, run a DFS
//     from child looking for child reachable from itself. On hit:
//     exit 1 with {"error":{"kind":"cycle","path":[...]}}.
//  5. Dedup per §5.C.5: if the (child, parent, edge_type) triple is
//     already live in the folded index, return idempotent exit 0 with
//     committed=false (no op file written).
//  6. Build and write the add_dep envelope; auto-commit unless --no-commit.
//  7. Emit the success envelope with committed=!opts.NoCommit.
//
// Returns:
//   - output: DepAddResult on success, DepAddErrorOutput on bad-flag /
//     not-found, DepAddCycleOutput on cycle.
//   - exitCode: 0 success or idempotent dedup; 1 cycle / write failure;
//     2 bad flags / self-edge / ambiguous prefix; 3 missing repo /
//     missing .act/ / unknown id.
func RunDepAdd(repoRoot string, opts DepAddOptions) (output any, exitCode int) {
	// Step 1: repo + .act/ required.
	if !hasGitDir(repoRoot) {
		return DepAddErrorOutput{
			Error:   "not_in_git",
			Message: fmt.Sprintf("act dep add: %s is not inside a git working tree", repoRoot),
		}, 3
	}
	paths := config.Layout(repoRoot)
	if _, err := os.Stat(paths.ConfigJSON); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DepAddErrorOutput{
				Error:   "act_not_initialized",
				Message: fmt.Sprintf("act dep add: %s/.act not initialized; run `act init` first", repoRoot),
			}, 3
		}
		return DepAddErrorOutput{
			Error:   "act_not_initialized",
			Message: fmt.Sprintf("act dep add: stat %s: %v", paths.ConfigJSON, err),
		}, 3
	}

	// Step 2a: validate edge type before id resolution so a bad --type
	// is reported (exit 2) regardless of whether the ids exist.
	edgeType := opts.EdgeType
	if edgeType == "" {
		edgeType = "blocks"
	}
	if !validDepAddEdgeTypes[edgeType] {
		return DepAddErrorOutput{
			Error:   "bad_flag",
			Message: fmt.Sprintf("act dep add: --type %q: must be one of blocks|relates|supersedes", edgeType),
		}, 2
	}

	// Step 2b: positional args required.
	if opts.Child == "" || opts.Parent == "" {
		return DepAddErrorOutput{
			Error:   "bad_flag",
			Message: "act dep add: <child> and <parent> are required",
		}, 2
	}

	// Step 2c: universal-write-flag conflicts (per spec §4).
	if opts.NoCommit && opts.Push {
		return DepAddErrorOutput{
			Error:   "bad_flag",
			Message: "act dep add: --no-commit and --push are mutually exclusive",
		}, 2
	}
	if opts.Isolated && opts.Push {
		return DepAddErrorOutput{
			Error:   "bad_flag",
			Message: "act dep add: --isolated and --push are mutually exclusive",
		}, 2
	}

	// Step 2d: enumerate known full ids, then resolve child + parent.
	knownIDs, err := listIssueIDs(paths.Ops)
	if err != nil {
		return DepAddErrorOutput{
			Error:   "ops_scan_failed",
			Message: err.Error(),
		}, 1
	}

	childFull, code, errOut := resolveDepID(opts.Child, "child", knownIDs)
	if code != 0 {
		return errOut, code
	}
	parentFull, code, errOut := resolveDepID(opts.Parent, "parent", knownIDs)
	if code != 0 {
		return errOut, code
	}

	// Step 2e: self-edge → exit 2.
	if childFull == parentFull {
		return DepAddErrorOutput{
			Error:   "bad_flag",
			Message: fmt.Sprintf("act dep add: self-edge not permitted (%s)", childFull),
		}, 2
	}

	// Step 3: build / open the index and rebuild for freshness. The
	// rebuild reflects all on-disk ops, including any that were written
	// after this process started.
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		return DepAddErrorOutput{
			Error:   "index_open_failed",
			Message: err.Error(),
		}, 1
	}
	defer func() { _ = idx.Close() }()
	if err := idx.Rebuild(paths.Ops); err != nil {
		return DepAddErrorOutput{
			Error:   "index_rebuild_failed",
			Message: err.Error(),
		}, 1
	}

	// Pull every row; we use this both for cycle detection and dedup.
	rows, err := idx.ListAll(index.Filter{})
	if err != nil {
		return DepAddErrorOutput{
			Error:   "index_query_failed",
			Message: err.Error(),
		}, 1
	}

	// Step 4: cycle check (blocks only).
	if edgeType == "blocks" {
		// Build a directed blocks graph keyed by child → []parent.
		// Adding an edge child→parent is a cycle iff parent can already
		// reach child via existing blocks edges.
		adj := make(map[string][]string, len(rows))
		for _, r := range rows {
			for _, d := range r.Deps {
				if d.EdgeType != "blocks" {
					continue
				}
				adj[r.ID] = append(adj[r.ID], d.Parent)
			}
		}
		// DFS from parent; if we reach child, the proposed edge closes
		// a cycle. The reported path goes child → parent → ... → child.
		path, found := dfsBlocksPath(adj, parentFull, childFull)
		if found {
			full := append([]string{childFull}, path...)
			return DepAddCycleOutput{
				Error: DepAddCycleError{
					Kind: "cycle",
					Path: full,
				},
			}, 1
		}
	}

	// Step 5: dedup per §5.C.5. The dedup key is (child, parent, edge_type).
	// If the triple is already in the folded index, no op is written and
	// the call returns success (committed=false because nothing happened).
	for _, r := range rows {
		if r.ID != childFull {
			continue
		}
		for _, d := range r.Deps {
			if d.Parent == parentFull && d.EdgeType == edgeType {
				return DepAddResult{
					OK:        true,
					Child:     childFull,
					Parent:    parentFull,
					EdgeType:  edgeType,
					Committed: false,
				}, 0
			}
		}
		break
	}

	// Step 6: build the add_dep envelope and write/auto-commit.
	cfg, cerr := config.ReadConfig(paths)
	if cerr != nil {
		return DepAddErrorOutput{
			Error:   "config_read_failed",
			Message: cerr.Error(),
		}, 1
	}

	payload := op.AddDepPayload{
		Parent:   parentFull,
		EdgeType: edgeType,
	}
	if verr := payload.Validate(); verr != nil {
		return DepAddErrorOutput{
			Error:   "payload_invalid",
			Message: verr.Error(),
		}, 1
	}
	bodyPayload, perr := canonicaljson.Marshal(payload)
	if perr != nil {
		return DepAddErrorOutput{
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
		OpType:        "add_dep",
		IssueID:       childFull,
		Payload:       bodyPayload,
		HLC:           stamp,
		NodeID:        cfg.NodeID,
	}
	if verr := env.Validate(); verr != nil {
		return DepAddErrorOutput{
			Error:   "envelope_invalid",
			Message: verr.Error(),
		}, 1
	}
	body, merr := env.Marshal()
	if merr != nil {
		return DepAddErrorOutput{
			Error:   "marshal_failed",
			Message: merr.Error(),
		}, 1
	}

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
			return DepAddErrorOutput{
				Error:   "bad_flag",
				Message: werr.Error(),
			}, 2
		}
		return DepAddErrorOutput{
			Error:   "write_failed",
			Message: werr.Error(),
		}, 1
	}

	// Refresh the live SQLite index for the child issue so doctor's
	// index-divergence check passes immediately after a successful
	// dep add. The earlier idx handle is already closed by the deferred
	// close above; RefreshIndexForIssue opens a fresh handle.
	if err := RefreshIndexForIssue(paths, childFull); err != nil {
		return DepAddErrorOutput{
			Error:   "index_update_failed",
			Message: err.Error(),
		}, 1
	}

	// Step 7: success envelope. committed mirrors !NoCommit.
	return DepAddResult{
		OK:        true,
		Child:     childFull,
		Parent:    parentFull,
		EdgeType:  edgeType,
		Committed: !opts.NoCommit,
	}, 0
}

// resolveDepID resolves a single id (child or parent) via the prefix
// pipeline. Returns the full id on success, or the appropriate error
// envelope + exit code (2 for ambiguous, 3 for not-found) on failure.
func resolveDepID(arg, label string, knownIDs []string) (string, int, any) {
	full, rerr := ids.Resolve(arg, knownIDs)
	if rerr == nil {
		return full, 0, nil
	}
	if errors.Is(rerr, ids.ErrNotFound) {
		return "", 3, DepAddErrorOutput{
			Error:   "issue_not_found",
			Message: fmt.Sprintf("act dep add: %s %q: no matching id", label, arg),
			Details: map[string]any{"query": arg},
		}
	}
	var amb *ids.ErrAmbiguousID
	if errors.As(rerr, &amb) {
		candidates := amb.Candidates()
		// Exit 2 (usage): see resolve_helpers.go for the spec rationale.
		return "", 2, DepAddErrorOutput{
			Error:   "id_ambiguous",
			Message: fmt.Sprintf("act dep add: %s %q matches %d issues", label, arg, len(candidates)),
			Details: map[string]any{
				"prefix":     arg,
				"candidates": candidates,
			},
			Candidates: candidates,
		}
	}
	return "", 3, DepAddErrorOutput{
		Error:   "issue_not_found",
		Message: rerr.Error(),
		Details: map[string]any{"query": arg},
	}
}

// dfsBlocksPath runs a depth-first search from src looking for dst in
// the directed adjacency map adj. On hit, returns the path from src to
// dst (inclusive of both endpoints). The caller prepends the child id
// to form the full cycle path: child → src → ... → dst (== child).
func dfsBlocksPath(adj map[string][]string, src, dst string) ([]string, bool) {
	visited := map[string]bool{}
	var dfs func(node string, stack []string) ([]string, bool)
	dfs = func(node string, stack []string) ([]string, bool) {
		if visited[node] {
			return nil, false
		}
		visited[node] = true
		stack = append(stack, node)
		if node == dst {
			out := make([]string, len(stack))
			copy(out, stack)
			return out, true
		}
		for _, next := range adj[node] {
			if path, ok := dfs(next, stack); ok {
				return path, true
			}
		}
		return nil, false
	}
	return dfs(src, nil)
}

// FormatDepAddHuman renders a DepAddResult as a single human-friendly
// line; the trailing newline is included so callers can pipe directly
// to stdout.
func FormatDepAddHuman(res DepAddResult) string {
	if !res.Committed {
		return fmt.Sprintf("Edge %s --[%s]--> %s already present (no op)\n", res.Child, res.EdgeType, res.Parent)
	}
	return fmt.Sprintf("Added %s --[%s]--> %s\n", res.Child, res.EdgeType, res.Parent)
}
