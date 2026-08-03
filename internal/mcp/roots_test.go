package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The tests here cover act-516314: an MCP client that advertises the standard
// `roots` capability tells the server where the user's workspace is, and that
// answer outranks the server's own process cwd.
//
// Why it matters: a plugin host launches `act mcp` as a long-lived process with
// a cwd of its choosing. When that cwd is not the user's project (the Codex
// plugin-cache case, act-ffc00d), every repo-relative tool operates in the
// wrong directory. Codex carries the workspace in a proprietary per-call
// `_meta` block; Claude Code carries it over `roots`. This is the `roots` half.

// runScript feeds a whole pre-written client script to a deferred Server and
// returns the frames the server emitted. Because the transport is a byte
// stream, a scripted response can sit in the buffer ahead of time and be read
// exactly when the server asks for it — which is what lets a plain buffer stand
// in for a live client answering a server->client request.
func runScript(t *testing.T, resolve func() (string, error), frames []map[string]any) []map[string]any {
	t.Helper()
	in := &bytes.Buffer{}
	for _, f := range frames {
		body, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("marshal frame: %v", err)
		}
		in.Write(body)
		in.WriteByte('\n')
	}
	out := &bytes.Buffer{}
	srv := NewDeferredServer(resolve, false, in, out)
	if err := srv.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal server frame %q: %v", line, err)
		}
		got = append(got, m)
	}
	return got
}

func initializeFrame(withRoots bool) map[string]any {
	caps := map[string]any{}
	if withRoots {
		caps["roots"] = map[string]any{"listChanged": true}
	}
	return map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"capabilities": caps},
	}
}

func rootsResponseFrame(dirs ...string) map[string]any {
	roots := make([]map[string]any, 0, len(dirs))
	for _, d := range dirs {
		roots = append(roots, map[string]any{"uri": "file://" + d})
	}
	return map[string]any{
		"jsonrpc": "2.0", "id": rootsRequestID,
		"result": map[string]any{"roots": roots},
	}
}

func listFrame(id int) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": "act_list", "arguments": map[string]any{}},
	}
}

// titlesFromList digs the issue titles out of an act_list tool result. Reading
// the titles — rather than an internal field — is what makes these tests assert
// on which repo the tool actually operated in.
func titlesFromList(t *testing.T, frame map[string]any) []string {
	t.Helper()
	res, ok := frame["result"].(map[string]any)
	if !ok {
		t.Fatalf("frame has no result: %+v", frame)
	}
	content, ok := res["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("result has no content: %+v", res)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	var body struct {
		Issues []struct {
			Title string `json:"title"`
		} `json:"issues"`
	}
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("unmarshal act_list body %q: %v", text, err)
	}
	titles := make([]string, 0, len(body.Issues))
	for _, i := range body.Issues {
		titles = append(titles, i.Title)
	}
	return titles
}

// responseByID picks the server's response to a specific client request.
// Selecting by id rather than by position matters here: the server also emits
// its own roots/list REQUEST onto the same stream, so "the last frame" is not
// reliably the tool result.
func responseByID(t *testing.T, frames []map[string]any, id float64) map[string]any {
	t.Helper()
	for _, f := range frames {
		if _, isReq := f["method"]; isReq {
			continue
		}
		if got, ok := f["id"].(float64); ok && got == id {
			return f
		}
	}
	t.Fatalf("no response with id %v; frames=%+v", id, frames)
	return nil
}

// findServerRequest returns the server->client request with the given method,
// or nil. Used to assert both that we ask a roots-capable client, and that we
// never ask one that did not advertise the capability.
func findServerRequest(frames []map[string]any, method string) map[string]any {
	for _, f := range frames {
		if m, _ := f["method"].(string); m == method {
			return f
		}
	}
	return nil
}

// TestRootsOverridesProcessCwd is the headline behaviour: the client names its
// workspace over `roots`, and the tool operates there rather than in the
// (wrong) directory the cwd resolver reports.
func TestRootsOverridesProcessCwd(t *testing.T) {
	cwdRepo := makeRepo(t)
	seedIssue(t, cwdRepo, "from-cwd")
	rootsRepo := makeRepo(t)
	seedIssue(t, rootsRepo, "from-roots")

	frames := runScript(t, func() (string, error) { return cwdRepo, nil }, []map[string]any{
		initializeFrame(true),
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
		rootsResponseFrame(rootsRepo),
		listFrame(2),
	})

	if findServerRequest(frames, "roots/list") == nil {
		t.Fatalf("server never issued roots/list to a roots-capable client; frames=%+v", frames)
	}
	titles := titlesFromList(t, responseByID(t, frames, 2))
	if len(titles) != 1 || titles[0] != "from-roots" {
		t.Errorf("tool operated in the wrong repo: titles=%v, want [from-roots]", titles)
	}
}

// TestRootsNotAdvertisedKeepsCwd is the regression guard for the path that
// already worked: a client that never advertises `roots` must not be sent a
// roots/list request, and resolution must stay on process cwd.
func TestRootsNotAdvertisedKeepsCwd(t *testing.T) {
	cwdRepo := makeRepo(t)
	seedIssue(t, cwdRepo, "from-cwd")

	frames := runScript(t, func() (string, error) { return cwdRepo, nil }, []map[string]any{
		initializeFrame(false),
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
		listFrame(2),
	})

	if req := findServerRequest(frames, "roots/list"); req != nil {
		t.Errorf("server asked a client that never advertised roots: %+v", req)
	}
	titles := titlesFromList(t, responseByID(t, frames, 2))
	if len(titles) != 1 || titles[0] != "from-cwd" {
		t.Errorf("titles=%v, want [from-cwd]", titles)
	}
}

// TestRootsUnansweredFallsBackAndPreservesFrames covers the misbehaving client:
// it advertises `roots` but answers with traffic of its own instead. The tool
// call it sent during that window must still be served (nothing dropped or
// reordered), and resolution must fall back to cwd rather than hang or error.
func TestRootsUnansweredFallsBackAndPreservesFrames(t *testing.T) {
	cwdRepo := makeRepo(t)
	seedIssue(t, cwdRepo, "from-cwd")

	frames := runScript(t, func() (string, error) { return cwdRepo, nil }, []map[string]any{
		initializeFrame(true),
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
		// Arrives while the server is awaiting its roots/list response.
		{"jsonrpc": "2.0", "id": 7, "method": "ping"},
		listFrame(2),
	})

	if findServerRequest(frames, "roots/list") == nil {
		t.Fatalf("server never issued roots/list; frames=%+v", frames)
	}
	var sawPing bool
	for _, f := range frames {
		if id, ok := f["id"].(float64); ok && id == 7 {
			sawPing = true
		}
	}
	if !sawPing {
		t.Errorf("frame consumed while awaiting roots/list was never answered; frames=%+v", frames)
	}
	titles := titlesFromList(t, responseByID(t, frames, 2))
	if len(titles) != 1 || titles[0] != "from-cwd" {
		t.Errorf("titles=%v, want [from-cwd] (fallback to cwd)", titles)
	}
}

// TestRootsListChangedReResolves covers a workspace that moves mid-session (a
// switched worktree): the server must drop the cached root and re-ask rather
// than keep routing writes at the old one.
func TestRootsListChangedReResolves(t *testing.T) {
	cwdRepo := makeRepo(t)
	seedIssue(t, cwdRepo, "from-cwd")
	firstRepo := makeRepo(t)
	seedIssue(t, firstRepo, "from-first")
	secondRepo := makeRepo(t)
	seedIssue(t, secondRepo, "from-second")

	frames := runScript(t, func() (string, error) { return cwdRepo, nil }, []map[string]any{
		initializeFrame(true),
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
		rootsResponseFrame(firstRepo),
		listFrame(2),
		{"jsonrpc": "2.0", "method": "notifications/roots/list_changed"},
		rootsResponseFrame(secondRepo),
		listFrame(3),
	})

	if got := titlesFromList(t, responseByID(t, frames, 2)); len(got) != 1 || got[0] != "from-first" {
		t.Errorf("before list_changed: titles=%v, want [from-first]", got)
	}
	if got := titlesFromList(t, responseByID(t, frames, 3)); len(got) != 1 || got[0] != "from-second" {
		t.Errorf("after list_changed: titles=%v, want [from-second]", got)
	}
}

// TestRootsUnresolvableIsAuthoritative: a client-declared workspace is
// authoritative, so when it names a directory that is not a host repo the tool
// surfaces that error rather than silently operating in the cwd repo. Falling
// back there is exactly the bug act-ffc00d fixed for the `_meta` path.
func TestRootsUnresolvableIsAuthoritative(t *testing.T) {
	cwdRepo := makeRepo(t)
	seedIssue(t, cwdRepo, "from-cwd")
	notARepo := t.TempDir()

	frames := runScript(t, func() (string, error) { return cwdRepo, nil }, []map[string]any{
		initializeFrame(true),
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
		rootsResponseFrame(notARepo),
		listFrame(2),
	})

	last := responseByID(t, frames, 2)
	res, ok := last["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result frame: %+v", last)
	}
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Errorf("expected a tool error for an unresolvable client root, got %+v", res)
	}
	if strings.Contains(strings.Join(titlesSafe(res), ","), "from-cwd") {
		t.Errorf("silently fell back to cwd repo: %+v", res)
	}
}

// titlesSafe extracts whatever text a tool result carries without failing the
// test — used only to prove the cwd repo's contents did not leak into an error.
func titlesSafe(res map[string]any) []string {
	content, _ := res["content"].([]any)
	out := make([]string, 0, len(content))
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			if s, ok := m["text"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// TestRootsErrorReplyIsConsumed: a client that answers roots/list with a
// JSON-RPC error must not have that frame re-dispatched as client input. The
// server treats it as "no roots" and falls back to cwd.
func TestRootsErrorReplyIsConsumed(t *testing.T) {
	cwdRepo := makeRepo(t)
	seedIssue(t, cwdRepo, "from-cwd")

	frames := runScript(t, func() (string, error) { return cwdRepo, nil }, []map[string]any{
		initializeFrame(true),
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
		{"jsonrpc": "2.0", "id": rootsRequestID,
			"error": map[string]any{"code": -32601, "message": "method not found"}},
		listFrame(2),
	})

	for _, f := range frames {
		if _, isReq := f["method"]; isReq {
			continue
		}
		if id, ok := f["id"].(string); ok && id == rootsRequestID {
			t.Errorf("server echoed a response to its own request: %+v", f)
		}
	}
	titles := titlesFromList(t, responseByID(t, frames, 2))
	if len(titles) != 1 || titles[0] != "from-cwd" {
		t.Errorf("titles=%v, want [from-cwd]", titles)
	}
}

func TestFileURIToPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"file:///Users/a/proj", "/Users/a/proj"},
		{"file://localhost/Users/a/proj", "/Users/a/proj"},
		{"file:///Users/a/my%20proj", "/Users/a/my proj"},
		{"https://example.com/x", ""},
		{"/Users/a/proj", ""},
		{"file://relative", ""},
	}
	for _, c := range cases {
		if got := fileURIToPath(c.in); got != c.want {
			t.Errorf("fileURIToPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClientDeclaresRoots(t *testing.T) {
	yes := json.RawMessage(`{"capabilities":{"roots":{"listChanged":true}}}`)
	no := json.RawMessage(`{"capabilities":{"elicitation":{}}}`)
	empty := json.RawMessage(``)
	bad := json.RawMessage(`{"capabilities":`)
	if !clientDeclaresRoots(yes) {
		t.Error("roots capability not detected")
	}
	if clientDeclaresRoots(no) || clientDeclaresRoots(empty) || clientDeclaresRoots(bad) {
		t.Error("roots capability falsely detected")
	}
}

// TestRootsResolverErrorStillReported keeps the act-119180 contract intact: a
// client with no roots and an unresolvable cwd still gets a tool error, not a
// crash.
func TestRootsResolverErrorStillReported(t *testing.T) {
	frames := runScript(t, func() (string, error) { return "", errors.New("no host git repo") }, []map[string]any{
		initializeFrame(false),
		listFrame(2),
	})
	last := responseByID(t, frames, 2)
	res, ok := last["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result frame: %+v", last)
	}
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Errorf("expected tool error, got %+v", res)
	}
}
