package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/config"
)

// fakeNow returns a deterministic time func suitable for RunInit.
func fakeNow(t time.Time) func() time.Time { return func() time.Time { return t } }

// makeRepo creates a tempdir with a `.git/` directory and returns its path.
func makeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	return root
}

func TestRunInit_HappyPath(t *testing.T) {
	root := makeRepo(t)
	out, code := RunInit(root, false, false, "machine-abc", "alice@example.com",
		fakeNow(time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}

	succ, ok := out.(successOutput)
	if !ok {
		t.Fatalf("output type = %T, want successOutput", out)
	}
	if !succ.OK {
		t.Errorf("ok = false")
	}
	if succ.ActDir != filepath.Join(root, ".act") {
		t.Errorf("act_dir = %q, want %q", succ.ActDir, filepath.Join(root, ".act"))
	}
	if len(succ.NodeID) != 8 {
		t.Errorf("node_id = %q, want 8 hex", succ.NodeID)
	}

	paths := config.Layout(root)
	for _, dir := range []string{paths.Root, paths.Ops, paths.Snapshots, paths.Hooks, paths.Imports} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Errorf("missing dir %s: %v", dir, err)
			continue
		}
		if !fi.IsDir() {
			t.Errorf("%s is not a dir", dir)
		}
	}

	cfg, err := config.ReadConfig(paths)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if cfg.NodeID != succ.NodeID {
		t.Errorf("config node_id = %q, want %q", cfg.NodeID, succ.NodeID)
	}
	if cfg.Version != "0.1.0" {
		t.Errorf("version = %q, want 0.1.0", cfg.Version)
	}
	if cfg.CreatedAt != "2026-04-29T12:00:00.000Z" {
		t.Errorf("created_at = %q", cfg.CreatedAt)
	}
	if cfg.LastHLC != (config.HLCState{}) {
		t.Errorf("last_hlc = %+v, want zero", cfg.LastHLC)
	}
}

func TestRunInit_NoGit(t *testing.T) {
	// Use a deeply nested tempdir so no ancestor up to / has a .git/.
	// t.TempDir is guaranteed under the OS temp dir which has no .git.
	root := t.TempDir()
	// Defensive: avoid false positives if the test host has .git in /.
	if hasGitDir(root) {
		t.Skip("test host has .git/ on an ancestor of the temp dir")
	}
	out, code := RunInit(root, false, false, "m", "e", nil)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	e, ok := out.(errorOutput)
	if !ok {
		t.Fatalf("output type = %T, want errorOutput", out)
	}
	if e.Error != "not_in_git" {
		t.Errorf("error = %q, want not_in_git", e.Error)
	}
}

func TestRunInit_RejectsReinitWithoutForce(t *testing.T) {
	root := makeRepo(t)
	if _, code := RunInit(root, false, false, "m", "e", nil); code != 0 {
		t.Fatalf("first init code = %d", code)
	}
	out, code := RunInit(root, false, false, "m", "e", nil)
	if code != 1 {
		t.Fatalf("second init code = %d, want 1", code)
	}
	e, ok := out.(errorOutput)
	if !ok {
		t.Fatalf("type %T", out)
	}
	if e.Error != "act_already_initialized" {
		t.Errorf("error = %q", e.Error)
	}
}

func TestRunInit_ForceReinitOverwrites(t *testing.T) {
	root := makeRepo(t)
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, code := RunInit(root, false, false, "m", "e", fakeNow(t1)); code != 0 {
		t.Fatalf("first init code = %d", code)
	}

	t2 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	_, code := RunInit(root, true, false, "m2", "e2", fakeNow(t2))
	if code != 0 {
		t.Fatalf("force re-init code = %d, want 0", code)
	}

	cfg, err := config.ReadConfig(config.Layout(root))
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if cfg.CreatedAt != "2026-06-01T00:00:00.000Z" {
		t.Errorf("created_at = %q, want overwritten value", cfg.CreatedAt)
	}
	if cfg.NodeID != config.ComputeNodeID("m2", "e2") {
		t.Errorf("node_id was not overwritten: %q", cfg.NodeID)
	}
}

func TestRunInit_GitignoreAppendIdempotent(t *testing.T) {
	root := makeRepo(t)
	if _, code := RunInit(root, false, false, "m", "e", nil); code != 0 {
		t.Fatalf("first init code = %d", code)
	}
	gi := filepath.Join(root, ".gitignore")
	first, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if !strings.Contains(string(first), ".act/index.db") {
		t.Errorf("gitignore missing .act/index.db: %q", string(first))
	}

	// Second init with --force should not duplicate the entry.
	if _, code := RunInit(root, true, false, "m", "e", nil); code != 0 {
		t.Fatalf("second init code = %d", code)
	}
	second, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if got := strings.Count(string(second), ".act/index.db"); got != 1 {
		t.Errorf(".act/index.db appears %d times, want 1; content=%q", got, string(second))
	}
}

func TestRunInit_GitignorePreservesExisting(t *testing.T) {
	root := makeRepo(t)
	gi := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(gi, []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatalf("seed gitignore: %v", err)
	}
	if _, code := RunInit(root, false, false, "m", "e", nil); code != 0 {
		t.Fatalf("init code = %d", code)
	}
	got, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "node_modules/") {
		t.Errorf("existing entry lost: %q", string(got))
	}
	if !strings.Contains(string(got), ".act/index.db") {
		t.Errorf("new entry missing: %q", string(got))
	}
}

func TestRunInit_OutputJSONShape(t *testing.T) {
	root := makeRepo(t)
	out, code := RunInit(root, false, false, "m", "alice@example.com", nil)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"ok", "act_dir", "node_id"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("missing key %q in %s", key, data)
		}
	}
	if decoded["ok"] != true {
		t.Errorf("ok = %v", decoded["ok"])
	}
	if s, ok := decoded["node_id"].(string); !ok || len(s) != 8 {
		t.Errorf("node_id shape: %v", decoded["node_id"])
	}
}

func TestRunInit_ErrorJSONShape(t *testing.T) {
	root := t.TempDir()
	if hasGitDir(root) {
		t.Skip("temp dir has ancestor .git")
	}
	out, code := RunInit(root, false, false, "m", "e", nil)
	if code != 3 {
		t.Fatalf("code = %d", code)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["error"] != "not_in_git" {
		t.Errorf("error = %v", decoded["error"])
	}
	if _, ok := decoded["message"].(string); !ok {
		t.Errorf("message missing/non-string in %s", data)
	}
}
