package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/op"
)

// act-3331ff (docs/redaction-design.md §4, §10 U1): an op file left beside
// the copy a redaction re-issued from it is superseded — the fold ignores
// it, and `act log` / `act show --include-ops`, which read op files with
// their own walk, apply the same slot rule. Asserted at the binary boundary
// by planting both files in a scratch tracker, the state a peer is in
// before it syncs a redaction's deletion. Helpers are prefixed `slot`.

const slotDocPlanted = "PLANTED-4417-9902-7731"

// slotDocReissue writes the re-issued copy of the op file at path: same
// envelope, every occurrence of from in the payload's description replaced
// by to, payload key "redacted": 1, under the file name its new content hash
// gives, in the same directory. Returns the new path.
func slotDocReissue(t *testing.T, path, from, to string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	env, err := op.Unmarshal(body)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("payload of %s: %v", path, err)
	}
	desc, _ := payload["description"].(string)
	if !strings.Contains(desc, from) {
		t.Fatalf("%s: description %q does not hold the planted string", path, desc)
	}
	payload["description"] = strings.ReplaceAll(desc, from, to)
	payload["redacted"] = 1
	if env.Payload, err = canonicaljson.Marshal(payload); err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	out, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	newPath := filepath.Join(filepath.Dir(path), op.Filename(env))
	if newPath == path {
		t.Fatalf("re-issued op got the original's file name %s", path)
	}
	if err := os.WriteFile(newPath, out, 0o644); err != nil {
		t.Fatalf("write %s: %v", newPath, err)
	}
	return newPath
}

// slotDocRun runs act in dir, failing on a non-zero exit, and returns stdout.
func slotDocRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, stderr, code := runActIn(t, dir, args...)
	if code != 0 {
		t.Fatalf("act %s: exit %d; stdout=%s stderr=%s", strings.Join(args, " "), code, out, stderr)
	}
	return out
}

func TestDocClaim_Redact_LogOmitsSupersededFiles(t *testing.T) {
	dir := pass5Site(t)
	out := slotDocRun(t, dir, "create", "Reconcile brokerage statement", "--description",
		"Wire from account "+slotDocPlanted+" to savings; confirm by Friday.", "--json")
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("act create: no id in %s (%v)", out, err)
	}
	id := created.ID

	// Premise: before the re-issued copy exists, every surface prints the
	// planted string, so the absence asserted below is not vacuous.
	for _, args := range [][]string{
		{"show", id, "--json", "--no-fetch"},
		{"log", id, "--json", "--no-fetch"},
	} {
		if got := slotDocRun(t, dir, args...); !strings.Contains(got, slotDocPlanted) {
			t.Fatalf("premise: act %s does not print the planted string: %s", strings.Join(args, " "), got)
		}
	}

	original := pass5CreateOp(t, dir, id)
	reissued := slotDocReissue(t, original, slotDocPlanted, "[redacted]")
	for _, p := range []string{original, reissued} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("both files must be on disk: %v", err)
		}
	}

	const want = "Wire from account [redacted] to savings; confirm by Friday."
	for _, args := range [][]string{
		{"show", id, "--json", "--no-fetch"},
		{"show", id, "--json", "--no-fetch", "--include-ops"},
		{"show", id, "--no-fetch"},
		{"log", id, "--json", "--no-fetch"},
		{"log", "--type", "create", "--json", "--no-fetch"},
	} {
		got := slotDocRun(t, dir, args...)
		if strings.Contains(got, slotDocPlanted) {
			t.Errorf("act %s prints the superseded file's text:\n%s", strings.Join(args, " "), got)
		}
		if !strings.Contains(got, want) {
			t.Errorf("act %s does not print the re-issued text %q:\n%s", strings.Join(args, " "), want, got)
		}
	}

	// The log lists the op once — the re-issued copy, marker included —
	// not once per file.
	var logged struct {
		Ops []struct {
			OpType  string         `json:"op_type"`
			Payload map[string]any `json:"payload"`
		} `json:"ops"`
	}
	if err := json.Unmarshal([]byte(slotDocRun(t, dir, "log", id, "--json", "--no-fetch")), &logged); err != nil {
		t.Fatalf("act log --json: %v", err)
	}
	creates := 0
	for _, o := range logged.Ops {
		if o.OpType == "create" {
			creates++
			if g, _ := o.Payload["redacted"].(float64); g != 1 {
				t.Errorf("logged create carries redacted=%v, want 1", o.Payload["redacted"])
			}
		}
	}
	if creates != 1 {
		t.Errorf("act log lists %d create ops, want 1 (the superseded file is omitted)", creates)
	}
}
