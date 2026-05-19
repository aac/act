package cli

// Phase 2 ticket 3b (act-4a604d) — --offline + pending-push retry tests.
//
// AC1: --offline commits locally, no push attempted, .act/.pending-pushes
//      gains a record with the local commit's SHA.
// AC2: A subsequent non-offline write flushes the pending-push before
//      its own push; .act/.pending-pushes is empty afterwards; both
//      commits land on the remote.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
)

// TestOffline_CreateLocallyCommitsAndRecordsPending — AC1.
//
// With origin configured and --offline set, the create op lands as a
// local commit but the push is skipped. The .act/.pending-pushes file
// gains exactly one record whose `sha` field matches HEAD on the
// nested .act/ repo. The push-invocation counter must NOT increment
// for this call.
func TestOffline_CreateLocallyCommitsAndRecordsPending(t *testing.T) {
	gitops.ResetPushAttemptCounter()
	root, remote := makeRepoWithRemoteOrigin(t)
	paths := config.Layout(root)

	before := gitops.TestPushInvocationCount.Load()
	out, code := RunCreate(root, CreateOptions{Title: "offline-1", Type: "task", Offline: true})
	if code != 0 {
		t.Fatalf("RunCreate (offline): code=%d, out=%+v", code, out)
	}
	after := gitops.TestPushInvocationCount.Load()
	if after-before != 0 {
		t.Errorf("TestPushInvocationCount delta = %d, want 0 (offline skips push)", after-before)
	}

	// HEAD on the nested .act/ repo is the local commit; capture it.
	headSHA := strings.TrimSpace(runOut(t, paths.Root, "git", "rev-parse", "HEAD"))

	// .act/.pending-pushes must exist and contain exactly one record
	// whose sha matches HEAD.
	body, err := os.ReadFile(filepath.Join(paths.Root, ".pending-pushes"))
	if err != nil {
		t.Fatalf("read .pending-pushes: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf(".pending-pushes: want 1 line, got %d:\n%s", len(lines), body)
	}
	var rec struct {
		Timestamp string `json:"timestamp"`
		SHA       string `json:"sha"`
		OpType    string `json:"op_type"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("unmarshal pending-push record: %v (line=%q)", err, lines[0])
	}
	if rec.SHA != headSHA {
		t.Errorf("pending-push sha = %q, want HEAD %q", rec.SHA, headSHA)
	}
	if rec.OpType != "create" {
		t.Errorf("pending-push op_type = %q, want create", rec.OpType)
	}
	if rec.Timestamp == "" {
		t.Errorf("pending-push timestamp empty")
	}

	// Sanity: the commit must NOT be on the bare remote yet — we
	// suppressed the push.
	tree := runOut(t, remote.Path, "git", "log", "--format=%H", "main")
	if strings.Contains(tree, headSHA) {
		t.Errorf("offline commit %s reached remote prematurely:\n%s", headSHA, tree)
	}
}

// TestOffline_NonOfflineFlushesPendingBeforeOwnPush — AC2.
//
// Two sequential creates: first --offline, then plain (no --offline).
// After the second create returns:
//   - .act/.pending-pushes is empty.
//   - Both commits are reachable on the bare remote.
func TestOffline_NonOfflineFlushesPendingBeforeOwnPush(t *testing.T) {
	gitops.ResetPushAttemptCounter()
	root, remote := makeRepoWithRemoteOrigin(t)
	paths := config.Layout(root)

	// First call: --offline. Commit lands locally; .pending-pushes
	// gains an entry; no push happens.
	out1, code := RunCreate(root, CreateOptions{Title: "offline-first", Type: "task", Offline: true})
	if code != 0 {
		t.Fatalf("first (offline) create: code=%d, out=%+v", code, out1)
	}
	headAfterOffline := strings.TrimSpace(runOut(t, paths.Root, "git", "rev-parse", "HEAD"))

	// Sanity precondition.
	if _, err := os.Stat(filepath.Join(paths.Root, ".pending-pushes")); err != nil {
		t.Fatalf(".pending-pushes missing after first offline create: %v", err)
	}

	// Second call: non-offline. Flush triggers BEFORE this commit's
	// own push. Both commits should land on the remote.
	out2, code := RunCreate(root, CreateOptions{Title: "online-second", Type: "task"})
	if code != 0 {
		t.Fatalf("second (online) create: code=%d, out=%+v", code, out2)
	}
	headAfterOnline := strings.TrimSpace(runOut(t, paths.Root, "git", "rev-parse", "HEAD"))

	// .pending-pushes must be empty (truncated by the flush).
	body, err := os.ReadFile(filepath.Join(paths.Root, ".pending-pushes"))
	if err != nil {
		t.Fatalf("read .pending-pushes: %v", err)
	}
	if len(strings.TrimSpace(string(body))) != 0 {
		t.Errorf(".pending-pushes not empty after non-offline flush:\n%s", body)
	}

	// Both commits visible on the bare remote.
	remoteLog := runOut(t, remote.Path, "git", "log", "--format=%H", "main")
	if !strings.Contains(remoteLog, headAfterOffline) {
		t.Errorf("offline commit %s missing from remote:\n%s", headAfterOffline, remoteLog)
	}
	if !strings.Contains(remoteLog, headAfterOnline) {
		t.Errorf("online commit %s missing from remote:\n%s", headAfterOnline, remoteLog)
	}
}
