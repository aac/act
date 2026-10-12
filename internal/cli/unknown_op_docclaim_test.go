package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// seedUnknownOp writes an op whose op_type this binary does not recognise
// straight into `.act/ops/<id>/`, the way a newer act sharing the tracker
// would land one. Everything else about the envelope is well-formed, so the
// only thing a reader can object to is the type.
func seedUnknownOp(t *testing.T, root, id, opType string, wallMs int64, monthDir string) {
	t.Helper()
	if op.ValidOpTypes[opType] {
		t.Fatalf("op_type %q is a known type; the probe needs one this binary does not recognise", opType)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        opType,
		IssueID:       id,
		Payload:       json.RawMessage(`{"title":"SET-BY-AN-OP-THIS-BINARY-CANNOT-APPLY"}`),
		HLC:           hlc.HLC{Wall: wallMs, Logical: 0, NodeID: "0123abcd"},
		NodeID:        "0123abcd",
	}
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal %s envelope: %v", opType, err)
	}
	dir := filepath.Join(root, ".act", "ops", id, monthDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, id+"-"+opType+".json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestDocClaim_Fold_UnknownOpTypeIsSkippedNotFatal asserts the spec's
// forward-compat claim at the surface a reader of the spec relies on: a
// tracker holding an op of a type this binary does not know stays readable.
// `act list`, `act show` and `act ready` all exit 0 and report the issues the
// known ops describe, and the unknown op changes nothing about the issue it
// sits on (act-0be17c; behaviour from act-20e87c).
//
// The spec used to say the opposite — a hard E_UNKNOWN_OP failure — which
// would make every read below exit non-zero. internal/fold has the unit
// guard; this one drives the real read path, real dispatch table included.
func TestDocClaim_Fold_UnknownOpTypeIsSkippedNotFatal(t *testing.T) {
	root := listTestRepo(t)
	// Later than every create op, so a reader that DID apply it would
	// see its payload win.
	seedUnknownOp(t, root, "act-bbbb", "op_from_a_newer_act", 1700000050000, "2026-04")

	// list: still answers, still reports all three issues, and the issue
	// carrying the unknown op is unchanged.
	ids := listIDs(t, root)
	for _, id := range []string{"act-aaaa", "act-bbbb", "act-cccc"} {
		if ids[id] != "open" {
			t.Fatalf("list after an unknown op: %s status = %q, want open (all: %v)", id, ids[id], ids)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("list after an unknown op reported %d issues, want 3: %v", len(ids), ids)
	}
	if got := listTitle(t, root, "act-bbbb"); got != "bravo bug" {
		t.Fatalf("list title of the issue carrying the unknown op = %q, want %q — the op must be skipped, not applied", got, "bravo bug")
	}

	// show: the issue carrying the unknown op still renders.
	out, code := RunShow(root, ShowOptions{ID: "act-bbbb", AsJSON: true})
	if code != 0 {
		t.Fatalf("show exit = %d on an issue carrying an unknown op, out=%+v", code, out)
	}
	show, ok := out.(ShowResult)
	if !ok {
		t.Fatalf("show output type = %T, want ShowResult", out)
	}
	if got := show.Fields["title"]; got != "bravo bug" {
		t.Fatalf("show title = %v, want %q", got, "bravo bug")
	}

	// ready: still answers, and still offers the issue.
	out, code = RunReady(root, ReadyOptions{})
	if code != 0 {
		t.Fatalf("ready exit = %d with an unknown op in the store, out=%+v", code, out)
	}
	ready, ok := out.(ReadyResult)
	if !ok {
		t.Fatalf("ready output type = %T, want ReadyResult", out)
	}
	found := false
	for _, r := range ready.Ready {
		if r.ID == "act-bbbb" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ready does not offer act-bbbb after an unknown op landed on it: %+v", ready.Ready)
	}
}

// TestDocClaim_Spec_OpTypeEnumMatchesValidOpTypes asserts the spec's
// "`op_type` enum (all of v1)" block lists exactly the types op.ValidOpTypes
// accepts — the block had drifted to 13 of 17 (act-0be17c). Both sides are
// printed on a mismatch so the failure says which names differ.
func TestDocClaim_Spec_OpTypeEnumMatchesValidOpTypes(t *testing.T) {
	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "spec.md"))
	if err != nil {
		t.Fatalf("read docs/spec.md: %v", err)
	}
	const header = "`op_type` enum (all of v1):"
	_, after, ok := strings.Cut(string(body), header)
	if !ok {
		t.Fatalf("docs/spec.md has no %q block", header)
	}
	// The enum is the first fenced block after the header.
	parts := strings.SplitN(after, "```", 3)
	if len(parts) < 3 {
		t.Fatalf("no fenced block follows %q in docs/spec.md", header)
	}
	inSpec := regexp.MustCompile(`[a-z_]+`).FindAllString(parts[1], -1)
	sort.Strings(inSpec)

	var inCode []string
	for name := range op.ValidOpTypes {
		inCode = append(inCode, name)
	}
	sort.Strings(inCode)

	if strings.Join(inSpec, ",") != strings.Join(inCode, ",") {
		t.Fatalf("docs/spec.md op_type enum does not match op.ValidOpTypes\n  spec: %v\n  code: %v", inSpec, inCode)
	}
}
