package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// commitAllRE matches a `git commit` invocation that stages every modified
// tracked file: `-a`, `-am`, `--all`. Matching is anchored on the literal
// `git commit` so prose that merely *names* the flag while forbidding it
// ("don't commit with -a") does not trip the check.
var commitAllRE = regexp.MustCompile(`git commit[^\n]*(\s-a\b|\s-am\b|\s--all\b)`)

// TestDocClaim_Docs_NoCommitAllInCanonicalLoop is an absence property
// (act-57e743): none of act's own instructional surfaces may teach the
// work commit with a commit-all flag.
//
// Why it matters: act's canonical loop runs in checkouts that several
// agent sessions share concurrently (the normal parallel-agent setup),
// where `git commit -a` commits whatever a *sibling* session happens to
// have dirty in the tree. That has already swept unrelated in-flight
// edits into act-marked commits twice. The safe form names the paths
// explicitly after `--`.
//
// Registered via unregisteredDocClaimOptOut in docs_sweep_test.go — the
// registry's positive-substring model doesn't fit an absence property.
func TestDocClaim_Docs_NoCommitAllInCanonicalLoop(t *testing.T) {
	root := repoRootForDocClaim(t)
	surfaces := []string{
		"cmd/act/help.go",
		"skills/act/SKILL.md",
		"README.md",
	}
	for _, rel := range surfaces {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if commitAllRE.MatchString(line) {
				t.Errorf("%s:%d teaches a commit-all form: %q\n"+
					"  Use an explicit pathspec instead: git commit -m \"...\" -m \"Act-Id: ...\" -- path/one path/two\n"+
					"  Rationale: in a checkout shared by concurrent sessions, -a commits another session's dirty files.",
					rel, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestDocClaim_Docs_CommitPathspecRationale pins the *positive* half of
// act-57e743: each surface that shows the work commit must also say why
// explicit paths matter, so an agent reading it cold understands the
// constraint rather than reformatting the command back to `-a`.
func TestDocClaim_Docs_CommitPathspecRationale(t *testing.T) {
	root := repoRootForDocClaim(t)
	cases := []struct {
		file     string
		mustHave string
	}{
		{"cmd/act/help.go", "share one checkout"},
		{"skills/act/SKILL.md", "share one checkout"},
	}
	for _, c := range cases {
		b, err := os.ReadFile(filepath.Join(root, c.file))
		if err != nil {
			t.Fatalf("read %s: %v", c.file, err)
		}
		if !strings.Contains(string(b), c.mustHave) {
			t.Errorf("%s: missing the shared-checkout rationale for explicit commit pathspecs (looked for %q)", c.file, c.mustHave)
		}
	}
}
