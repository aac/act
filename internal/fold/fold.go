// Package fold implements the deterministic op-fold algorithm that materialises
// task state from the op log.
//
// This file is the orchestration shell: discover, parse, validate, sort, and
// dispatch ops to a per-op-type apply function. The per-op-type semantics live
// in act-c9f0; act-9362 supplies stub apply functions adequate for testing the
// dispatch wiring.
package fold

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// IssueState is the generic per-issue state container produced by fold.
//
// Per-field LWW tracking is exposed as LastHLC for use by act-296e. Apply
// functions are expected to write into Fields and update LastHLC entries
// keyed by field name.
type IssueState struct {
	ID         string
	Fields     map[string]any
	LastHLC    map[string]hlc.HLC
	Tombstoned bool
}

// FoldResult is the fold output: the materialised issue states plus the
// number of ops applied.
type FoldResult struct {
	Issues      map[string]*IssueState
	OpsConsumed int
}

// ApplyFunc applies a single op envelope to the given issue state. The payload
// bytes are passed alongside the envelope so apply functions can decode the
// per-op-type payload without re-marshaling.
type ApplyFunc func(state *IssueState, env op.Envelope, payload []byte) error

// sortedOp is the in-memory representation of a parsed op: the envelope, its
// canonical hash (used for tiebreaks in the global sort), and the source path
// for error reporting.
type sortedOp struct {
	env      op.Envelope
	fullHash string
	path     string
}

// newIssueState returns a fresh IssueState with empty maps and no tombstone.
func newIssueState(id string) *IssueState {
	return &IssueState{
		ID:      id,
		Fields:  map[string]any{},
		LastHLC: map[string]hlc.HLC{},
	}
}

// Fold walks rootOps, parses every op file, sorts globally by HLC then op_hash,
// groups by issue id, and applies each op via the dispatch returned by
// applyDispatch.
//
// Discovery: <rootOps>/<issue>/<yyyy-mm>/*.json. Files outside that pattern or
// with non-.json suffix are skipped. A missing rootOps yields an empty result.
func Fold(rootOps string, applyDispatch func(opType string) ApplyFunc) (*FoldResult, error) {
	ops, err := discoverAndParse(rootOps, "")
	if err != nil {
		return nil, err
	}
	sortOps(ops)
	return applyAll(ops, applyDispatch)
}

// FoldIssue folds a single issue subtree and returns its terminal state. If
// the issue has no ops on disk, FoldIssue returns a zero-op IssueState with
// the requested id (mirroring the lazy-init contract in the spec).
func FoldIssue(rootOps, issueID string, applyDispatch func(string) ApplyFunc) (*IssueState, error) {
	ops, err := discoverAndParse(rootOps, issueID)
	if err != nil {
		return nil, err
	}
	sortOps(ops)
	res, err := applyAll(ops, applyDispatch)
	if err != nil {
		return nil, err
	}
	if s, ok := res.Issues[issueID]; ok {
		return s, nil
	}
	return newIssueState(issueID), nil
}

// discoverAndParse walks rootOps (or the single issue subtree if issueID is
// non-empty) and returns one sortedOp per .json op file found.
func discoverAndParse(rootOps, issueID string) ([]sortedOp, error) {
	var ops []sortedOp
	root := rootOps
	if issueID != "" {
		root = filepath.Join(rootOps, issueID)
	}

	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ops, nil
		}
		return nil, fmt.Errorf("fold: stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("fold: %s: not a directory", root)
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return fmt.Errorf("fold: walk %s: %w", path, werr)
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("fold: read %s: %w", path, rerr)
		}
		env, uerr := op.Unmarshal(body)
		if uerr != nil {
			return fmt.Errorf("fold: parse %s: %w", path, uerr)
		}
		full, herr := env.FullHash()
		if herr != nil {
			return fmt.Errorf("fold: hash %s: %w", path, herr)
		}
		ops = append(ops, sortedOp{env: env, fullHash: full, path: path})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return ops, nil
}

// sortOps sorts ops globally by (HLC.Wall, HLC.Logical, op_hash) ascending.
// HLC.Less is used for the wall+logical comparison; ties on (wall, logical)
// — possible only across different writers — are broken by lexicographic
// op_hash. node_id is intentionally not used: it is already mixed into the
// hash, per spec §3.
func sortOps(ops []sortedOp) {
	sort.SliceStable(ops, func(i, j int) bool {
		a, b := ops[i].env.HLC, ops[j].env.HLC
		if a.Wall != b.Wall {
			return a.Wall < b.Wall
		}
		if a.Logical != b.Logical {
			return a.Logical < b.Logical
		}
		return ops[i].fullHash < ops[j].fullHash
	})
}

// applyAll groups ops by issue id, lazy-inits IssueState, and dispatches each
// op to the per-op-type apply function. A nil dispatch (or a dispatch that
// returns nil) for a known op type is reported as an error rather than
// silently ignored.
func applyAll(ops []sortedOp, applyDispatch func(string) ApplyFunc) (*FoldResult, error) {
	res := &FoldResult{Issues: map[string]*IssueState{}}
	for _, so := range ops {
		state, ok := res.Issues[so.env.IssueID]
		if !ok {
			state = newIssueState(so.env.IssueID)
			res.Issues[so.env.IssueID] = state
		}
		if applyDispatch == nil {
			return nil, fmt.Errorf("fold: %s: nil applyDispatch", so.path)
		}
		fn := applyDispatch(so.env.OpType)
		if fn == nil {
			return nil, fmt.Errorf("fold: %s: no apply func for op_type %q", so.path, so.env.OpType)
		}
		if err := fn(state, so.env, so.env.Payload); err != nil {
			return nil, fmt.Errorf("fold: apply %s: %w", so.path, err)
		}
		res.OpsConsumed++
	}
	return res, nil
}

// StubDispatch is the identity dispatcher for the act-9362 framework: every
// op type maps to a no-op apply that records the op type in
// state.Fields["__last_op"]. Tests use it to verify that the dispatch wiring
// reaches the expected handler in HLC order without committing to per-op
// semantics (which arrive in act-c9f0).
func StubDispatch(opType string) ApplyFunc {
	if !op.ValidOpTypes[opType] {
		return nil
	}
	return func(state *IssueState, env op.Envelope, _ []byte) error {
		state.Fields["__last_op"] = opType
		state.Fields["__last_hash"] = env.NodeID
		// Record the per-issue HLC high-water mark in LastHLC under a
		// reserved key. Per-field LWW tracking lives in act-296e.
		if cur, ok := state.LastHLC["__any"]; !ok || cur.Less(env.HLC) {
			state.LastHLC["__any"] = env.HLC
		}
		if env.OpType == "tombstone" {
			state.Tombstoned = true
		}
		return nil
	}
}
