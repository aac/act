package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIdxMCPCreateIndexUnopenableReturnsTheID is the MCP side of act-e3782c:
// act_create shares RunCreate, so an index that cannot be updated after the
// commit is a result carrying the id and the index_update_failed warning,
// not an error result the caller would retry.
func TestIdxMCPCreateIndexUnopenableReturnsTheID(t *testing.T) {
	root := makeRealRepo(t)
	seedIssue(t, root, "idx seed")

	db := filepath.Join(root, ".act", "index.db")
	matches, _ := filepath.Glob(db + "*")
	for _, m := range matches {
		if err := os.Remove(m); err != nil {
			t.Fatalf("remove %s: %v", m, err)
		}
	}
	if err := os.Mkdir(db, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", db, err)
	}

	res, text := callTool(t, root, "act_create", map[string]any{"title": "idx via mcp"})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("act_create with an unopenable index.db is an error result: %s", text)
	}
	var out struct {
		ID        string   `json:"id"`
		Committed bool     `json:"committed"`
		Warnings  []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("parse act_create result %q: %v", text, err)
	}
	if out.ID == "" || !out.Committed || strings.Join(out.Warnings, ",") != "index_update_failed" {
		t.Errorf("act_create result = %s; want an id, committed and warnings [index_update_failed]", text)
	}
}
