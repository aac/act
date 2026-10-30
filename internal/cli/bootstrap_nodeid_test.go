package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/config"
)

// TestBootstrapWorker_CopyModesMintOwnNodeID: both copy modes of
// bootstrap-worker / state import carry the source's .act/.git (and with it
// act.nodeId) into the target; the target must still resolve its own
// node_id, stored against its own state root, so the worker and the
// checkout it was copied from are distinct claimants.
func TestBootstrapWorker_CopyModesMintOwnNodeID(t *testing.T) {
	for _, mode := range []string{"from", "from-cwd"} {
		t.Run(mode, func(t *testing.T) {
			srcRoot, _ := makeBootstrapSource(t)
			srcID := readNodeID(t, srcRoot)

			targetRoot := makeBootstrapTarget(t)
			opts := BootstrapWorkerOptions{SourceCWD: srcRoot, Target: targetRoot}
			if mode == "from-cwd" {
				opts = BootstrapWorkerOptions{FromCWDSourcePath: srcRoot, Target: targetRoot}
			}
			if out, code := RunBootstrapWorker(opts); code != 0 {
				t.Fatalf("bootstrap (%s) code=%d out=%+v", mode, code, out)
			}

			tgtPaths := config.Layout(targetRoot)
			cfgPath := config.ActGitConfigPath(tgtPaths.Root)
			stored, _ := config.GetGitConfig(cfgPath, config.NodeIDKey)
			if stored == "" || stored == srcID {
				t.Fatalf("target act.nodeId = %q right after bootstrap; want its own, not the source's %q", stored, srcID)
			}
			if got := readNodeID(t, targetRoot); got != stored {
				t.Fatalf("target resolves node_id %q, want the stored %q", got, stored)
			}
			if again := readNodeID(t, srcRoot); again != srcID {
				t.Fatalf("source node_id changed from %q to %q after bootstrap", srcID, again)
			}
		})
	}
}

// TestBootstrapWorker_CopyOmitsNodeIDLock: the source's act-nodeid.lock is
// checkout-local state and must not appear in either copy mode's target.
func TestBootstrapWorker_CopyOmitsNodeIDLock(t *testing.T) {
	for _, mode := range []string{"from", "from-cwd"} {
		t.Run(mode, func(t *testing.T) {
			srcRoot, _ := makeBootstrapSource(t)
			nlkLock := filepath.Join(config.Layout(srcRoot).Root, ".git", config.NodeIDLockFile)
			if err := os.WriteFile(nlkLock, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			targetRoot := makeBootstrapTarget(t)
			opts := BootstrapWorkerOptions{SourceCWD: srcRoot, Target: targetRoot}
			if mode == "from-cwd" {
				opts = BootstrapWorkerOptions{FromCWDSourcePath: srcRoot, Target: targetRoot}
			}
			if out, code := RunBootstrapWorker(opts); code != 0 {
				t.Fatalf("bootstrap (%s) code=%d out=%+v", mode, code, out)
			}
			got := filepath.Join(config.Layout(targetRoot).Root, ".git", config.NodeIDLockFile)
			if _, err := os.Stat(got); !os.IsNotExist(err) {
				t.Fatalf("target has %s (stat err=%v); want it excluded from the copy", got, err)
			}
		})
	}
}
