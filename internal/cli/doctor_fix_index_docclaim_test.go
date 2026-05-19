package cli

// Doc-claim test for act-f2f93a: the literal remediation hint
// `rebuild with 'act doctor --fix-index'` is emitted as the
// index-malformed finding Message on a corrupt index detected without
// --fix-index. The asserting boundary is the public RunDoctor surface
// (not checkIndexMalformed directly) so a refactor that drops or
// rephrases the literal fails this test the same way an agent grepping
// the stderr line would notice.
//
// The docs/spec-v2.md "act doctor index-malformed" section pins this
// literal; docs_sweep_test.go registers the (doc, claim, test) tuple so
// drift on either side surfaces in the sweep.

import (
	"strings"
	"testing"
)

func TestDocClaim_DoctorFixIndex_StderrRemediationHint(t *testing.T) {
	root := makeCreateRepo(t)
	mustCreate(t, root, "A")
	_ = corruptIndexFile(t, root)

	out, code := RunDoctor(root, DoctorOptions{Check: "index-malformed"})
	if code != 1 {
		t.Fatalf("doctor exit = %d, want 1 on malformed index; out=%+v", code, out)
	}
	res := out.(DoctorResult)
	const wantLiteral = "rebuild with 'act doctor --fix-index'"
	for _, f := range res.Findings {
		if f.Check != "index-malformed" {
			continue
		}
		if strings.Contains(f.Message, wantLiteral) {
			return
		}
	}
	t.Errorf("no index-malformed finding contained literal %q; got findings %+v",
		wantLiteral, res.Findings)
}
