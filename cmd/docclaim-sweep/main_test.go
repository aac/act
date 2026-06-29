package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractFlagDefs_RunsOnRealCmdAct asserts the analyzer can parse
// every cmd/act/*.go file without error and finds a non-trivial number
// of flag definitions. The exact count is intentionally not pinned —
// it changes whenever a new subcommand or flag is added; we only want
// to catch a regression where the AST walker stops finding fs.* calls
// entirely (e.g. recv-name renamed from `fs`, or the file pattern
// stopped matching).
func TestExtractFlagDefs_RunsOnRealCmdAct(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}
	defs, err := extractFlagDefs(filepath.Join(root, "cmd", "act"))
	if err != nil {
		t.Fatalf("extractFlagDefs: %v", err)
	}
	if len(defs) < 20 {
		t.Fatalf("expected at least 20 flag defs in cmd/act/*.go, got %d", len(defs))
	}

	// Spot check: there's a known --offline flag in cmd/act/create.go
	// with the load-bearing "commit locally, skip push" help string.
	// If that disappears the registry sweep would also catch it, but
	// we want this test to be a smoke check on the AST walker itself.
	found := false
	for _, d := range defs {
		if d.File == "create.go" && d.FlagName == "offline" &&
			strings.Contains(d.Help, "commit locally, skip push") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find create.go --offline flag with 'commit locally, skip push' help")
	}
}

// TestExtractClaimPatterns_RunsOnRealRegistry asserts the registry
// parser pulls a reasonable number of claimPattern entries out of the
// real docs_sweep_test.go. Like the flag-defs test, the count is not
// pinned — only the "did the parser find anything at all" assertion.
func TestExtractClaimPatterns_RunsOnRealRegistry(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}
	claims, err := extractClaimPatterns(filepath.Join(root, "internal", "cli", "docs_sweep_test.go"))
	if err != nil {
		t.Fatalf("extractClaimPatterns: %v", err)
	}
	if len(claims) < 10 {
		t.Fatalf("expected at least 10 claim patterns, got %d", len(claims))
	}

	// Spot check: the offline-flag-help entry must be present, because
	// the docclaim sweep we just ran depends on it matching create.go's
	// --offline flag.
	found := false
	for _, c := range claims {
		if c.Name == "offline-flag-help" && c.Pattern == "commit locally, skip push" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected offline-flag-help claim with pattern 'commit locally, skip push'")
	}
}

// TestBuildReport_MatchesClaimedFlags asserts the matching layer wires
// flag-def help-strings against registry patterns at the substring
// boundary the sweep test itself uses.
func TestBuildReport_MatchesClaimedFlags(t *testing.T) {
	flags := []flagDef{
		{File: "create.go", Line: 1, FuncName: "fs.Bool", FlagName: "offline", Help: "commit locally, skip push; record in .act/.pending-pushes"},
		{File: "create.go", Line: 2, FuncName: "fs.Bool", FlagName: "json", Help: "emit JSON output"},
	}
	claims := []claimEntry{
		{Name: "offline-flag-help", Pattern: "commit locally, skip push"},
	}
	rep := buildReport(flags, claims)
	if rep.Total != 2 || rep.Claimed != 1 || rep.Orphans != 1 {
		t.Fatalf("unexpected counts: %+v", rep)
	}
	if !rep.Flags[0].Claimed || rep.Flags[0].MatchedBy[0] != "offline-flag-help" {
		t.Errorf("offline flag should have matched offline-flag-help: %+v", rep.Flags[0])
	}
	if rep.Flags[1].Claimed {
		t.Errorf("json flag should be orphan: %+v", rep.Flags[1])
	}
}
