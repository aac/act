package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

var testNodeIDShape = regexp.MustCompile(`^[0-9a-f]{8}$`)

// nestedRepo makes <dir>/.act with a git-initialised nested repo and returns
// its layout.
func nestedRepo(t *testing.T) LayoutPaths {
	t.Helper()
	paths := Layout(t.TempDir())
	if err := os.MkdirAll(paths.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", paths.Root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	return paths
}

func TestNodeID_MintedOnceAndStable(t *testing.T) {
	paths := nestedRepo(t)
	id1, err := ResolveNodeID(paths)
	if err != nil {
		t.Fatal(err)
	}
	if !testNodeIDShape.MatchString(id1) {
		t.Fatalf("node_id %q is not 8 lowercase hex", id1)
	}
	stored, err := GetGitConfig(ActGitConfigPath(paths.Root), NodeIDKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored != id1 {
		t.Fatalf("act.nodeId in .act/.git/config = %q, want %q", stored, id1)
	}
	storedPath, _ := GetGitConfig(ActGitConfigPath(paths.Root), NodeIDPathKey)
	if storedPath != canonicalStateRoot(paths.Root) {
		t.Fatalf("act.nodeIdPath = %q, want %q", storedPath, canonicalStateRoot(paths.Root))
	}
	for i := 0; i < 3; i++ {
		id, err := ResolveNodeID(paths)
		if err != nil {
			t.Fatal(err)
		}
		if id != id1 {
			t.Fatalf("resolve #%d returned %q, want stable %q", i+2, id, id1)
		}
	}
}

func TestNodeID_RemintedWhenPathDiffers(t *testing.T) {
	paths := nestedRepo(t)
	cfg := ActGitConfigPath(paths.Root)
	if err := SetGitConfig(cfg, NodeIDKey, "abcdef01"); err != nil {
		t.Fatal(err)
	}
	if err := SetGitConfig(cfg, NodeIDPathKey, "/somewhere/else/.act"); err != nil {
		t.Fatal(err)
	}
	id, err := ResolveNodeID(paths)
	if err != nil {
		t.Fatal(err)
	}
	if id == "abcdef01" {
		t.Fatalf("node_id copied from another checkout was kept; want a fresh one")
	}
	if !testNodeIDShape.MatchString(id) {
		t.Fatalf("node_id %q is not 8 lowercase hex", id)
	}
	gotPath, _ := GetGitConfig(cfg, NodeIDPathKey)
	if gotPath != canonicalStateRoot(paths.Root) {
		t.Fatalf("act.nodeIdPath = %q, want %q", gotPath, canonicalStateRoot(paths.Root))
	}
	again, _ := ResolveNodeID(paths)
	if again != id {
		t.Fatalf("re-minted id not stable: %q then %q", id, again)
	}
}

// TestNodeID_SymlinkedRootKeepsIdentity: the same checkout reached through a
// symlinked spelling must not re-mint, or its own re-claim would lose to
// itself.
func TestNodeID_SymlinkedRootKeepsIdentity(t *testing.T) {
	paths := nestedRepo(t)
	id, err := ResolveNodeID(paths)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Dir(paths.Root), link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	via, err := ResolveNodeID(Layout(link))
	if err != nil {
		t.Fatal(err)
	}
	if via != id {
		t.Fatalf("symlinked spelling got %q, want %q", via, id)
	}
}

func TestNodeID_NoNestedGitFallsBackWithoutWriting(t *testing.T) {
	paths := Layout(t.TempDir())
	if err := os.MkdirAll(paths.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	id1, err := ResolveNodeID(paths)
	if err != nil {
		t.Fatal(err)
	}
	if !testNodeIDShape.MatchString(id1) {
		t.Fatalf("fallback node_id %q is not 8 lowercase hex", id1)
	}
	id2, _ := ResolveNodeID(paths)
	if id1 != id2 {
		t.Fatalf("fallback not deterministic: %q vs %q", id1, id2)
	}
	if _, err := os.Stat(filepath.Join(paths.Root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("fallback wrote a .git entry (stat err=%v)", err)
	}
	entries, _ := os.ReadDir(paths.Root)
	if len(entries) != 0 {
		t.Fatalf("fallback wrote files into .act/: %v", entries)
	}
}
