package cli

// Phase 2 doc-claim regression test (act-95bc5c).
//
// docs/coordination-plane-design.md's "Phase 1.5 → Phase 2
// cutover" section names the one-time operator setup
// (`act remote enable`, `act remote add-upstream`) and the rollback
// path. This pins that section header.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocClaim_Phase2Design_Cutover pins the cutover section header in
// the Phase 2 design doc. An operator enabling Phase 2 on a project must
// land on a section with this name; drift makes the procedure unfindable
// from the table-of-contents scan.
func TestDocClaim_Phase2Design_Cutover(t *testing.T) {
	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "docs/coordination-plane-design.md"))
	if err != nil {
		t.Fatalf("read coordination-plane-design.md: %v", err)
	}
	const want = "Phase 1.5 → Phase 2 cutover"
	if !strings.Contains(string(body), want) {
		t.Errorf("docs/coordination-plane-design.md no longer contains section header %q\n"+
			"  The cutover procedure for Phase 2 depends on this header.",
			want)
	}
}
