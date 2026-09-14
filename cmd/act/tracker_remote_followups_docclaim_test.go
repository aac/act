package main

// Follow-ups to act-a025ab's tracker-remote guard: worktree {repo}
// resolution (act-15ca2b), the MCP surface (act-626391), and act init
// (act-ef5a69).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// trkfollowGitIn runs git in dir and fails the test on error.
func trkfollowGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
}

// trkfollowLinkedWorktree gives host (a repo named "proj") one commit and
// adds a real linked worktree at host/.claude/worktrees/<name>.
func trkfollowLinkedWorktree(t *testing.T, host, name string) string {
	t.Helper()
	trkfollowGitIn(t, host, "-c", "user.name=t", "-c", "user.email=t@example.com",
		"commit", "-q", "--allow-empty", "-m", "seed")
	wt := filepath.Join(host, ".claude", "worktrees", name)
	trkfollowGitIn(t, host, "worktree", "add", "-q", "-b", name, wt)
	return wt
}

// trkfollowMCPSession runs `act mcp` in dir, feeds it initialize plus the
// given tools/call requests (ids 2..), and returns each call's decoded
// result keyed by id. The process must exit 0 (the server started).
func trkfollowMCPSession(t *testing.T, dir string, calls ...string) map[float64]map[string]any {
	t.Helper()
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"
	for i, c := range calls {
		input += fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":%s}`, i+2, c) + "\n"
	}
	cmd := exec.Command(actBinary(t), "mcp")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	var outB, errB strings.Builder
	cmd.Stdout = &outB
	cmd.Stderr = &errB
	if err := cmd.Run(); err != nil {
		t.Fatalf("act mcp exited non-zero: %v\nstderr=%s\nstdout=%s", err, errB.String(), outB.String())
	}
	results := map[float64]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(outB.String()), "\n") {
		var resp struct {
			ID     float64        `json:"id"`
			Result map[string]any `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("parse response %q: %v", line, err)
		}
		results[resp.ID] = resp.Result
	}
	if info, _ := results[1]["serverInfo"].(map[string]any); info == nil {
		t.Fatalf("initialize did not answer with serverInfo: %s", outB.String())
	}
	return results
}

// trkfollowToolEnvelope extracts the error envelope from a tool result,
// failing unless the result is marked isError.
func trkfollowToolEnvelope(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("tool result isError=false, want true: %v", res)
	}
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("tool result has no content: %v", res)
	}
	first, _ := content[0].(map[string]any)
	var env map[string]any
	if err := json.Unmarshal([]byte(first["text"].(string)), &env); err != nil {
		t.Fatalf("parse envelope %q: %v", first["text"], err)
	}
	return env
}

func TestDocClaim_TrackerRemoteMCPToolCallsReturnTrackerNotCheckedOut(t *testing.T) {
	brokencoIsolateEnv(t)
	bares := t.TempDir()
	bare, id := brokencoTrackerBare(t, bares)
	host := brokencoHostRepo(t)
	t.Setenv("ACT_TRACKER_REMOTE", filepath.Join(bares, "{repo}.git"))

	results := trkfollowMCPSession(t, host,
		`{"name":"act_list","arguments":{}}`,
		fmt.Sprintf(`{"name":"act_show","arguments":{"id":%q}}`, id),
		`{"name":"act_create","arguments":{"title":"would land in an empty store"}}`,
		`{"name":"act_version","arguments":{}}`,
	)
	for _, callID := range []float64{2, 3, 4} {
		env := trkfollowToolEnvelope(t, results[callID])
		if env["error"] != "tracker_not_checked_out" {
			t.Errorf("call %v: error = %v, want tracker_not_checked_out", callID, env["error"])
		}
		msg, _ := env["message"].(string)
		if !strings.Contains(msg, "tracker exists at "+bare) || !strings.Contains(msg, "Recover with: git clone "+bare) || strings.Contains(msg, "normal") {
			t.Errorf("call %v: message = %q", callID, msg)
		}
		details, _ := env["details"].(map[string]any)
		if details["tracker_remote"] != bare || details["source"] != "$ACT_TRACKER_REMOTE" {
			t.Errorf("call %v: details = %v", callID, details)
		}
	}
	if isErr, _ := results[5]["isError"].(bool); isErr {
		t.Errorf("act_version must stay stateless: %v", results[5])
	}
	// The create must not have made a store behind the error.
	if _, err := os.Stat(filepath.Join(host, ".act")); !os.IsNotExist(err) {
		t.Errorf(".act/ exists after refused MCP calls: %v", err)
	}
}

func TestDocClaim_TrackerRemoteInitRefusesDivergentTracker(t *testing.T) {
	brokencoIsolateEnv(t)
	bares := t.TempDir()
	bare, _ := brokencoTrackerBare(t, bares)
	t.Setenv("ACT_TRACKER_REMOTE", filepath.Join(bares, "{repo}.git"))

	// --help names the escape flag and what happens without it.
	_, helpErr, _ := runActIn(t, t.TempDir(), "init", "--help")
	if !strings.Contains(helpErr, "-force-new") || !strings.Contains(helpErr, "tracker_not_checked_out") {
		t.Errorf("act init --help does not describe --force-new: %q", helpErr)
	}

	// Refused: human mode names the clone recovery and the escape.
	host := brokencoHostRepo(t)
	actDir := filepath.Join(host, ".act")
	_, stderr, code := runActIn(t, host, "init")
	if code != 3 {
		t.Fatalf("init exit = %d, want 3; stderr=%q", code, stderr)
	}
	for _, want := range []string{
		"tracker exists at " + bare,
		"Recover with: git clone " + bare + " " + actDir,
		"act init --force-new",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q: %q", want, stderr)
		}
	}
	if _, err := os.Stat(actDir); !os.IsNotExist(err) {
		t.Fatalf("refused init left %s behind: %v", actDir, err)
	}

	// Refused: JSON mode carries the code.
	stdout, _, code := runActIn(t, host, "init", "--json")
	var env map[string]any
	if err := json.Unmarshal([]byte(stdout), &env); err != nil || code != 3 || env["error"] != "tracker_not_checked_out" {
		t.Fatalf("init --json: exit=%d stdout=%q err=%v", code, stdout, err)
	}

	// MCP act_init refuses the same way, and force_new is its escape.
	mcpHost := brokencoHostRepo(t)
	res := trkfollowMCPSession(t, mcpHost, `{"name":"act_init","arguments":{}}`)
	if e := trkfollowToolEnvelope(t, res[2]); e["error"] != "tracker_not_checked_out" {
		t.Errorf("MCP act_init error = %v", e["error"])
	}

	// The escape flag deliberately starts a new tracker.
	if _, stderr, code := runActIn(t, host, "init", "--force-new"); code != 0 {
		t.Fatalf("init --force-new: exit %d: %s", code, stderr)
	}
	if !fileExists(filepath.Join(actDir, "config.json")) {
		t.Errorf("init --force-new did not create %s", filepath.Join(actDir, "config.json"))
	}

	// Unconfigured: init is unchanged.
	t.Setenv("ACT_TRACKER_REMOTE", "")
	plain := brokencoHostRepo(t)
	if _, stderr, code := runActIn(t, plain, "init"); code != 0 {
		t.Fatalf("unconfigured init: exit %d: %s", code, stderr)
	}
}

func TestDocClaim_TrackerRemoteWorktreeUsesMainRepoName(t *testing.T) {
	brokencoIsolateEnv(t)
	bares := t.TempDir()
	bare, id := brokencoTrackerBare(t, bares)
	host := brokencoHostRepo(t)
	wt := trkfollowLinkedWorktree(t, host, "agent-wt")
	t.Setenv("ACT_TRACKER_REMOTE", filepath.Join(bares, "{repo}.git"))

	// {repo} must be "proj" (the main repo), so the probe finds proj.git
	// even though the worktree's own folder is "agent-wt".
	_, stderr, code := runActIn(t, wt, "show", id)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (tracker_not_checked_out); stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "tracker exists at "+bare) {
		t.Errorf("stderr does not name %s: %q", bare, stderr)
	}
	if strings.Contains(stderr, "agent-wt.git") {
		t.Errorf("{repo} expanded to the worktree folder: %q", stderr)
	}
}
