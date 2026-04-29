package fold

// Property tests that complement TestPropertyLWWPermutationInvariance. These
// give broader coverage of the fold engine's deterministic-by-HLC contract:
// fold determinism via on-disk permutation, single-node HLC monotonicity,
// add_dep/remove_dep idempotence under random sequencing, and earliest-claim
// winner selection.

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// renderAllCanonical canonicalises a FoldResult: it renders every issue,
// sorts by id, and returns a single byte slice covering the whole result.
// Tombstoned issues render as null per RenderState's contract.
func renderAllCanonical(t *testing.T, res *FoldResult) []byte {
	t.Helper()
	ids := make([]string, 0, len(res.Issues))
	for id := range res.Issues {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make(map[string]any, len(ids))
	for _, id := range ids {
		out[id] = RenderState(res.Issues[id])
	}
	intermediate, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var generic any
	if err := json.Unmarshal(intermediate, &generic); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	canon, err := canonicaljson.Marshal(generic)
	if err != nil {
		t.Fatalf("canonicaljson.Marshal: %v", err)
	}
	return canon
}

// foldEnvsToCanonical writes envs to a fresh temp dir in the supplied order
// and returns the canonical render. The "fold permutation" is the file write
// order, not the on-disk filename: fold's discovery walks filepath.WalkDir,
// which is sorted by name, but the *sort step* inside fold rebuilds order
// from HLC alone. Two writes of the same envelopes must therefore produce
// the same canonical render regardless of what the host filesystem hands
// back from WalkDir.
func foldEnvsToCanonical(t *testing.T, envs []op.Envelope) []byte {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	for _, e := range envs {
		body, err := e.Marshal()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		shard := op.ShardDir(root, e.IssueID, e.HLC.Wall)
		if err := os.MkdirAll(shard, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		path := filepath.Join(shard, op.Filename(e))
		// Two distinct envelopes can collide on filename if their (wall,
		// op_hash[:8], op_type) triple matches. We probe up to length 16
		// to disambiguate; collisions past 16 are statistically impossible
		// for this generator.
		if _, err := os.Stat(path); err == nil {
			full, herr := e.FullHash()
			if herr != nil {
				t.Fatalf("fullhash: %v", herr)
			}
			path = filepath.Join(shard, formatProbedFilename(e, full, 12))
			if _, err := os.Stat(path); err == nil {
				path = filepath.Join(shard, formatProbedFilename(e, full, 16))
			}
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	res, err := Fold(root, ApplyDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	return renderAllCanonical(t, res)
}

// formatProbedFilename builds an op filename with a chosen hash length. We
// can't call op.filenameWithLen (unexported) so we duplicate the format here.
func formatProbedFilename(env op.Envelope, fullHash string, hashLen int) string {
	iso := isoMillis(env.HLC.Wall)
	return iso + "-" + fullHash[:hashLen] + "-" + env.OpType + ".json"
}

// isoMillis renders unix-ms as the canonical RFC3339Millis form.
func isoMillis(ms int64) string {
	return formatRFC3339Millis(ms)
}

// TestPropertyFoldDeterminism: for N random sequences of 10..30 ops each,
// fold the sequence; permute its file write order; fold again. The two
// canonical renders must be byte-identical.
func TestPropertyFoldDeterminism(t *testing.T) {
	const iterations = 50
	for iter := 0; iter < iterations; iter++ {
		seed := int64(iter*131 + 7)
		r := rand.New(rand.NewSource(seed))
		n := 10 + r.Intn(21) // 10..30
		envs := makeRandomOps(seed, n)

		baseline := foldEnvsToCanonical(t, envs)

		// Permute three times and verify byte-equal output.
		for k := 0; k < 3; k++ {
			perm := make([]op.Envelope, len(envs))
			copy(perm, envs)
			r.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
			got := foldEnvsToCanonical(t, perm)
			if !bytes.Equal(baseline, got) {
				t.Fatalf("seed=%d k=%d: render diverged\nwant: %s\ngot:  %s", seed, k, baseline, got)
			}
		}
	}
}

// TestPropertyHLCMonotonicityPerNode: simulate a single node emitting 1000
// ops via clock.Send(). Each emitted HLC must be strictly greater than the
// previous (per spec §6 determinism contract — HLC monotonicity holds even
// across local-clock jitter). The fake `now` jitters around a moving wall
// to exercise the algorithm's logical-counter regression behaviour.
func TestPropertyHLCMonotonicityPerNode(t *testing.T) {
	const iterations = 1000
	r := rand.New(rand.NewSource(424242))

	// nowMs starts at a baseline and drifts. Half the calls jitter backwards
	// to simulate NTP corrections; the HLC must absorb this without losing
	// monotonicity.
	var nowMs int64 = 1700000000000
	now := func() int64 {
		// 50% chance of forward step (1..5 ms), 50% chance of backward jump
		// (0..3 ms). The HLC's max(now, prev.wall) keeps wall non-decreasing.
		if r.Intn(2) == 0 {
			nowMs += int64(1 + r.Intn(5))
		} else {
			delta := int64(r.Intn(4))
			nowMs -= delta
		}
		return nowMs
	}
	clock := hlc.NewClock("11111111", now)

	prev := clock.Send()
	for i := 1; i < iterations; i++ {
		next := clock.Send()
		if !prev.Less(next) {
			t.Fatalf("iter %d: HLC not strictly monotone: prev=%+v next=%+v", i, prev, next)
		}
		prev = next
	}
}

// TestPropertyAddRemoveDepIdempotent: random sequence of add_dep/remove_dep
// ops on the same (parent, edge_type). After fold, the edge is present iff
// the LWW-winning op (the one with the largest HLC) is add_dep. This is the
// LWW-on-deps property: the per-edge state is determined by the last-touching
// op alone.
func TestPropertyAddRemoveDepIdempotent(t *testing.T) {
	const iterations = 50
	const opsPerIter = 12
	const parent = "act-bbbb"
	const edge = "blocks"
	const issueID = "act-aaaa"

	for iter := 0; iter < iterations; iter++ {
		seed := int64(iter*11 + 3)
		r := rand.New(rand.NewSource(seed))

		st := newIssueState(issueID)
		// Build (op_type, hlc, op_hash) triples and apply in HLC-sorted order
		// — same protocol as fold.
		type item struct {
			isAdd bool
			h     hlc.HLC
			hash  string
		}
		items := make([]item, 0, opsPerIter)
		for i := 0; i < opsPerIter; i++ {
			isAdd := r.Intn(2) == 0
			h := hlc.HLC{
				Wall:    int64(1 + r.Intn(100)),
				Logical: uint32(r.Intn(8)),
				NodeID:  fixedNodes[r.Intn(len(fixedNodes))],
			}
			opType := "remove_dep"
			var payload any = op.RemoveDepPayload{Parent: parent, EdgeType: edge}
			if isAdd {
				opType = "add_dep"
				payload = op.AddDepPayload{Parent: parent, EdgeType: edge}
			}
			env := buildEnvelope(issueID, opType, h, payload)
			fullHash, err := env.FullHash()
			if err != nil {
				t.Fatalf("FullHash: %v", err)
			}
			items = append(items, item{isAdd: isAdd, h: h, hash: fullHash})
		}
		// Sort by (wall, logical, hash) ascending, same as fold.
		sort.Slice(items, func(i, j int) bool {
			if items[i].h.Wall != items[j].h.Wall {
				return items[i].h.Wall < items[j].h.Wall
			}
			if items[i].h.Logical != items[j].h.Logical {
				return items[i].h.Logical < items[j].h.Logical
			}
			return items[i].hash < items[j].hash
		})
		// Apply in sorted order.
		for _, it := range items {
			if it.isAdd {
				p, _ := json.Marshal(op.AddDepPayload{Parent: parent, EdgeType: edge})
				if err := applyAddDep(st, op.Envelope{HLC: it.h, NodeID: it.h.NodeID}, p); err != nil {
					t.Fatal(err)
				}
			} else {
				p, _ := json.Marshal(op.RemoveDepPayload{Parent: parent, EdgeType: edge})
				if err := applyRemoveDep(st, op.Envelope{HLC: it.h, NodeID: it.h.NodeID}, p); err != nil {
					t.Fatal(err)
				}
			}
		}
		// Last item in sorted order is the LWW winner.
		lastIsAdd := items[len(items)-1].isAdd
		deps := getDeps(st)
		hasEdge := false
		for _, d := range deps {
			if d["parent"] == parent && d["edge_type"] == edge {
				hasEdge = true
				break
			}
		}
		if hasEdge != lastIsAdd {
			t.Fatalf("seed=%d: hasEdge=%v but lastIsAdd=%v (items=%+v)", seed, hasEdge, lastIsAdd, items)
		}
	}
}

// TestPropertyClaimEarliestWinner: random number of competing claim ops with
// random HLCs and node_ids. The applied state's assignee must equal the
// claim with the smallest HLC tuple per spec §5.B.3.
func TestPropertyClaimEarliestWinner(t *testing.T) {
	const iterations = 50
	const issueID = "act-aaaa"

	for iter := 0; iter < iterations; iter++ {
		seed := int64(iter*23 + 1)
		r := rand.New(rand.NewSource(seed))
		n := 2 + r.Intn(8) // 2..9 competing claims

		type claim struct {
			h        hlc.HLC
			assignee string
		}
		claims := make([]claim, 0, n)
		used := map[string]bool{}
		for i := 0; i < n; i++ {
			// Distinct HLC tuples per draw so "smallest" is unambiguous.
			var h hlc.HLC
			for {
				h = hlc.HLC{
					Wall:    int64(1 + r.Intn(50)),
					Logical: uint32(r.Intn(5)),
					NodeID:  fixedNodes[r.Intn(len(fixedNodes))],
				}
				key := keyHLC(h)
				if !used[key] {
					used[key] = true
					break
				}
			}
			claims = append(claims, claim{h: h, assignee: fixedAssignees[i%len(fixedAssignees)]})
		}

		// Find the expected winner: smallest HLC by (wall, logical, node_id).
		winner := claims[0]
		for _, c := range claims[1:] {
			if c.h.Less(winner.h) {
				winner = c
			}
		}

		// Apply in random arrival order.
		r.Shuffle(len(claims), func(i, j int) { claims[i], claims[j] = claims[j], claims[i] })
		st := newIssueState(issueID)
		for _, c := range claims {
			payload, _ := json.Marshal(op.ClaimPayload{Assignee: c.assignee})
			env := op.Envelope{
				IssueID: issueID,
				HLC:     c.h,
				NodeID:  c.h.NodeID,
			}
			if err := applyClaim(st, env, payload); err != nil {
				t.Fatalf("applyClaim: %v", err)
			}
		}
		got, _ := st.Fields["assignee"].(string)
		if got != winner.assignee {
			t.Fatalf("seed=%d: assignee=%q want %q (winner HLC=%+v)", seed, got, winner.assignee, winner.h)
		}
		if got, _ := st.Fields["status"].(string); got != "in_progress" {
			t.Fatalf("seed=%d: status=%q want in_progress", seed, got)
		}
	}
}

// keyHLC renders an HLC as a stable string key for dedup purposes.
func keyHLC(h hlc.HLC) string {
	return formatRFC3339Millis(h.Wall) + ":" + h.NodeID
}

// TestPropertyGeneratorCovers12OpTypes is the "smoke test" called for in the
// issue's test plan: across 1000 draws the generator must emit every one of
// the 12 op types.
func TestPropertyGeneratorCovers12OpTypes(t *testing.T) {
	envs := makeRandomOps(99, 1000)
	seen := map[string]bool{}
	for _, e := range envs {
		seen[e.OpType] = true
	}
	for _, opType := range allOpTypes {
		// "create" only appears as the per-issue seed in makeRandomOps; that
		// still counts as a draw.
		if !seen[opType] {
			t.Fatalf("op_type %q never generated in 1000 draws", opType)
		}
	}
}

// TestPropertyGeneratorReproducible verifies the test-plan reproducibility
// requirement: a known-seed run produces identical generated ops across two
// invocations. We compare envelope canonical bytes since op.Envelope contains
// json.RawMessage which doesn't reflect.DeepEqual cleanly across calls.
func TestPropertyGeneratorReproducible(t *testing.T) {
	a := makeRandomOps(7, 50)
	b := makeRandomOps(7, 50)
	if len(a) != len(b) {
		t.Fatalf("len mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		ab, err := a[i].Marshal()
		if err != nil {
			t.Fatalf("marshal a[%d]: %v", i, err)
		}
		bb, err := b[i].Marshal()
		if err != nil {
			t.Fatalf("marshal b[%d]: %v", i, err)
		}
		if !bytes.Equal(ab, bb) {
			t.Fatalf("op %d not reproducible:\nfirst:  %s\nsecond: %s", i, ab, bb)
		}
	}
}
