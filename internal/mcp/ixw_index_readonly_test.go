package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/cli"
)

// TestIxwMCPWritesWithReadOnlyIndexAreResultsNotErrors is the MCP side of
// act-c3aa0b: act_update, act_dep_add and act_close share the CLI's Run
// functions, so an index that cannot be updated after the commit is a
// result carrying the index_update_failed warning, not an error result the
// caller would read as "the write did not land".
func TestIxwMCPWritesWithReadOnlyIndexAreResultsNotErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions; a read-only index.db stays writable")
	}
	// One repo per tool: after the first write the index is behind the op
	// log and read-only, so a second command is rightly stopped before it
	// writes (index_rebuild_failed).
	for _, tc := range []struct {
		tool string
		args func(a, b string) map[string]any
	}{
		{"act_update", func(a, b string) map[string]any { return map[string]any{"id": a, "priority": 1} }},
		{"act_dep_add", func(a, b string) map[string]any { return map[string]any{"child": a, "parent": b} }},
		{"act_close", func(a, b string) map[string]any { return map[string]any{"id": b, "reason": "ixw"} }},
	} {
		root := makeRealRepo(t)
		a := seedIssue(t, root, "ixw mcp a")
		b := seedIssue(t, root, "ixw mcp b")
		// Bring the index up to date, then make it read-only: reads before the
		// write still answer, and only the refresh after the commit fails.
		if out, code := cli.RunList(root, cli.ListOptions{AsJSON: true}); code != 0 {
			t.Fatalf("list before freezing the index: %v", out)
		}
		files, _ := filepath.Glob(filepath.Join(root, ".act", "index.db*"))
		if len(files) == 0 {
			t.Fatal("no .act/index.db to freeze")
		}
		for _, f := range files {
			if err := os.Chmod(f, 0o444); err != nil {
				t.Fatalf("chmod %s: %v", f, err)
			}
		}

		res, text := callTool(t, root, tc.tool, tc.args(a, b))
		if isErr, _ := res["isError"].(bool); isErr {
			t.Errorf("%s with a read-only index.db is an error result: %s", tc.tool, text)
			continue
		}
		var out struct {
			Committed bool     `json:"committed"`
			Warnings  []string `json:"warnings"`
		}
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Errorf("parse %s result %q: %v", tc.tool, text, err)
			continue
		}
		if !out.Committed || strings.Join(out.Warnings, ",") != "index_update_failed" {
			t.Errorf("%s result = %s; want committed and warnings [index_update_failed]", tc.tool, text)
		}
	}
}
