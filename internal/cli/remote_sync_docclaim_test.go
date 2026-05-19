package cli

// Doc-claim regression tests for `act remote sync` (Phase 2 ticket 6a).
//
// Each test pins a user-visible behavior claim made in a doc surface
// at the boundary an agent would actually hit:
//
//   - `act help` stdout for help-text claims (cmd/act/help.go).
//   - The actual `.act/.sync-log` file shape for spec-claimed JSON
//     schema details.
//   - The actual installed hook body for the post-receive content.
//   - Subprocess stderr for the upstream_not_configured literal line.
//
// Internal behaviour is covered by remote_sync_test.go; this file is
// for the drift-vs-doc shape enforced by docs_sweep_test.go.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/config"
)

// TestDocClaim_RemoteSync_HelpListed asserts that `act help`'s
// rendered subcommands section names `remote sync`. The sweep
// enforces that cmd/act/help.go contains the literal `remote sync`;
// this test drives the actual binary so a refactor that splits the
// help text must still surface the verb to readers.
func TestDocClaim_RemoteSync_HelpListed(t *testing.T) {
	site := t.TempDir()
	out, _ := mustRunAct(t, site, 0, "help")
	if !strings.Contains(out, "remote sync") {
		t.Errorf("`act help` output missing `remote sync` listing")
	}
}

// TestDocClaim_RemoteSync_NoUpstreamStderr pins the spec claim
// "stderr line MUST be the literal `no origin-upstream configured;
// run 'act remote add-upstream <url>'`" at the subprocess boundary.
func TestDocClaim_RemoteSync_NoUpstreamStderr(t *testing.T) {
	host := newRemoteFixture(t)
	mustRunAct(t, host, 0, "remote", "enable")

	// Without --json the stderr line is the user-visible boundary.
	// runAct returns (stdout, stderr, exitCode).
	stdout, stderr, exit := runAct(t, host, "remote", "sync")
	_ = stdout
	if exit != 2 {
		t.Errorf("act remote sync (no upstream): exit=%d, want 2\nstdout:%s\nstderr:%s", exit, stdout, stderr)
	}
	const want = "no origin-upstream configured; run 'act remote add-upstream <url>'"
	if !strings.Contains(stderr, want) {
		t.Errorf("act remote sync (no upstream): stderr missing literal hint\nstderr:\n%s", stderr)
	}
}

// TestDocClaim_Hook_PostReceiveInvokesSync pins the spec/help claim
// that the installed post-receive hook body invokes `act remote sync`
// in the background. Drives at the filesystem boundary the spec
// names: `.act/.git/hooks/post-receive` after `act remote enable`.
//
// Since act-528547 the hook embeds the absolute path of the installing
// `act` binary in place of bare `act`, so the assertion is on the
// invariant shape: `nohup <something> remote sync ... &`, not on the
// literal bare-act form.
func TestDocClaim_Hook_PostReceiveInvokesSync(t *testing.T) {
	host := newRemoteFixture(t)
	mustRunAct(t, host, 0, "remote", "enable")
	hookPath := filepath.Join(host, ".act", ".git", "hooks", "post-receive")
	body, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	// Must invoke `remote sync` in the background via nohup. The
	// invariant is the verb invocation, not the bare-`act` shape.
	if !strings.Contains(string(body), "remote sync") {
		t.Errorf("post-receive hook body does not invoke `remote sync`:\n%s", body)
	}
	if !strings.Contains(string(body), "nohup ") {
		t.Errorf("post-receive hook body does not use `nohup` to detach:\n%s", body)
	}
	// The {{ACT_BIN}} placeholder must have been substituted at install
	// time — a literal placeholder slipping through would mean the
	// renderer was bypassed.
	if strings.Contains(string(body), "{{ACT_BIN}}") {
		t.Errorf("post-receive hook body still contains unrendered {{ACT_BIN}} placeholder:\n%s", body)
	}
	// Hook must still execute exit 0 so git receive doesn't error.
	if !strings.Contains(string(body), "exit 0") {
		t.Errorf("post-receive hook body missing `exit 0`:\n%s", body)
	}
}

// TestDocClaim_RemoteSync_SyncLogReasonFirstField pins the spec
// claim that `.act/.sync-log` lines emit `reason` as the first JSON
// field. Drives the actual file by triggering an unreachable push
// and reading the line.
func TestDocClaim_RemoteSync_SyncLogReasonFirstField(t *testing.T) {
	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("remote enable: code=%d", code)
	}
	gitDir := seedActGitForSync(t, host)
	configPath := filepath.Join(gitDir, "config")
	bogusPath := filepath.Join(t.TempDir(), "nowhere.git")
	mustExecSync(t, "git", "config", "-f", configPath, "remote.origin-upstream.url", bogusPath)
	if _, code := RunRemoteSync(RemoteSyncOptions{SourceCWD: host}); code != 0 {
		t.Fatalf("sync: code=%d", code)
	}
	lines := readSyncLogLines(t, host)
	if len(lines) == 0 {
		t.Fatalf("sync-log empty after unreachable push")
	}
	if !strings.HasPrefix(lines[0], `{"reason":`) {
		t.Errorf("sync-log first JSON field is not `reason`:\n%s", lines[0])
	}
}

// TestDocClaim_RemoteSync_SyncLogSchemaFields pins the spec schema:
// every entry has `reason`, `timestamp`, `error` keys.
func TestDocClaim_RemoteSync_SyncLogSchemaFields(t *testing.T) {
	host := newRemoteFixture(t)
	syncLogPath := filepath.Join(host, ".act", SyncLogFilename)
	entry := SyncLogEntry{
		Reason:    "unreachable",
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Error:     "synthetic",
	}
	if err := appendSyncLog(syncLogPath, entry); err != nil {
		t.Fatalf("appendSyncLog: %v", err)
	}
	lines := readSyncLogLines(t, host)
	if len(lines) != 1 {
		t.Fatalf("sync-log lines=%d, want 1", len(lines))
	}
	// Decode as a generic map so we can assert key presence (not
	// merely that the typed struct fields round-trip).
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"reason", "timestamp", "error"} {
		if _, ok := m[k]; !ok {
			t.Errorf("sync-log entry missing key %q: %v", k, m)
		}
	}
}

// configRefForDocClaim pins config.PostReceiveHookBodyTemplate so a
// refactor that renames the constant fails this file's compile, rather
// than silently un-anchoring the docclaim that the hook body invokes
// `remote sync` in the background.
var _ = config.PostReceiveHookBodyTemplate
