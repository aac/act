package importer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
)

// writeDescriptionJSONL writes a one-op JSONL log: a create whose
// description is desc.
func writeDescriptionJSONL(t *testing.T, dir, desc string) string {
	t.Helper()
	payload, err := canonicaljson.Marshal(map[string]any{
		"title":       "big body",
		"type":        "task",
		"description": desc,
		"nonce":       goodNonce("big body"),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	line, err := json.Marshal(map[string]any{
		"op_version":     1,
		"schema_version": 1,
		"op_type":        "create",
		"issue_id":       "boot-big",
		"payload":        json.RawMessage(payload),
		"hlc":            map[string]any{"wall": "2024-01-01T00:00:00.000Z", "logical": 0, "node_id": "ffffffff"},
		"node_id":        "ffffffff",
	})
	if err != nil {
		t.Fatalf("marshal line: %v", err)
	}
	path := filepath.Join(dir, "big.jsonl")
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	return path
}

// TestDocClaim_Import_DescriptionCap pins docs/spec.md "Length caps" for
// description at the `act import` boundary (act-993498): an op log whose
// create carries exactly 1048576 bytes (1 MiB) of description imports and
// folds back to the same bytes; one byte more is rejected up front, naming
// the field and the cap, with nothing written. The numbers are the spec's,
// written as literals so moving op.MaxDescriptionLen alone fails here.
func TestDocClaim_Import_DescriptionCap(t *testing.T) {
	t.Run("at cap round-trips", func(t *testing.T) {
		root := setup(t)
		desc := strings.Repeat("d", 1048576)
		res, err := Run(root, Options{JSONLPath: writeDescriptionJSONL(t, root, desc)}, &fakeGitOps{})
		if err != nil {
			t.Fatalf("import of a 1048576-byte description: %v", err)
		}
		data, err := os.ReadFile(res.MappingFile)
		if err != nil {
			t.Fatalf("read mapping: %v", err)
		}
		var mf mappingFile
		if err := json.Unmarshal(data, &mf); err != nil {
			t.Fatalf("unmarshal mapping: %v", err)
		}
		localID := mf.Mapping["boot-big"]
		folded, err := fold.Fold(config.Layout(root).Ops, fold.ApplyDispatch)
		if err != nil {
			t.Fatalf("fold: %v", err)
		}
		st, ok := folded.Issues[localID]
		if !ok {
			t.Fatalf("imported issue %q not in fold", localID)
		}
		if got, _ := st.Fields["description"].(string); got != desc {
			t.Fatalf("folded description is %d bytes, want the imported %d", len(got), len(desc))
		}
	})
	t.Run("over cap rejected", func(t *testing.T) {
		root := setup(t)
		desc := strings.Repeat("d", 1048577)
		_, err := Run(root, Options{JSONLPath: writeDescriptionJSONL(t, root, desc)}, &fakeGitOps{})
		if err == nil {
			t.Fatal("import of a 1048577-byte description succeeded, want rejected")
		}
		for _, want := range []string{"import_invalid_jsonl", "line 1", "create.description", "length 1048577 > 1048576 bytes"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
		if n := countOpFiles(t, config.Layout(root).Ops); n != 0 {
			t.Errorf("%d op files written on a rejected import, want 0", n)
		}
	})
}
