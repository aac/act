package cli

import (
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
