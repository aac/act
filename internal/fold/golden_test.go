package fold

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// goldenStateJSON is a JSON-serialisable mirror of IssueState used by the
// golden tests. It is intentionally minimal: tests use small, hand-authored
// payloads, and any field shape that needs round-tripping (e.g. accept lists
// or deps) lives inside Fields as a JSON-native value.
type goldenStateJSON struct {
	ID         string               `json:"id"`
	Fields     map[string]any       `json:"fields"`
	LastHLC    map[string]hlc.Stamp `json:"last_hlc"`
	Tombstoned bool                 `json:"tombstoned"`
}

// loadGoldenState parses a state file into an IssueState. Map values are kept
// as decoded JSON types so apply functions see the same shapes they would see
// when re-reading state from disk.
func loadGoldenState(t *testing.T, path string) *IssueState {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var g goldenStateJSON
	if err := json.Unmarshal(body, &g); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	st := newIssueState(g.ID)
	if g.Fields != nil {
		st.Fields = g.Fields
	}
	if g.LastHLC != nil {
		st.LastHLC = g.LastHLC
	}
	st.Tombstoned = g.Tombstoned
	return st
}

// renderCanonicalForGolden produces the canonical-JSON view of RenderState for
// a state. It mirrors the helper in lww_test.go but is duplicated here to keep
// the golden file independently legible.
func renderCanonicalForGolden(t *testing.T, state *IssueState) []byte {
	t.Helper()
	rendered := RenderState(state)
	if rendered == nil {
		return []byte("null")
	}
	intermediate, err := json.Marshal(rendered)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var generic any
	if err := json.Unmarshal(intermediate, &generic); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	out, err := canonicaljson.Marshal(generic)
	if err != nil {
		t.Fatalf("canonicaljson.Marshal: %v", err)
	}
	return out
}

// canonicaliseJSONBytes re-canonicalises an arbitrary JSON byte slice. The
// special token "null" is preserved verbatim so tombstoned cases compare cleanly.
func canonicaliseJSONBytes(t *testing.T, body []byte) []byte {
	t.Helper()
	trimmed := bytes.TrimSpace(body)
	if string(trimmed) == "null" {
		return []byte("null")
	}
	var generic any
	if err := json.Unmarshal(trimmed, &generic); err != nil {
		t.Fatalf("unmarshal expected: %v", err)
	}
	out, err := canonicaljson.Marshal(generic)
	if err != nil {
		t.Fatalf("canonicaljson.Marshal expected: %v", err)
	}
	return out
}

// TestGoldenApply walks testdata/golden/<op-type>/, applies the op against the
// before-state, and compares RenderState's canonical JSON to after.json
// byte-for-byte. Set GOLDEN_UPDATE=1 to regenerate after.json files in place.
func TestGoldenApply(t *testing.T) {
	root := filepath.Join("testdata", "golden")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read golden root: %v", err)
	}

	// Sort by name so test output is deterministic.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	// Coverage: every op type defined in op.ValidOpTypes must have a golden.
	wantCases := make(map[string]bool, len(op.ValidOpTypes))
	for k := range op.ValidOpTypes {
		wantCases[k] = false
	}
	// "update_field-stale" is an extra LWW-stale case beyond the 12 ops.
	knownExtras := map[string]bool{
		"update_field-stale": true,
	}

	update := os.Getenv("GOLDEN_UPDATE") == "1"

	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			caseDir := filepath.Join(root, name)

			beforePath := filepath.Join(caseDir, "before.json")
			opPath := filepath.Join(caseDir, "op.json")
			afterPath := filepath.Join(caseDir, "after.json")

			st := loadGoldenState(t, beforePath)

			opBody, err := os.ReadFile(opPath)
			if err != nil {
				t.Fatalf("read %s: %v", opPath, err)
			}
			env, err := op.Unmarshal(opBody)
			if err != nil {
				t.Fatalf("op.Unmarshal: %v", err)
			}

			fn := ApplyDispatch(env.OpType)
			if fn == nil {
				t.Fatalf("ApplyDispatch(%q) = nil", env.OpType)
			}
			fullHash, err := env.FullHash()
			if err != nil {
				t.Fatalf("full hash: %v", err)
			}
			if err := fn(st, env, env.Payload, fullHash); err != nil {
				t.Fatalf("apply: %v", err)
			}

			got := renderCanonicalForGolden(t, st)

			if update {
				// Pretty-print stays as canonical (no indent) so diffs stay
				// stable. Trailing newline keeps editors happy.
				body := append(append([]byte{}, got...), '\n')
				if err := os.WriteFile(afterPath, body, 0o644); err != nil {
					t.Fatalf("write %s: %v", afterPath, err)
				}
				return
			}

			wantBody, err := os.ReadFile(afterPath)
			if err != nil {
				t.Fatalf("read %s: %v", afterPath, err)
			}
			want := canonicaliseJSONBytes(t, wantBody)

			if !bytes.Equal(got, want) {
				t.Fatalf("after mismatch:\n got:  %s\n want: %s", got, want)
			}

			// Mark coverage. Strip any trailing variant suffix after a dash.
			base := name
			if i := strings.Index(base, "-"); i > 0 {
				if !op.ValidOpTypes[base] && !knownExtras[base] {
					base = base[:i]
				}
			}
			if _, ok := wantCases[base]; ok {
				wantCases[base] = true
			}
		})
	}

	if !update {
		var missing []string
		for k, seen := range wantCases {
			if !seen {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Fatalf("missing golden cases for op types: %v", missing)
		}
	}
}

// makeOpEnvelopeJSON returns the canonical-JSON byte form of an envelope
// constructed from the supplied parts. Used by tests that generate goldens
// programmatically; not invoked at run-time once the goldens are committed.
func makeOpEnvelopeJSON(t *testing.T, opType, issueID string, hh hlc.HLC, payload any) []byte {
	t.Helper()
	pb, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        opType,
		IssueID:       issueID,
		Payload:       pb,
		HLC:           hh,
		NodeID:        hh.NodeID,
	}
	out, err := env.Marshal()
	if err != nil {
		t.Fatalf("env.Marshal: %v", err)
	}
	return out
}

// Compile-time use marker for makeOpEnvelopeJSON to keep it from being flagged
// when the goldens are static. The function is exposed so contributors can
// regenerate op.json files via a one-off helper test if needed.
var _ = makeOpEnvelopeJSON

// formatGoldenStateJSON marshals an IssueState to the on-disk before/after
// shape used by the goldens. Currently only useful when authoring fixtures
// (kept exported via _ = blank to silence lint).
func formatGoldenStateJSON(t *testing.T, st *IssueState) []byte {
	t.Helper()
	g := goldenStateJSON{
		ID:         st.ID,
		Fields:     st.Fields,
		LastHLC:    st.LastHLC,
		Tombstoned: st.Tombstoned,
	}
	body, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden state: %v", err)
	}
	return body
}

var _ = formatGoldenStateJSON

// helpfulGoldenError is the formatted error used by the smoke checks below.
// It is placed last in the file to keep the test logic up top.
var _ = fmt.Errorf
