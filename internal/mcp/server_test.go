package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/cli"
)

// makeRepo prepares a tempdir initialised as both a git repo and an act
// repo so the cli RunX helpers can run against it.
func makeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	_, code := cli.RunInit(root, false, "machine-mcp", "mcp@example.com",
		func() time.Time { return time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC) })
	if code != 0 {
		t.Fatalf("RunInit failed: code=%d", code)
	}
	return root
}

// runOne feeds a single JSON-RPC line to a fresh Server and returns the
// parsed response.
func runOne(t *testing.T, repoRoot string, readOnly bool, req map[string]any) jsonRPCResponse {
	t.Helper()
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal req: %v", err)
	}
	in.Write(body)
	in.WriteByte('\n')
	srv := NewServer(repoRoot, readOnly, in, out)
	if err := srv.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var resp jsonRPCResponse
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal resp: %v\nraw=%s", err, out.String())
	}
	return resp
}

func TestInitialize(t *testing.T) {
	root := makeRepo(t)
	resp := runOne(t, root, false, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T", resp.Result)
	}
	if m["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion = %v, want %v", m["protocolVersion"], protocolVersion)
	}
	caps, _ := m["capabilities"].(map[string]any)
	if _, hasTools := caps["tools"]; !hasTools {
		t.Errorf("capabilities missing 'tools': %+v", caps)
	}
	info, _ := m["serverInfo"].(map[string]any)
	if info["name"] != serverName {
		t.Errorf("serverInfo.name = %v", info["name"])
	}
}

func TestToolsList(t *testing.T) {
	root := makeRepo(t)
	resp := runOne(t, root, false, map[string]any{
		"jsonrpc": "2.0",
		"id":      "tl",
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	m, _ := resp.Result.(map[string]any)
	tools, _ := m["tools"].([]any)
	if got := len(tools); got != 12 {
		t.Fatalf("tools count = %d, want 12", got)
	}
	want := map[string]bool{
		"act_init": false, "act_create": false, "act_list": false,
		"act_show": false, "act_update": false, "act_close": false,
		"act_dep_add": false, "act_ready": false, "act_search": false,
		"act_log": false, "act_doctor": false, "act_version": false,
	}
	for _, raw := range tools {
		td, _ := raw.(map[string]any)
		name, _ := td["name"].(string)
		if name == "" {
			t.Errorf("tool missing name: %+v", td)
		}
		schema, _ := td["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			t.Errorf("tool %s: inputSchema type = %v, want object", name, schema["type"])
		}
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected tool %q", name)
		}
		want[name] = true
	}
	for n, seen := range want {
		if !seen {
			t.Errorf("missing tool %q", n)
		}
	}
}

func TestToolsCallList(t *testing.T) {
	root := makeRepo(t)
	resp := runOne(t, root, false, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "act_list",
			"arguments": map[string]any{},
		},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	m, _ := resp.Result.(map[string]any)
	if isErr, _ := m["isError"].(bool); isErr {
		t.Fatalf("tool returned error envelope: %+v", m)
	}
	content, _ := m["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content")
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	// The body should be a JSON document corresponding to ListResult.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("body is not JSON: %v\ntext=%s", err, text)
	}
	if _, ok := parsed["issues"]; !ok {
		t.Errorf("expected 'issues' key in ListResult; got %+v", parsed)
	}
}

func TestReadOnlyRefusal(t *testing.T) {
	root := makeRepo(t)
	resp := runOne(t, root, true, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "act_create",
			"arguments": map[string]any{
				"title": "should be refused",
			},
		},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %+v", resp.Error)
	}
	m, _ := resp.Result.(map[string]any)
	if isErr, _ := m["isError"].(bool); !isErr {
		t.Fatalf("expected isError=true, got %+v", m)
	}
	content, _ := m["content"].([]any)
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if !strings.Contains(text, "method_not_allowed") {
		t.Errorf("expected method_not_allowed in body; got %s", text)
	}
}

func TestUnknownMethod(t *testing.T) {
	root := makeRepo(t)
	resp := runOne(t, root, false, map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "no_such_method",
		"params":  map[string]any{},
	})
	if resp.Error == nil {
		t.Fatalf("expected error, got result %+v", resp.Result)
	}
	if resp.Error.Code != errMethodNotFound {
		t.Errorf("error code = %d, want %d", resp.Error.Code, errMethodNotFound)
	}
}
