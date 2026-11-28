package redaction

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/op"
)

// redCanon re-encodes a JSON document canonically, for comparing payloads.
func redCanon(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("redCanon %s: %v", s, err)
	}
	b, err := canonicaljson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApply_TextFieldsPerOpType(t *testing.T) {
	S, R := redSecret, Replacement
	cases := []struct {
		name    string
		opType  string
		payload string
		want    string
		n       int
	}{
		{"create title, description, each accept", "create",
			`{"title":"T ` + S + `","description":"` + S + ` and ` + S + `","accept":["a ` + S + `","plain"],"type":"task","nonce":"` + redNonce + `"}`,
			`{"title":"T ` + R + `","description":"` + R + ` and ` + R + `","accept":["a ` + R + `","plain"],"type":"task","nonce":"` + redNonce + `"}`, 4},
		{"update_field title", "update_field",
			`{"field":"title","value":"x ` + S + `"}`, `{"field":"title","value":"x ` + R + `"}`, 1},
		{"update_field description", "update_field",
			`{"field":"description","value":"` + S + `"}`, `{"field":"description","value":"` + R + `"}`, 1},
		{"add_accept criterion", "add_accept",
			`{"criterion":"c ` + S + `"}`, `{"criterion":"c ` + R + `"}`, 1},
		{"set_accept each criterion", "set_accept",
			`{"criteria":["` + S + `","b","c ` + S + `"]}`, `{"criteria":["` + R + `","b","c ` + R + `"]}`, 2},
		{"close reason", "close",
			`{"reason":"r ` + S + `","no_code":true}`, `{"reason":"r ` + R + `","no_code":true}`, 1},
		{"reopen reason", "reopen",
			`{"reason":"` + S + `"}`, `{"reason":"` + R + `"}`, 1},
		{"unclaim reason", "unclaim",
			`{"reason":"` + S + `"}`, `{"reason":"` + R + `"}`, 1},
		{"add_external_dep ref", "add_external_dep",
			`{"ref":"linear:` + S + `"}`, `{"ref":"linear:` + R + `"}`, 1},
		{"remove_external_dep ref", "remove_external_dep",
			`{"ref":"linear:` + S + `"}`, `{"ref":"linear:` + R + `"}`, 1},
		{"redact pre-act-8d1d new_value", "redact",
			`{"field_path":"description","new_value":"` + S + `"}`, `{"field_path":"description","new_value":"` + R + `"}`, 1},

		// Structural fields and types with no text fields: left alone.
		{"create machine and nonce are structure", "create",
			`{"title":"t","type":"task","machine":"` + S + `","nonce":"` + redNonce + `"}`,
			`{"title":"t","type":"task","machine":"` + S + `","nonce":"` + redNonce + `"}`, 0},
		{"update_field of another field", "update_field",
			`{"field":"assignee","value":"` + S + `"}`, `{"field":"assignee","value":"` + S + `"}`, 0},
		{"update_field description, non-string value", "update_field",
			`{"field":"description","value":{"x":"` + S + `"}}`, `{"field":"description","value":{"x":"` + S + `"}}`, 0},
		{"claim assignee", "claim",
			`{"assignee":"` + S + `"}`, `{"assignee":"` + S + `"}`, 0},
		{"import source_ref", "import",
			`{"source_ref":"` + S + `"}`, `{"source_ref":"` + S + `"}`, 0},
		{"object key is structure", "create",
			`{"title":"t","type":"task","nonce":"` + redNonce + `","x":{"` + S + `":1}}`,
			`{"title":"t","type":"task","nonce":"` + redNonce + `","x":{"` + S + `":1}}`, 0},
		{"unknown op type", "frobnicate",
			`{"title":"` + S + `"}`, `{"title":"` + S + `"}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := redEnv(t, redIssue, tc.opType, redWall, 0, redNode, tc.payload)
			out, n, err := Apply(env, []string{S})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if n != tc.n {
				t.Errorf("replacements = %d, want %d", n, tc.n)
			}
			if got, want := string(out.Payload), redCanon(t, tc.want); got != want {
				t.Errorf("payload =\n  %s\nwant\n  %s", got, want)
			}
			if strings.Contains(string(out.Payload), `"redacted"`) {
				t.Errorf("Apply set the generation marker; only Reissue may")
			}
			// The envelope outside the payload is untouched.
			out.Payload, env.Payload = nil, nil
			if out.OpType != env.OpType || out.IssueID != env.IssueID || out.HLC != env.HLC ||
				out.NodeID != env.NodeID || out.WriterVersion != env.WriterVersion {
				t.Errorf("envelope changed: %+v vs %+v", out, env)
			}
		})
	}
}

// redSamplePayloads holds one representative payload per op type, written
// to exercise the round trip: non-ASCII, characters encoding/json would
// HTML-escape, control characters, nested objects, numbers, booleans.
var redSamplePayloads = map[string]string{
	"create":              `{"title":"plain <title> & more","description":"Zoë's line\nnext\ttab \u0001","type":"task","priority":2,"accept":["a","b"],"parent":"act-bbbb","machine":"mini","nonce":"` + redNonce + `"}`,
	"update_field":        `{"field":"description","value":"some text — with a dash"}`,
	"add_dep":             `{"parent":"act-bbbb","edge_type":"blocks"}`,
	"remove_dep":          `{"parent":"act-bbbb","edge_type":"relates"}`,
	"add_external_dep":    `{"ref":"linear:ENG-1"}`,
	"remove_external_dep": `{"ref":"linear:ENG-1"}`,
	"add_accept":          `{"criterion":"works \"quoted\""}`,
	"remove_accept":       `{"index":0}`,
	"set_accept":          `{"criteria":["a","b"]}`,
	"claim":               `{"assignee":"alice@example"}`,
	"unclaim":             `{"reason":"cannot finish"}`,
	"close":               `{"reason":"done","no_code":true}`,
	"reopen":              `{"reason":"regressed"}`,
	"import":              `{"source_ref":"github:o/r#1","mapping":{"x":"y","a":"b"}}`,
	"migrate":             `{"from_version":1,"to_version":2,"transform":{"kind":"rename_field","from":"a","to":"b"}}`,
	"tombstone":           `{"deleted_at":"2026-04-29T12:00:00Z"}`,
	"redact":              `{"ops_rewritten":2,"replacements":3}`,
}

func TestApply_NoHitRoundTripsByteIdentical(t *testing.T) {
	types := make([]string, 0, len(op.ValidOpTypes))
	for ty := range op.ValidOpTypes {
		types = append(types, ty)
	}
	sort.Strings(types)
	for _, ty := range types {
		t.Run(ty, func(t *testing.T) {
			payload, ok := redSamplePayloads[ty]
			if !ok {
				t.Fatalf("no sample payload for op type %q: add one to redSamplePayloads", ty)
			}
			env := redEnv(t, redIssue, ty, redWall, 3, redNode, payload)
			want := redMarshal(t, env)
			// Start from the bytes as they sit on disk, the way the plan does.
			disk, err := op.UnmarshalTolerant(want)
			if err != nil {
				t.Fatal(err)
			}
			out, n, err := Apply(disk, []string{"NOT-PRESENT-ANYWHERE"})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if n != 0 {
				t.Errorf("replacements = %d, want 0", n)
			}
			if got := redMarshal(t, out); !bytes.Equal(got, want) {
				t.Errorf("round trip changed the bytes:\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestApply_MatchesAcrossJSONEscapes(t *testing.T) {
	pattern := `acct "Zoë" 4417`
	env := redEnv(t, redIssue, "update_field", redWall, 0, redNode,
		`{"field":"description","value":"wire from `+strings.ReplaceAll(pattern, `"`, `\"`)+` today"}`)
	file := redMarshal(t, env)
	if bytes.Contains(file, []byte(pattern)) {
		t.Fatalf("fixture: file bytes hold the unescaped pattern; the test would not exercise escaping")
	}
	out, n, err := Apply(env, []string{pattern})
	if err != nil || n != 1 {
		t.Fatalf("Apply = %d, %v; want 1 replacement", n, err)
	}
	if got, want := string(out.Payload), redCanon(t, `{"field":"description","value":"wire from [redacted] today"}`); got != want {
		t.Errorf("payload = %s, want %s", got, want)
	}

	// The same text written with \u escapes (a hand-edited or foreign file)
	// matches too, because matching runs on the decoded string.
	escaped := redEnv(t, redIssue, "update_field", redWall, 0, redNode,
		`{"field":"description","value":"wire from acct `+"\\"+`u0022Zo`+"\\"+`u00eb`+"\\"+`u0022 4417 today"}`)
	if !strings.Contains(string(escaped.Payload), "\\u00eb") {
		t.Fatalf("fixture: payload %s does not hold the \\u escape", escaped.Payload)
	}
	if _, n, err := Apply(escaped, []string{pattern}); err != nil || n != 1 {
		t.Errorf("Apply on \\u-escaped payload = %d, %v; want 1 replacement", n, err)
	}

	// And the raw scan finds the canonical file by the escaped raw form.
	if !containsAny(file, rawForms([]string{pattern})) {
		t.Errorf("rawForms do not find the pattern in the canonical file bytes")
	}
}

func TestReissue_SetsGenerationAndRejectsBadInput(t *testing.T) {
	env := redEnv(t, redIssue, "close", redWall, 0, redNode, `{"reason":"x","redacted":1}`)
	re, err := Reissue(env, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(re.Payload), `{"reason":"x","redacted":2}`; got != want {
		t.Errorf("payload = %s, want %s", got, want)
	}
	if Generation(re) != 2 {
		t.Errorf("Generation = %d, want 2", Generation(re))
	}
	if _, err := Reissue(env, 0); err == nil {
		t.Errorf("Reissue(gen 0) = nil error, want error")
	}
	if _, err := Reissue(redEnv(t, redIssue, "frobnicate", redWall, 0, redNode, `["x"]`), 1); err == nil {
		t.Errorf("Reissue(non-object payload) = nil error, want error")
	}
}

func TestGeneration_OnlyPositiveIntegersCount(t *testing.T) {
	for payload, want := range map[string]int{
		`{"reason":"x"}`:           0,
		`{"redacted":1}`:           1,
		`{"redacted":7}`:           7,
		`{"redacted":0}`:           0,
		`{"redacted":-2}`:          0,
		`{"redacted":1.5}`:         0,
		`{"redacted":"1"}`:         0,
		`{"redacted":true}`:        0,
		`{"redacted":null}`:        0,
		`["not","an","object"]`:    0,
		`{"redacted":{"n":1}}`:     0,
		`{"x":1,"redacted":3}`:     3,
		`{"redacted":12345678901}`: 12345678901,
	} {
		env := redEnv(t, redIssue, "close", redWall, 0, redNode, payload)
		if got := Generation(env); got != want {
			t.Errorf("Generation(%s) = %d, want %d", payload, got, want)
		}
	}
}
