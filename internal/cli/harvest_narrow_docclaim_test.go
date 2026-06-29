package cli

// Doc-claim test for Phase 2 ticket 8 (act-e31aa1) — `act harvest`
// narrowing for remote-attached workers. The load-bearing claim is the
// literal stderr line emitted when harvest short-circuits because the
// worker pushed its ops directly to the orchestrator during execution.
//
// Surface boundary: this test reads cmd/act/help.go directly (matching
// the docs-sweep registry entry's docFile) AND verifies the constant in
// internal/cli/harvest.go is the same string — so the two sides cannot
// drift apart without the test failing.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocClaim_HarvestNarrow_SkipMessage asserts that the literal stderr
// line "harvest skipped, worker was push-attached" appears in
// cmd/act/help.go (where the harvest help text documents the skip
// behavior) AND is byte-equal to the exported HarvestSkipMessage
// constant in internal/cli/harvest.go.
//
// Why both surfaces: the docs sweep matches the literal in cmd/act/
// help.go; the runtime emits HarvestSkipMessage from harvest.go. If
// someone edits one without the other, this test catches the drift at
// the boundary an agent would consult.
func TestDocClaim_HarvestNarrow_SkipMessage(t *testing.T) {
	root := repoRootForDocClaim(t)
	helpPath := filepath.Join(root, "cmd/act/help.go")
	body, err := os.ReadFile(helpPath)
	if err != nil {
		t.Fatalf("read %s: %v", helpPath, err)
	}
	if !strings.Contains(string(body), HarvestSkipMessage) {
		t.Errorf("cmd/act/help.go does not contain HarvestSkipMessage %q\n"+
			"  Either restore the help-text line that documents the skip,\n"+
			"  or update HarvestSkipMessage to match the help text.",
			HarvestSkipMessage)
	}
	// Sanity: the constant itself must be the documented literal. If
	// someone "fixes" the typo in one place and not the other, this
	// guard fires before the integration test below.
	if HarvestSkipMessage != "harvest skipped, worker was push-attached" {
		t.Errorf("HarvestSkipMessage = %q, want %q",
			HarvestSkipMessage, "harvest skipped, worker was push-attached")
	}
}
