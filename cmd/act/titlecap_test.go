package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// act-65b0ec: act create accepted a 256-byte title while op-payload
// validation (what `act import` runs) capped titles at 200, so an issue act
// itself wrote could not be re-imported. docs/spec.md documents one cap —
// "title >256 bytes → exit 2" — and these tests hold create, update --title
// and import to it at the binary boundary.

// titlecapOpsJSONL reads every op file act wrote under dir/.act/ops and
// returns them as one JSONL document, create ops first (the importer needs a
// create before any op that targets its issue).
func titlecapOpsJSONL(t *testing.T, dir string) string {
	t.Helper()
	var creates, rest []string
	err := filepath.Walk(filepath.Join(dir, ".act", "ops"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		var head struct {
			OpType string `json:"op_type"`
		}
		if jerr := json.Unmarshal(raw, &head); jerr != nil {
			return jerr
		}
		line := strings.TrimSpace(string(raw))
		if head.OpType == "create" {
			creates = append(creates, line)
		} else {
			rest = append(rest, line)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk ops: %v", err)
	}
	if len(creates) == 0 {
		t.Fatalf("no create op found under %s/.act/ops", dir)
	}
	return strings.Join(append(creates, rest...), "\n") + "\n"
}

// TestDocClaim_TitleCap_MaxLengthRoundTripsThroughImport creates an issue
// whose title is exactly 256 bytes, retitles a second issue to another
// 256-byte title, copies the op log out, and imports it into a fresh store.
func TestDocClaim_TitleCap_MaxLengthRoundTripsThroughImport(t *testing.T) {
	src := bootstrapLoopRepo(t)
	createTitle := strings.Repeat("c", 256)
	retitle := strings.Repeat("u", 256)

	createIssue(t, src, createTitle)
	id := createIssue(t, src, "short")
	if _, stderr, code := runActIn(t, src, "update", id, "--title", retitle, "--json"); code != 0 {
		t.Fatalf("act update --title (256 bytes): exit %d; stderr=%s", code, stderr)
	}

	jsonl := filepath.Join(t.TempDir(), "ops.jsonl")
	if err := os.WriteFile(jsonl, []byte(titlecapOpsJSONL(t, src)), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	dst := bootstrapLoopRepo(t)
	out, stderr, code := runActIn(t, dst, "import", jsonl, "--json", "--no-commit")
	if code != 0 {
		t.Fatalf("act import of a 256-byte-title op log: exit %d; stdout=%s stderr=%s", code, out, stderr)
	}

	listOut, stderr, code := runActIn(t, dst, "list", "--json")
	if code != 0 {
		t.Fatalf("act list in import target: exit %d; stderr=%s", code, stderr)
	}
	for _, want := range []string{createTitle, retitle} {
		if !strings.Contains(listOut, want) {
			t.Errorf("imported store is missing a 256-byte title (%q...); list=%s", want[:8], listOut)
		}
	}
}

// TestDocClaim_TitleCap_OverCapRejected asserts 257 bytes is exit 2 on both
// create and update --title.
func TestDocClaim_TitleCap_OverCapRejected(t *testing.T) {
	dir := bootstrapLoopRepo(t)
	over := strings.Repeat("x", 257)

	if _, stderr, code := runActIn(t, dir, "create", over); code != 2 {
		t.Fatalf("act create (257-byte title): exit %d, want 2; stderr=%s", code, stderr)
	} else if !strings.Contains(stderr, "256") {
		t.Errorf("create error does not name the 256-byte cap: %s", stderr)
	}

	id := createIssue(t, dir, "short")
	if _, stderr, code := runActIn(t, dir, "update", id, "--title", over); code != 2 {
		t.Fatalf("act update --title (257 bytes): exit %d, want 2; stderr=%s", code, stderr)
	} else if !strings.Contains(stderr, "256") {
		t.Errorf("update error does not name the 256-byte cap: %s", stderr)
	}
}
