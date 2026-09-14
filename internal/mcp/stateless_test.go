package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// statelessSession writes each request as one newline-delimited JSON-RPC frame
// on the server's stdin, runs the server to EOF, and returns every response
// frame it wrote to stdout, in order. It is the stdio boundary a real client
// sees: no handler is called directly.
func statelessSession(t *testing.T, repoRoot string, reqs ...map[string]any) []map[string]any {
	t.Helper()
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	for _, r := range reqs {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal req: %v", err)
		}
		in.Write(b)
		in.WriteByte('\n')
	}
	srv := NewServer(repoRoot, false, in, out)
	if err := srv.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var frames []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("response frame is not JSON: %v\nraw=%s", err, line)
		}
		frames = append(frames, m)
	}
	return frames
}

// statelessMeta is the per-request _meta a 2026-07-28 client sends in place of
// the initialize handshake.
func statelessMeta(version string) map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    version,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "stateless-test", "version": "1.0"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
}

func statelessResult(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	if e, ok := frame["error"]; ok {
		t.Fatalf("unexpected JSON-RPC error: %+v", e)
	}
	res, ok := frame["result"].(map[string]any)
	if !ok {
		t.Fatalf("frame has no result object: %+v", frame)
	}
	return res
}

// TestDocClaim_MCP_StatelessRequestsWithoutInitialize pins the docs/spec.md
// `act mcp` protocol-revisions claim: a 2026-07-28 client that never sends
// initialize gets tools/list and tools/call answered, reading the protocol
// version from params._meta, with resultType, per-result serverInfo, and the
// tools/list cache hint (ttlMs + cacheScope) in the spec's shape.
func TestDocClaim_MCP_StatelessRequestsWithoutInitialize(t *testing.T) {
	root := makeRepo(t)
	frames := statelessSession(t, root,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list",
			"params": map[string]any{"_meta": statelessMeta("2026-07-28")}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
			"params": map[string]any{"name": "act_list", "arguments": map[string]any{},
				"_meta": statelessMeta("2026-07-28")}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "server/discover",
			"params": map[string]any{"_meta": statelessMeta("2026-07-28")}},
	)
	if len(frames) != 3 {
		t.Fatalf("want 3 response frames, got %d: %+v", len(frames), frames)
	}

	list := statelessResult(t, frames[0])
	if list["resultType"] != "complete" {
		t.Errorf("tools/list resultType = %v, want complete", list["resultType"])
	}
	if tools, _ := list["tools"].([]any); len(tools) == 0 {
		t.Errorf("tools/list returned no tools: %+v", list)
	}
	ttl, isNum := list["ttlMs"].(float64)
	if !isNum || ttl < 0 || ttl != float64(int64(ttl)) {
		t.Errorf("tools/list ttlMs = %#v, want a non-negative integer", list["ttlMs"])
	}
	if cs := list["cacheScope"]; cs != "public" && cs != "private" {
		t.Errorf("tools/list cacheScope = %#v, want public|private", cs)
	}
	statelessAssertServerInfo(t, "tools/list", list)

	call := statelessResult(t, frames[1])
	if call["resultType"] != "complete" {
		t.Errorf("tools/call resultType = %v, want complete", call["resultType"])
	}
	if isErr, _ := call["isError"].(bool); isErr {
		t.Fatalf("tools/call returned tool error: %+v", call)
	}
	content, _ := call["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("tools/call has no content: %+v", call)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	var body map[string]any
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("tools/call body not JSON: %v\n%s", err, text)
	}
	if _, ok := body["issues"]; !ok {
		t.Errorf("act_list body missing issues: %+v", body)
	}
	statelessAssertServerInfo(t, "tools/call", call)

	disc := statelessResult(t, frames[2])
	vers, _ := disc["supportedVersions"].([]any)
	if len(vers) == 0 || vers[0] != "2026-07-28" {
		t.Errorf("server/discover supportedVersions = %+v", disc["supportedVersions"])
	}
	if caps, _ := disc["capabilities"].(map[string]any); caps["tools"] == nil {
		t.Errorf("server/discover capabilities missing tools: %+v", disc)
	}
	if _, ok := disc["ttlMs"]; !ok {
		t.Errorf("server/discover missing ttlMs: %+v", disc)
	}
}

func statelessAssertServerInfo(t *testing.T, method string, res map[string]any) {
	t.Helper()
	meta, _ := res["_meta"].(map[string]any)
	info, _ := meta["io.modelcontextprotocol/serverInfo"].(map[string]any)
	if info["name"] != serverName {
		t.Errorf("%s _meta serverInfo = %+v, want name %q", method, meta, serverName)
	}
}

// TestMCP_StatelessUnsupportedVersionAndMissingCapabilities pins the modern
// error paths: an unimplemented _meta version is UnsupportedProtocolVersion
// (-32022) naming what is supported, and a modern request without the required
// clientCapabilities is Invalid params (-32602).
func TestMCP_StatelessUnsupportedVersionAndMissingCapabilities(t *testing.T) {
	root := makeRepo(t)
	frames := statelessSession(t, root,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list",
			"params": map[string]any{"_meta": statelessMeta("1900-01-01")}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list",
			"params": map[string]any{"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion": "2026-07-28"}}},
	)
	if len(frames) != 2 {
		t.Fatalf("want 2 frames, got %d: %+v", len(frames), frames)
	}
	e1, _ := frames[0]["error"].(map[string]any)
	if e1["code"] != float64(-32022) {
		t.Errorf("unsupported version code = %v, want -32022 (%+v)", e1["code"], frames[0])
	}
	data, _ := e1["data"].(map[string]any)
	if data["requested"] != "1900-01-01" {
		t.Errorf("error data.requested = %v", data["requested"])
	}
	if sup, _ := data["supported"].([]any); len(sup) == 0 || sup[0] != "2026-07-28" {
		t.Errorf("error data.supported = %v", data["supported"])
	}
	e2, _ := frames[1]["error"].(map[string]any)
	if e2["code"] != float64(-32602) {
		t.Errorf("missing capabilities code = %v, want -32602 (%+v)", e2["code"], frames[1])
	}
}

// TestMCP_LegacyHandshakeUnchanged drives the legacy flow — initialize,
// notifications/initialized, tools/list, tools/call — over stdio and asserts
// the responses carry none of the modern-only fields, so pre-2026-07-28
// clients see exactly the wire shape they always did.
func TestMCP_LegacyHandshakeUnchanged(t *testing.T) {
	root := makeRepo(t)
	frames := statelessSession(t, root,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}}},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call",
			"params": map[string]any{"name": "act_list", "arguments": map[string]any{}}},
	)
	if len(frames) != 3 {
		t.Fatalf("want 3 frames (notification gets none), got %d: %+v", len(frames), frames)
	}
	init := statelessResult(t, frames[0])
	if init["protocolVersion"] != "2024-11-05" {
		t.Errorf("initialize protocolVersion = %v, want 2024-11-05", init["protocolVersion"])
	}
	for i, method := range []string{"initialize", "tools/list", "tools/call"} {
		res := statelessResult(t, frames[i])
		for _, k := range []string{"resultType", "ttlMs", "cacheScope", "_meta"} {
			if _, ok := res[k]; ok {
				t.Errorf("legacy %s result carries modern field %q: %+v", method, k, res)
			}
		}
	}
	if tools, _ := statelessResult(t, frames[1])["tools"].([]any); len(tools) == 0 {
		t.Errorf("legacy tools/list returned no tools")
	}
	if isErr, _ := statelessResult(t, frames[2])["isError"].(bool); isErr {
		t.Errorf("legacy tools/call returned tool error: %+v", frames[2])
	}
}
