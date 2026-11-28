// Package fold implements the deterministic op-fold algorithm that materialises
// task state from the op log.
//
// This file is the orchestration shell: discover, parse, validate, sort, and
// dispatch ops to a per-op-type apply function. The per-op-type semantics live
// in act-c9f0; act-9362 supplies stub apply functions adequate for testing the
// dispatch wiring.
package fold

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// IssueState is the generic per-issue state container produced by fold.
//
// Per-field LWW tracking is exposed as LastHLC for use by act-296e. Apply
// functions are expected to write into Fields and update LastHLC entries
// keyed by field name.
//
// LastHLC values are hlc.Stamp (HLC + full op_hash) so the LWW gate's tiebreak
// matches the spec (§Op-fold ties on (wall, logical) resolve by op_hash) and
// the claim winner-selection path in internal/claim. Storing the full hash
// rather than just the HLC is what keeps the two paths in sync; see
// hlc.Stamp.Less for the canonical comparator.
type IssueState struct {
	ID         string
	Fields     map[string]any
	LastHLC    map[string]hlc.Stamp
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
//
// fullHash is the full 64-hex-char op_hash of this op (the canonical SHA-256
// over {payload, hlc, node_id}) and is the tiebreak component for any LWW
// gate the apply function stamps. fold.Fold computes it once at parse time
// and threads it through here so apply functions don't have to re-hash.
type ApplyFunc func(state *IssueState, env op.Envelope, payload []byte, fullHash string) error

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
		LastHLC: map[string]hlc.Stamp{},
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
// non-empty) and returns one sortedOp per live .json op file found: every
// parsed op except those the slot rule (dropSuperseded, below) marks
// superseded. It is the one loader behind Fold, FoldIssue and
// FoldIssueStrict, so the rule holds on every fold path.
func discoverAndParse(rootOps, issueID string) ([]sortedOp, error) {
	ops, err := walkOps(rootOps, issueID)
	if err != nil {
		return nil, err
	}
	return dropSuperseded(ops), nil
}

// walkOps is the raw discovery walk: one sortedOp per .json op file under
// rootOps (or the issue subtree), with no slot rule applied. SlotState reads
// it unfiltered so it can report what the fold drops.
func walkOps(rootOps, issueID string) ([]sortedOp, error) {
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
		// Forward-compat: use the op-type-tolerant unmarshal here, NOT the
		// strict op.Unmarshal. An op whose op_type this binary does not know
		// (written by a newer act) must not abort the whole rebuild — it
		// parses, flows through to applyAll, and is skipped there (nil
		// dispatch), exactly like the "redact" legacy entry. Genuine
		// corruption (bad version, empty payload, malformed ids) is still
		// rejected. This mirrors applyAll's existing unknown-type tolerance,
		// closing the asymmetry where apply skipped unknown types but parse
		// aborted on them.
		env, uerr := op.UnmarshalTolerant(body)
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

// The slot rule (docs/redaction-design.md §4; spec "Op-fold algorithm").
//
// `act redact` removes a string from stored op files by RE-ISSUING each
// affected op: same envelope, the string replaced in the payload's text,
// one new payload key `"redacted": N` (the generation), written under the
// file name its new content hash gives, the old file deleted. Until a peer
// syncs that deletion it can hold the original and the re-issued file side
// by side, and without a rule the fold picks between them by hash —
// first-create-wins and the op_hash tiebreak make it a coin flip whether
// the old text shows (and gets indexed).
//
// The rule: ops are grouped by SLOT, (issue_id, op_type, hlc.wall,
// hlc.logical, node_id). Within a group of two or more, the highest
// `redacted` generation is the op and every member below it is superseded:
// dropped before the sort, never applied, not counted in OpsConsumed.
// Members at the same generation are all live and fold like any ops.
//
// Why the slot and not a lineage id or a hash of the original: any function
// of the original file's content (its hash, its name) published next to
// the redacted payload is an oracle for the removed string. The slot is
// content-free, every checkout computes the same one, and across real
// trackers it is shared only by one op stored at two paths (the
// pre-act-2f3d colon name and the dash name) — byte-identical unmarked
// files, whose maximum generation is 0, so they fold exactly as before. A
// single-member slot is never inspected: the cost is one map insert per op
// and a payload peek only where a slot is shared.
//
// Why the marker lives in the payload: the op hash covers {hlc, node_id,
// payload}, so the key is hashed with no change to Envelope or hashInput,
// and payload decoders ignore an unknown key, so every apply function (and
// every older binary) reads a re-issued op as an ordinary op.
//
// The rule is a pure function of the SET of op files, so the determinism
// contract holds: any permutation, any machine, same output.

// slotKey is the content-free identity of an op: the envelope fields a
// re-issue keeps unchanged. Files that share it are one op, differing at
// most by redaction.
type slotKey struct {
	issueID string
	opType  string
	wall    int64
	logical uint32
	nodeID  string
}

func slotOf(env op.Envelope) slotKey {
	return slotKey{
		issueID: env.IssueID,
		opType:  env.OpType,
		wall:    env.HLC.Wall,
		logical: env.HLC.Logical,
		nodeID:  env.NodeID,
	}
}

// Generation reports the redaction generation of an op: the value of its
// payload key `redacted`. A key that is absent, or whose value is not a
// JSON integer >= 1 (a string, a fraction, an exponent form, zero, a
// negative, null), reads as 0, as does a payload that is not a JSON object.
// The key is matched exactly — encoding/json's case-insensitive struct
// matching would let "Redacted" count — so the payload is peeked as a map
// of raw values. Exported so every reader of op files (`act log`, the
// redaction planner, doctor) applies one definition of the marker.
func Generation(env op.Envelope) int {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &m); err != nil {
		return 0
	}
	raw := bytes.TrimSpace(m["redacted"])
	if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' {
		return 0
	}
	n, err := strconv.ParseInt(string(raw), 10, 0)
	if err != nil || n < 1 {
		return 0
	}
	return int(n)
}

// slotGroup is one shared slot: its members' indices, their generations
// (parallel to members), and the group maximum.
type slotGroup struct {
	members []int
	gens    []int
	maxGen  int
}

// slotGroups indexes n envelopes (reached through at) by slot and returns
// only the groups of two or more, in first-seen order. Singletons are never
// peeked.
func slotGroups(n int, at func(i int) op.Envelope) []slotGroup {
	byKey := make(map[slotKey][]int, n)
	var order []slotKey
	for i := 0; i < n; i++ {
		k := slotOf(at(i))
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], i)
	}
	var out []slotGroup
	for _, k := range order {
		members := byKey[k]
		if len(members) < 2 {
			continue
		}
		g := slotGroup{members: members, gens: make([]int, len(members))}
		for j, i := range members {
			g.gens[j] = Generation(at(i))
			if g.gens[j] > g.maxGen {
				g.maxGen = g.gens[j]
			}
		}
		out = append(out, g)
	}
	return out
}

// supersededMask applies the slot rule: mask[i] is true when envelope i
// shares its slot with a member of strictly higher generation. A group
// whose maximum generation is 0 is left untouched.
func supersededMask(n int, at func(i int) op.Envelope) []bool {
	mask := make([]bool, n)
	for _, g := range slotGroups(n, at) {
		if g.maxGen == 0 {
			continue
		}
		for j, i := range g.members {
			if g.gens[j] < g.maxGen {
				mask[i] = true
			}
		}
	}
	return mask
}

// Superseded is the slot rule as a predicate over a set of envelopes, for
// readers that walk op files themselves (internal/cli's `act log` and `act
// show --include-ops`), so what they print is what the fold saw. The
// result has one entry per envelope, true when the fold would drop it.
// Pass every op of each issue concerned: the rule compares slot mates.
func Superseded(envs []op.Envelope) []bool {
	return supersededMask(len(envs), func(i int) op.Envelope { return envs[i] })
}

// dropSuperseded returns ops minus the members the slot rule supersedes,
// preserving order.
func dropSuperseded(ops []sortedOp) []sortedOp {
	mask := supersededMask(len(ops), func(i int) op.Envelope { return ops[i].env })
	out := ops[:0]
	for i, so := range ops {
		if !mask[i] {
			out = append(out, so)
		}
	}
	return out
}

// SlotState reports what the slot rule does to the op files under rootOps
// (every issue when issueID is "", else that issue's subtree), for the
// doctor's redaction check and the redaction planner.
//
// superseded lists the paths of files the fold ignores. conflicts lists
// the slots whose surviving members (those at a non-zero maximum
// generation) hold two or more different op bodies — two redactions that
// re-issued one op independently — as one sorted path list per slot.
// Bodies are compared by op hash, the identity of {hlc, node_id, payload},
// so one op stored at two paths is not a conflict. Both results are
// sorted and nil when empty.
func SlotState(rootOps, issueID string) (superseded []string, conflicts [][]string, err error) {
	ops, err := walkOps(rootOps, issueID)
	if err != nil {
		return nil, nil, err
	}
	for _, g := range slotGroups(len(ops), func(i int) op.Envelope { return ops[i].env }) {
		if g.maxGen == 0 {
			continue
		}
		var top []string
		bodies := map[string]bool{}
		for j, i := range g.members {
			if g.gens[j] < g.maxGen {
				superseded = append(superseded, ops[i].path)
				continue
			}
			top = append(top, ops[i].path)
			bodies[ops[i].fullHash] = true
		}
		if len(bodies) >= 2 {
			sort.Strings(top)
			conflicts = append(conflicts, top)
		}
	}
	sort.Strings(superseded)
	sort.Slice(conflicts, func(a, b int) bool { return conflicts[a][0] < conflicts[b][0] })
	return superseded, conflicts, nil
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
// op to the per-op-type apply function. A nil applyDispatch is a programming
// error. A dispatch that returns nil for a given op_type — meaning the type
// is unknown or is a legacy parse-only entry (e.g. "redact" after act-8d1d) —
// silently skips that op so historical .act/ops/ trees fold without error.
// The skipped op still creates the IssueState entry (above) so an issue
// composed entirely of skipped ops shows up with the empty state rather
// than vanishing.
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
			// Legacy / unknown op type: skip silently. The state entry
			// is preserved above; OpsConsumed is not incremented because
			// no apply ran.
			continue
		}
		if err := fn(state, so.env, so.env.Payload, so.fullHash); err != nil {
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
	return func(state *IssueState, env op.Envelope, _ []byte, fullHash string) error {
		state.Fields["__last_op"] = opType
		state.Fields["__last_hash"] = env.NodeID
		// Record the per-issue HLC high-water mark in LastHLC under a
		// reserved key. Per-field LWW tracking lives in act-296e.
		next := hlc.Stamp{HLC: env.HLC, Hash: fullHash}
		if cur, ok := state.LastHLC["__any"]; !ok || cur.Less(next) {
			state.LastHLC["__any"] = next
		}
		if env.OpType == "tombstone" {
			state.Tombstoned = true
		}
		return nil
	}
}

// ErrForeignOp reports an op file whose envelope names an issue other than
// the directory it lives in. It is the one shape that makes a per-issue
// refold unsound: the op contributes to an issue whose subtree signature did
// not move, so refolding only the changed directory would drop or misplace
// it.
//
// Nothing act writes produces this — op.ShardDir derives the directory from
// env.IssueID — so callers treat it as "fall back to a whole-tree rebuild",
// not as corruption.
var ErrForeignOp = errors.New("fold: op file names an issue other than its directory")

// FoldIssueStrict folds a single issue subtree like FoldIssue, but refuses to
// answer when the subtree contains an op belonging to another issue,
// returning ErrForeignOp.
//
// The difference matters only to the incremental read path
// (index.EnsureCurrent, act-50d2e2). FoldIssue silently discards a foreign
// op's state, which is the right shape for a caller that only wants this
// issue; an incremental index rebuild instead needs to know that its
// per-issue partition of the op tree does not hold, so it can rebuild the
// whole thing rather than write rows that omit the foreign op.
func FoldIssueStrict(rootOps, issueID string, applyDispatch func(string) ApplyFunc) (state *IssueState, found bool, err error) {
	ops, err := discoverAndParse(rootOps, issueID)
	if err != nil {
		return nil, false, err
	}
	for _, so := range ops {
		if so.env.IssueID != issueID {
			return nil, false, fmt.Errorf("%w: %s names %s", ErrForeignOp, so.path, so.env.IssueID)
		}
	}
	sortOps(ops)
	res, err := applyAll(ops, applyDispatch)
	if err != nil {
		return nil, false, err
	}
	st, ok := res.Issues[issueID]
	if !ok {
		// No ops at all under this directory. A whole-tree Fold would not
		// have produced an entry for this id either, so report absence
		// rather than an empty state — the caller writes rows from what a
		// full fold would have contained, and an empty-state row is one a
		// full rebuild would not write.
		return nil, false, nil
	}
	return st, true, nil
}
