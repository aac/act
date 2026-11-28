package fold

// Tests for the slot rule (docs/redaction-design.md §4, §10 U1): among op
// files sharing (issue_id, op_type, hlc.wall, hlc.logical, node_id), the
// highest payload `redacted` generation is the op and lower ones are
// superseded. Helpers are prefixed `slot`.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

const (
	slotIssue   = "act-ae2eae"
	slotNode    = "37d15d7b"
	slotPlanted = "PLANTED-4417-9902-7731"
)

// slotCreate returns a create envelope whose description holds text, at a
// fixed slot. gen > 0 adds the `redacted` payload key.
func slotCreate(t *testing.T, text string, gen int) op.Envelope {
	t.Helper()
	payload := map[string]any{
		"title":       "Reconcile brokerage statement",
		"description": "Wire from account " + text + " to savings; confirm by Friday.",
		"type":        "task",
		"nonce":       "0123456789abcdef0123456789abcdef",
	}
	if gen > 0 {
		payload["redacted"] = gen
	}
	return slotEnvelope(t, slotIssue, "create", 1764358860229, 0, slotNode, payload)
}

// slotEnvelope builds an envelope with a canonical-JSON payload, the shape
// a real writer (and a real re-issue) produces.
func slotEnvelope(t *testing.T, issue, opType string, wall int64, logical uint32, node string, payload any) op.Envelope {
	t.Helper()
	pb, err := canonicaljson.Marshal(payload)
	if err != nil {
		t.Fatalf("slotEnvelope: marshal payload: %v", err)
	}
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        opType,
		IssueID:       issue,
		Payload:       pb,
		HLC:           hlc.HLC{Wall: wall, Logical: logical, NodeID: node},
		NodeID:        node,
	}
}

// slotWithGen returns env with a `redacted` key whose value is the raw JSON
// text rawGen spliced into its (unmarked) payload, byte for byte — not
// recanonicalised, so a literal like 1e0 reaches Generation as written.
func slotWithGen(t *testing.T, env op.Envelope, rawGen string) op.Envelope {
	t.Helper()
	if Generation(env) != 0 || len(env.Payload) < 2 || env.Payload[0] != '{' {
		t.Fatalf("slotWithGen: want an unmarked object payload, got %s", env.Payload)
	}
	env.Payload = json.RawMessage(`{"redacted":` + rawGen + `,` + string(env.Payload[1:]))
	return env
}

// slotFoldBytes folds rootOps with the real dispatch and renders every
// issue's visible state plus its LWW stamps and OpsConsumed as canonical
// JSON, so two folds compare byte for byte.
func slotFoldBytes(t *testing.T, rootOps string) []byte {
	t.Helper()
	res, err := Fold(rootOps, ApplyDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	return slotRender(t, res)
}

func slotRender(t *testing.T, res *FoldResult) []byte {
	t.Helper()
	issues := map[string]any{}
	for id, st := range res.Issues {
		stamps := map[string]any{}
		for f, s := range st.LastHLC {
			stamps[f] = map[string]any{"wall": s.HLC.Wall, "logical": s.HLC.Logical, "node": s.HLC.NodeID, "hash": s.Hash}
		}
		issues[id] = map[string]any{"state": RenderState(st), "last_hlc": stamps, "tombstoned": st.Tombstoned}
	}
	b, err := canonicaljson.Marshal(map[string]any{"issues": issues, "ops_consumed": res.OpsConsumed})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return b
}

// slotDescription folds rootOps and returns slotIssue's description.
func slotDescription(t *testing.T, rootOps string) string {
	t.Helper()
	st, err := FoldIssue(rootOps, slotIssue, ApplyDispatch)
	if err != nil {
		t.Fatalf("FoldIssue: %v", err)
	}
	d, _ := st.Fields["description"].(string)
	return d
}

func slotHash(t *testing.T, env op.Envelope) string {
	t.Helper()
	h, err := env.FullHash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return h
}

// TestSupersede_MarkedOpHidesUnmarkedSlotMate is the §1 experiment with the
// rule in place: an original create beside its re-issued copy folds
// byte-identical to the re-issued copy alone, for four replacement tokens.
// Today's fold (no rule) picks by hash, so the test also checks that at
// least one token puts the original first in hash order — the case where
// the fold without the rule would show the planted string.
func TestSupersede_MarkedOpHidesUnmarkedSlotMate(t *testing.T) {
	original := slotCreate(t, slotPlanted, 0)
	originalFirst := 0
	for _, token := range []string{"[redacted]", "XXXX", "***", "[gone]"} {
		t.Run(token, func(t *testing.T) {
			reissued := slotCreate(t, token, 1)
			if slotHash(t, original) < slotHash(t, reissued) {
				originalFirst++
			}

			both := filepath.Join(t.TempDir(), "ops")
			writeOp(t, both, original)
			writeOp(t, both, reissued)

			alone := filepath.Join(t.TempDir(), "ops")
			writeOp(t, alone, reissued)

			got, want := slotFoldBytes(t, both), slotFoldBytes(t, alone)
			if !bytes.Equal(got, want) {
				t.Fatalf("fold of {original, re-issued} differs from fold of {re-issued}:\n got %s\nwant %s", got, want)
			}
			if bytes.Contains(got, []byte(slotPlanted)) {
				t.Fatalf("folded output still holds the planted string: %s", got)
			}
			if d := slotDescription(t, both); !strings.Contains(d, token) {
				t.Fatalf("description = %q, want the replacement %q", d, token)
			}
			res, err := Fold(both, ApplyDispatch)
			if err != nil {
				t.Fatalf("Fold: %v", err)
			}
			if res.OpsConsumed != 1 {
				t.Fatalf("OpsConsumed = %d, want 1 (the superseded original is not counted)", res.OpsConsumed)
			}
		})
	}
	if originalFirst == 0 {
		t.Fatalf("no token put the original first in hash order; the test would pass without the slot rule")
	}
}

// TestSupersede_HigherGenerationWins: generations 0, 1 and 2 in one slot
// fold as generation 2 alone, whatever their hash order.
func TestSupersede_HigherGenerationWins(t *testing.T) {
	gen0 := slotCreate(t, slotPlanted, 0)
	gen1 := slotCreate(t, "[redacted] 4417", 1)
	gen2 := slotCreate(t, "[redacted]", 2)

	all := filepath.Join(t.TempDir(), "ops")
	for _, e := range []op.Envelope{gen0, gen1, gen2} {
		writeOp(t, all, e)
	}
	alone := filepath.Join(t.TempDir(), "ops")
	writeOp(t, alone, gen2)

	if got, want := slotFoldBytes(t, all), slotFoldBytes(t, alone); !bytes.Equal(got, want) {
		t.Fatalf("fold of {gen0, gen1, gen2} differs from fold of {gen2}:\n got %s\nwant %s", got, want)
	}
	if d := slotDescription(t, all); d != "Wire from account [redacted] to savings; confirm by Friday." {
		t.Fatalf("description = %q, want generation 2's text", d)
	}

	// A lower generation never supersedes a higher one, whichever of the
	// two has the smaller hash: also check gen1 vs gen2 with the payloads
	// swapped so the hash order flips.
	g1b := slotCreate(t, "[redacted]", 1)
	g2b := slotCreate(t, "[redacted] 4417", 2)
	pair := filepath.Join(t.TempDir(), "ops")
	writeOp(t, pair, g1b)
	writeOp(t, pair, g2b)
	if d := slotDescription(t, pair); d != "Wire from account [redacted] 4417 to savings; confirm by Friday." {
		t.Fatalf("description = %q, want generation 2's text", d)
	}
}

// TestSupersede_NonIntegerGenerationReadsAsZero: a `redacted` value that is
// not a JSON integer >= 1 reads as generation 0, and the key is matched
// exactly.
func TestSupersede_NonIntegerGenerationReadsAsZero(t *testing.T) {
	base := slotCreate(t, "x", 0)
	for raw, want := range map[string]int{
		`1`: 1, `7`: 7, `0`: 0, `-2`: 0, `1.5`: 0, `1e0`: 0, `"2"`: 0,
		`true`: 0, `null`: 0, `[1]`: 0, `{"n":1}`: 0,
	} {
		if got := Generation(slotWithGen(t, base, raw)); got != want {
			t.Errorf("Generation(redacted=%s) = %d, want %d", raw, got, want)
		}
	}
	if got := Generation(base); got != 0 {
		t.Errorf("Generation(no key) = %d, want 0", got)
	}
	upper := base
	upper.Payload = json.RawMessage(`{"Redacted":3,"title":"t","type":"task","nonce":"n"}`)
	if got := Generation(upper); got != 0 {
		t.Errorf("Generation(\"Redacted\") = %d, want 0 (key is case-sensitive)", got)
	}
	notObject := base
	notObject.Payload = json.RawMessage(`[1,2]`)
	if got := Generation(notObject); got != 0 {
		t.Errorf("Generation(array payload) = %d, want 0", got)
	}

	// In a slot, a malformed marker does not supersede an unmarked mate:
	// both are generation 0, so both are applied as before.
	dir := filepath.Join(t.TempDir(), "ops")
	writeOp(t, dir, slotCreate(t, slotPlanted, 0))
	writeOp(t, dir, slotWithGen(t, slotCreate(t, "[redacted]", 0), `"1"`))
	res, err := Fold(dir, ApplyDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if res.OpsConsumed != 2 {
		t.Fatalf("OpsConsumed = %d, want 2 (a string marker is generation 0)", res.OpsConsumed)
	}
}

// TestSupersede_UnmarkedSlotMatesFoldAsBefore: the one shared-slot shape
// seen in real trackers — one op stored byte-identically under the dash
// file name and the legacy colon name — and two distinct unmarked bodies
// (never observed) both fold exactly as they did before the rule: every
// file applied, output equal to the single-file fold.
func TestSupersede_UnmarkedSlotMatesFoldAsBefore(t *testing.T) {
	create := slotCreate(t, "brokerage", 0)
	upd := slotEnvelope(t, slotIssue, "update_field", 1764358860457, 0, slotNode,
		map[string]any{"field": "title", "value": "Reconcile statement"})

	single := filepath.Join(t.TempDir(), "ops")
	writeOp(t, single, create)
	writeOp(t, single, upd)

	twice := filepath.Join(t.TempDir(), "ops")
	for _, e := range []op.Envelope{create, upd} {
		dash := writeOp(t, twice, e)
		// Legacy pre-act-2f3d name: the ISO time's dashes as colons.
		base := filepath.Base(dash) // 2025-11-28T19-41-00.229Z-<hash8>-<type>.json
		colon := base[:13] + ":" + base[14:16] + ":" + base[17:]
		if colon == base {
			t.Fatalf("legacy name equals dash name: %s", base)
		}
		writeOpAtName(t, twice, e, colon)
	}

	res, err := Fold(twice, ApplyDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if res.OpsConsumed != 4 {
		t.Fatalf("OpsConsumed = %d, want 4: unmarked slot mates must all be applied, as before the rule", res.OpsConsumed)
	}
	resSingle, err := Fold(single, ApplyDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if !reflect.DeepEqual(RenderState(res.Issues[slotIssue]), RenderState(resSingle.Issues[slotIssue])) {
		t.Fatalf("byte-identical duplicates changed the folded state:\n got %v\nwant %v",
			RenderState(res.Issues[slotIssue]), RenderState(resSingle.Issues[slotIssue]))
	}
	if sup, conf, err := SlotState(twice, ""); err != nil || sup != nil || conf != nil {
		t.Fatalf("SlotState = %v, %v, %v; want nothing superseded, no conflicts", sup, conf, err)
	}

	// Two distinct unmarked bodies in one slot: both live (first create by
	// hash wins, as today).
	a, b := slotCreate(t, "alpha", 0), slotCreate(t, "bravo", 0)
	distinct := filepath.Join(t.TempDir(), "ops")
	writeOp(t, distinct, a)
	writeOp(t, distinct, b)
	res, err = Fold(distinct, ApplyDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if res.OpsConsumed != 2 {
		t.Fatalf("OpsConsumed = %d, want 2", res.OpsConsumed)
	}
	first := "alpha"
	if slotHash(t, b) < slotHash(t, a) {
		first = "bravo"
	}
	if d := slotDescription(t, distinct); !strings.Contains(d, first) {
		t.Fatalf("description = %q, want the smaller-hash body (%s)", d, first)
	}
}

// TestSupersede_EqualGenerationBothLive: two different bodies at the same
// non-zero generation are both live (the smaller hash's create applies);
// a lower-generation mate is still superseded.
func TestSupersede_EqualGenerationBothLive(t *testing.T) {
	orig := slotCreate(t, slotPlanted, 0)
	a := slotCreate(t, "[redacted] by A", 1)
	b := slotCreate(t, "[redacted] by B", 1)

	dir := filepath.Join(t.TempDir(), "ops")
	for _, e := range []op.Envelope{orig, a, b} {
		writeOp(t, dir, e)
	}
	res, err := Fold(dir, ApplyDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if res.OpsConsumed != 2 {
		t.Fatalf("OpsConsumed = %d, want 2 (both generation-1 bodies live, original superseded)", res.OpsConsumed)
	}
	want := "by A"
	if slotHash(t, b) < slotHash(t, a) {
		want = "by B"
	}
	if d := slotDescription(t, dir); !strings.Contains(d, want) {
		t.Fatalf("description = %q, want the smaller-hash body (%s)", d, want)
	}

	// Byte-identical generation-1 bodies at two paths are one op stored
	// twice: harmless, both applied, same state as one.
	dup := filepath.Join(t.TempDir(), "ops")
	p := writeOp(t, dup, a)
	writeOpAtName(t, dup, a, "copy-"+filepath.Base(p))
	writeOp(t, dup, orig)
	one := filepath.Join(t.TempDir(), "ops")
	writeOp(t, one, a)
	resDup, err := Fold(dup, ApplyDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	resOne, err := Fold(one, ApplyDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if !reflect.DeepEqual(RenderState(resDup.Issues[slotIssue]), RenderState(resOne.Issues[slotIssue])) {
		t.Fatalf("two paths of one re-issued op changed the state")
	}
}

// slotTwin re-issues env at generation gen: the `redacted` key set and,
// when the payload has a string field worth changing, a token appended
// so the twin's body (and hash) differs the way a real redaction's does.
func slotTwin(t *testing.T, env op.Envelope, gen int, token string) op.Envelope {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(env.Payload, &m); err != nil {
		t.Fatalf("slotTwin: %v", err)
	}
	for _, k := range []string{"title", "description", "criterion", "reason", "source_ref"} {
		if s, ok := m[k].(string); ok {
			m[k] = s + token
			break
		}
	}
	m["redacted"] = gen
	pb, err := canonicaljson.Marshal(m)
	if err != nil {
		t.Fatalf("slotTwin: marshal: %v", err)
	}
	env.Payload = pb
	return env
}

// slotWriteShuffled writes envs under rootOps with a random file-name
// prefix per file, so the fold's (lexical) walk order is a fresh
// permutation of the set on every call.
func slotWriteShuffled(t *testing.T, rootOps string, envs []op.Envelope, r *rand.Rand) {
	t.Helper()
	for _, i := range r.Perm(len(envs)) {
		e := envs[i]
		writeOpAtName(t, rootOps, e, fmt.Sprintf("%08x-%s", r.Uint32(), op.Filename(e)))
	}
}

// TestSupersede_PermutationInvariant: 50 seeds of random op sets with
// re-issued twins (generations 1 and 2 over some ops) fold byte-identically
// under shuffled file orders, and equal the fold of the live set alone.
// The Superseded predicate gives the same answer for shuffled slices.
func TestSupersede_PermutationInvariant(t *testing.T) {
	for seed := int64(0); seed < 50; seed++ {
		r := rand.New(rand.NewSource(seed))
		// Distinct ops only (the generator can repeat one exactly, which
		// writeOp would collapse to one file and the shuffled writer would
		// not), and twins only for ops alone in their slot: a re-issue
		// supersedes every lower-generation slot mate, so twinning one of
		// two generator ops that happen to share a slot would hide the
		// other too — the case §5.4 avoids by re-issuing every live member.
		var base []op.Envelope
		seen := map[string]bool{}
		slotCount := map[slotKey]int{}
		for _, e := range makeRandomOps(seed, 40) {
			if h := slotHash(t, e); !seen[h] {
				seen[h] = true
				base = append(base, e)
				slotCount[slotOf(e)]++
			}
		}
		var all, live []op.Envelope
		twins := 0
		for i, e := range base {
			choice := r.Intn(4)
			if slotCount[slotOf(e)] > 1 {
				choice = 3
			}
			switch choice {
			case 0: // original + gen-1 twin: only the twin is live
				t1 := slotTwin(t, e, 1, fmt.Sprintf(" [r%d]", i))
				all = append(all, e, t1)
				live = append(live, t1)
				twins++
			case 1: // original + gen-1 + gen-2: only gen 2 is live
				t1 := slotTwin(t, e, 1, fmt.Sprintf(" [a%d]", i))
				t2 := slotTwin(t, e, 2, fmt.Sprintf(" [b%d]", i))
				all = append(all, e, t1, t2)
				live = append(live, t2)
				twins++
			default:
				all = append(all, e)
				live = append(live, e)
			}
		}
		if twins == 0 {
			t.Fatalf("seed %d: no twins generated", seed)
		}

		liveDir := filepath.Join(t.TempDir(), "ops")
		for _, e := range live {
			writeOp(t, liveDir, e)
		}
		want := slotFoldBytes(t, liveDir)

		for perm := 0; perm < 3; perm++ {
			dir := filepath.Join(t.TempDir(), "ops")
			slotWriteShuffled(t, dir, all, r)
			if got := slotFoldBytes(t, dir); !bytes.Equal(got, want) {
				t.Fatalf("seed %d perm %d: fold differs from the live-set fold:\n got %s\nwant %s", seed, perm, got, want)
			}
		}

		// The predicate, on a shuffled slice, keeps exactly the live set.
		shuffled := append([]op.Envelope(nil), all...)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		mask := Superseded(shuffled)
		var kept, wantKept []string
		for i, e := range shuffled {
			if !mask[i] {
				kept = append(kept, slotHash(t, e))
			}
		}
		for _, e := range live {
			wantKept = append(wantKept, slotHash(t, e))
		}
		sort.Strings(kept)
		sort.Strings(wantKept)
		if !reflect.DeepEqual(kept, wantKept) {
			t.Fatalf("seed %d: Superseded kept %d ops, want the %d live ones", seed, len(kept), len(wantKept))
		}
	}
}

// TestSlotState_ReportsSupersededAndConflicts: SlotState names the files
// the fold ignores and the slots left with two different bodies at a
// non-zero maximum, and nothing for unmarked or identical slot mates.
func TestSlotState_ReportsSupersededAndConflicts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ops")
	const other = "act-bb11cc"

	// Slot A (slotIssue create): gen 0 + gen 1 -> gen 0 superseded.
	a0 := writeOp(t, root, slotCreate(t, slotPlanted, 0))
	a1 := writeOp(t, root, slotCreate(t, "[redacted]", 1))
	_ = a1

	// Slot B (slotIssue close): gen 0 + two different gen-1 bodies ->
	// gen 0 superseded, the two gen-1 files a conflict.
	closeEnv := slotEnvelope(t, slotIssue, "close", 1764358870000, 0, slotNode,
		map[string]any{"reason": "paid " + slotPlanted + " and PLANTED-2"})
	b0 := writeOp(t, root, closeEnv)
	bx := writeOp(t, root, slotTwin(t, closeEnv, 1, " x"))
	by := writeOp(t, root, slotTwin(t, closeEnv, 1, " y"))

	// Slot C (other issue create): one gen-1 body at two paths -> nothing.
	otherCreate := slotEnvelope(t, other, "create", 1764358880000, 0, slotNode,
		map[string]any{"title": "other", "type": "task", "nonce": "fedcba9876543210fedcba9876543210", "redacted": 1})
	c1 := writeOp(t, root, otherCreate)
	writeOpAtName(t, root, otherCreate, "copy-"+filepath.Base(c1))

	// Slot D (other issue update): two distinct unmarked bodies -> nothing.
	writeOp(t, root, slotEnvelope(t, other, "update_field", 1764358890000, 0, slotNode,
		map[string]any{"field": "title", "value": "d1"}))
	writeOp(t, root, slotEnvelope(t, other, "update_field", 1764358890000, 0, slotNode,
		map[string]any{"field": "title", "value": "d2"}))

	sup, conf, err := SlotState(root, "")
	if err != nil {
		t.Fatalf("SlotState: %v", err)
	}
	wantSup := []string{a0, b0}
	sort.Strings(wantSup)
	if !reflect.DeepEqual(sup, wantSup) {
		t.Fatalf("superseded = %v, want %v", sup, wantSup)
	}
	wantConf := []string{bx, by}
	sort.Strings(wantConf)
	if !reflect.DeepEqual(conf, [][]string{wantConf}) {
		t.Fatalf("conflicts = %v, want [%v]", conf, wantConf)
	}

	// The superseded files are exactly what the fold leaves out.
	res, err := Fold(root, ApplyDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	// 9 files: a0, a1, b0, bx, by, c1, c1 copy, d1, d2; minus a0 and b0.
	if res.OpsConsumed != 7 {
		t.Fatalf("OpsConsumed = %d, want 7", res.OpsConsumed)
	}

	// Scoped to the other issue: nothing to report.
	sup, conf, err = SlotState(root, other)
	if err != nil || sup != nil || conf != nil {
		t.Fatalf("SlotState(%s) = %v, %v, %v; want nothing", other, sup, conf, err)
	}
	// Scoped to slotIssue: the same answer as the whole tree.
	sup, conf, err = SlotState(root, slotIssue)
	if err != nil || !reflect.DeepEqual(sup, wantSup) || !reflect.DeepEqual(conf, [][]string{wantConf}) {
		t.Fatalf("SlotState(%s) = %v, %v, %v", slotIssue, sup, conf, err)
	}

	// A missing tree is empty, not an error.
	sup, conf, err = SlotState(filepath.Join(t.TempDir(), "nope"), "")
	if err != nil || sup != nil || conf != nil {
		t.Fatalf("SlotState(missing) = %v, %v, %v", sup, conf, err)
	}

	// An unparseable op file is an error, as for the fold.
	bad := filepath.Join(root, slotIssue, "2025-11", "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SlotState(root, ""); err == nil {
		t.Fatalf("SlotState over an unparseable file: want an error")
	}
}
