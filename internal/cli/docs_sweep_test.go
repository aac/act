package cli

// Doc-vs-implementation sweep test (act-ff5c).
//
// This is the lightest-weight option from the issue: a registry of
// (doc surface, claim pattern, asserting-test name) tuples plus a Go
// test that, on every `go test ./...` invocation, verifies:
//
//   1. The claim pattern is present in the named doc file (drift on
//      the doc side: if someone removes "prefix ok" from a flag-help
//      string, the registry entry is now lying about what the doc says
//      and the test fails — either re-introduce the claim, or delete
//      the registry entry).
//
//   2. A test function with the named `TestDocClaim_*` symbol exists
//      somewhere in the test corpus (drift on the test side: if the
//      asserting test is deleted or renamed without updating the
//      registry, the claim has lost its enforcement and this test
//      surfaces that).
//
// What this catches: the act-6fca and act-ac52 shape — doc claim and
// implementation drift apart with no automated signal. What it does
// NOT catch: a TestDocClaim_X that exists but doesn't actually assert
// anything meaningful. That's a code-review concern; the sweep is
// for orphan-detection.
//
// Scope: ~300 LoC ceiling. The registry is hand-maintained. The
// alternative (static analyzer extracting claims from prose) would
// surface every English sentence in every doc as a candidate; the
// false-positive rate makes it useless.
//
// To add a new tracked claim: append a `docClaim` entry below AND
// write a matching `TestDocClaim_*` test in docclaim_test.go (or
// another *_test.go in either internal/cli/ or cmd/act/).

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docClaim describes one user-visible behavior claim. The claim is
// "tracked" in the sense that two layers of drift (doc edits, test
// edits) become a build break.
type docClaim struct {
	// name is a short identifier used only in test failure messages.
	name string

	// docFile is the doc surface relative to the repo root.
	docFile string

	// claimPattern is a literal substring that must appear in docFile.
	// We use literal string match (not regex) to keep the registry
	// readable; if a regex is needed, prefer adding a new tuple over
	// generalising this struct.
	claimPattern string

	// testName is the symbol of the asserting test. Must start with
	// "TestDocClaim_" to make the convention searchable; the sweep
	// rejects entries that don't.
	testName string
}

// docClaimRegistry is the source of truth for tracked claims. New
// claims go here in the same commit that adds the doc edit and the
// asserting test.
//
// Order is alphabetical by `name` for readability; the sweep does not
// depend on it.
var docClaimRegistry = []docClaim{
	{
		name:         "act-help-go-install",
		docFile:      "README.md",
		claimPattern: "go install github.com/aac/act/cmd/act@latest",
		testName:     "TestDocClaim_GoInstallPath",
	},
	{
		name:         "act-help-subcommands-listing",
		docFile:      "cmd/act/help.go",
		claimPattern: "init version log list search ready mine show",
		testName:     "TestDocClaim_ActHelpListsSubcommands",
	},
	{
		name:         "act-help-bootstrap-worker-subcommand",
		docFile:      "cmd/act/help.go",
		claimPattern: "bootstrap-worker",
		testName:     "TestDocClaim_BootstrapWorker_HelpListsSubcommand",
	},
	{
		name:         "act-help-harvest-subcommand",
		docFile:      "cmd/act/help.go",
		claimPattern: "act harvest <worker-path>",
		testName:     "TestDocClaim_Harvest_HelpListsSubcommand",
	},
	{
		name:         "canonical-loop-git-push",
		docFile:      "cmd/act/help.go",
		claimPattern: "git push",
		testName:     "TestDocClaim_CanonicalLoop_HelpOverviewIncludesGitPush",
	},
	{
		name:         "commit-marker-trailer-form",
		docFile:      "cmd/act/help.go",
		claimPattern: "Act-Id: act-XXXX",
		testName:     "TestDocClaim_CommitMarker_TrailerFormAndDoctorAttribution",
	},
	{
		name:         "commit-marker-historical-back-compat",
		docFile:      "cmd/act/help.go",
		claimPattern: "back-compat",
		testName:     "TestDocClaim_CommitMarker_HistoricalSubjectFormStillAttributed",
	},
	{
		name:         "error-envelope-id-ambiguous",
		docFile:      "cmd/act/help.go",
		claimPattern: "id_ambiguous",
		testName:     "TestDocClaim_AmbiguousPrefix_ExitsTwoWithIdAmbiguous",
	},
	{
		name:         "prefix-ok-under-flag",
		docFile:      "cmd/act/ready.go",
		claimPattern: "(prefix ok)",
		testName:     "TestDocClaim_PrefixOk_TwoCharUniquePrefixResolves",
	},
	{
		name:         "prefix-ok-create-parent",
		docFile:      "cmd/act/create.go",
		claimPattern: "full or unique prefix",
		testName:     "TestDocClaim_PrefixOk_TwoCharUniquePrefixResolves",
	},
	// skill-worker-* (act-9e7078): the worker-protocol section in the
	// embedded SKILL.md tells dispatched sub-agents (a) that the
	// orchestrator pre-seeds .act/ via bootstrap-worker before launch and
	// (b) that the orchestrator harvests ops at teardown — so workers can
	// run the canonical loop locally without mid-flight coordination. If
	// either subcommand reference is dropped from the skill, a cold-start
	// worker reads no doc and might invent its own coordination protocol
	// (push from worktree, mid-flight rsync, etc.) — exactly the kind of
	// drift the sweep catches.
	//
	// The orchestrate command itself (~/.claude/commands/orchestrate.md)
	// ALSO makes load-bearing claims about bootstrap-on-dispatch and
	// harvest-on-teardown, but that file lives OUTSIDE this repo
	// (claude-config). The sweep harness resolves docFile relative to
	// the act repo root only (see repoRootForDocClaim in
	// docclaim_test.go) and has no mechanism to reach into another git
	// repo. Adding outside-repo entries would either silently no-op or
	// blow up filepath.Join. The claude-config repo would need its own
	// equivalent sweep to enforce those claims; cross-repo doc-claim
	// enforcement is out of scope for this registry.
	{
		name:         "skill-worker-bootstrap-ref",
		docFile:      "internal/skill/SKILL.md",
		claimPattern: "bootstrap-worker",
		testName:     "TestDocClaim_Skill_MentionsBootstrapWorker",
	},
	{
		name:         "skill-worker-harvest-ref",
		docFile:      "internal/skill/SKILL.md",
		claimPattern: "harvest",
		testName:     "TestDocClaim_Skill_MentionsHarvest",
	},
	{
		name:         "skill-worker-section",
		docFile:      "internal/skill/SKILL.md",
		claimPattern: "Working in a worktree or sandbox",
		testName:     "TestDocClaim_Skill_WorkerProtocolSection",
	},
}

// TestDocSweep_AllClaimsHaveAssertingTests is the meta-test that drives
// the registry. It runs on every `go test ./...`; a fresh agent reading
// the failure message learns both the convention and which entry to
// fix.
func TestDocSweep_AllClaimsHaveAssertingTests(t *testing.T) {
	root := repoRootForDocClaim(t)
	testNames := collectTestNames(t, root)

	for _, c := range docClaimRegistry {
		t.Run(c.name, func(t *testing.T) {
			// 1. Test-name convention: must start with TestDocClaim_.
			if !strings.HasPrefix(c.testName, "TestDocClaim_") {
				t.Fatalf("registry entry %q: testName %q does not start with TestDocClaim_",
					c.name, c.testName)
			}

			// 2. Doc file contains the claim.
			docPath := filepath.Join(root, c.docFile)
			body, err := os.ReadFile(docPath)
			if err != nil {
				t.Fatalf("read %s: %v", c.docFile, err)
			}
			if !strings.Contains(string(body), c.claimPattern) {
				t.Errorf("doc %s no longer contains claim %q\n"+
					"  Either re-introduce the claim or remove the registry entry %q\n"+
					"  (and the corresponding test %s if it has no other purpose).",
					c.docFile, c.claimPattern, c.name, c.testName)
			}

			// 3. Asserting test exists in the corpus.
			if !testNames[c.testName] {
				t.Errorf("no test function named %s found under %s\n"+
					"  The claim %q in %s is not enforced by any TestDocClaim_*.\n"+
					"  Add the test, or remove the registry entry %q if the claim is no longer load-bearing.",
					c.testName, root, c.claimPattern, c.docFile, c.name)
			}
		})
	}
}

// TestDocSweep_NoOrphanedDocClaimTests is the inverse pass: every
// TestDocClaim_* function found under the repo must be referenced by
// some registry entry, OR it must be a doc-claim helper (we allow
// shared assertions referenced from multiple registry entries — the
// PrefixOk_* test covers two registry entries). The cap protects
// against tests that *look like* they assert a doc claim but are
// silently disconnected from any tracked surface.
func TestDocSweep_NoOrphanedDocClaimTests(t *testing.T) {
	root := repoRootForDocClaim(t)
	testNames := collectTestNames(t, root)

	registered := map[string]bool{}
	for _, c := range docClaimRegistry {
		registered[c.testName] = true
	}
	var orphans []string
	for name := range testNames {
		if !strings.HasPrefix(name, "TestDocClaim_") {
			continue
		}
		if !registered[name] {
			orphans = append(orphans, name)
		}
	}
	if len(orphans) > 0 {
		t.Errorf("orphaned TestDocClaim_* tests (no registry entry references them): %v\n"+
			"  Either add a docClaimRegistry entry pointing at the doc claim, or rename "+
			"the test if it isn't actually asserting a tracked doc claim.", orphans)
	}
}

// collectTestNames walks the repo and returns the set of `func TestXxx`
// names declared in any *_test.go file under the project root. We
// deliberately don't parse the AST — a regex over file contents is
// sufficient and avoids the go/ast import cost.
//
// Files under hidden dirs (.git, .act, .claude) and bin/ are skipped.
func collectTestNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	funcRE := regexp.MustCompile(`(?m)^func\s+(Test[A-Za-z0-9_]+)\s*\(`)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		base := filepath.Base(path)
		if info.IsDir() {
			if base == ".git" || base == ".act" || base == ".claude" ||
				base == "bin" || base == "node_modules" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(base, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range funcRE.FindAllStringSubmatch(string(body), -1) {
			names[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return names
}
