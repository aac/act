package cli

// Doc-claim regression tests (act-ff5c).
//
// Every test in this file asserts a *user-visible behavior claim* made
// in a doc surface (act help text, --help string, README, CLAUDE.md,
// docs/spec-v2.md) at the boundary an agent would actually hit. Internal
// unit tests live elsewhere; this file is for tests whose failure means
// "doc says X, binary does Y" — the drift shape that bit act-6fca and
// act-ac52.
//
// Naming: every test starts with `TestDocClaim_`. The sweep test in
// docs_sweep_test.go has a registry that pins each tracked doc claim to
// the test function name asserting it; adding a claim without a matching
// `TestDocClaim_*` (or vice-versa) is a build break.
//
// Pattern: drive the actBinary subprocess (built by TestMain in
// concurrent_helper_test.go). This is load-bearing — both prior bugs
// passed internal-state tests because the failure was upstream of the
// asserted boundary. Subprocess + actual git log + actual exit code is
// the same surface a cold-start agent hits.

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestDocClaim_PrefixOk_TwoCharUniquePrefixResolves pins the canonical
// "prefix ok" promise made in every flag-help string for `<id>`
// arguments. The act-6fca bug was that this claim was true at the
// resolver level but false at the CLI surface — a length floor in the
// command's pre-resolution check rejected 2/3-char prefixes before they
// ever reached ResolvePrefix. The fix removed the floor; this test
// keeps it removed.
//
// The boundary asserted is the same one the original bug surfaced at:
// `act show <prefix>` exit code and stdout. Anything internal — the
// resolver's behavior on a string, the index's lookup logic — is the
// proxy that failed silently last time.
func TestDocClaim_PrefixOk_TwoCharUniquePrefixResolves(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	// Create an issue and pull its id from --json. The id's short form
	// is what an agent would type after `act ready`; we drive a strict
	// 2-char hex prefix to assert the doc claim end-to-end.
	createOut, _ := mustRunAct(t, site, 0, "create", "doc claim probe", "--json")
	id := pickIDFromJSON(t, createOut)

	hex := strings.TrimPrefix(id, "act-")
	if len(hex) < 4 {
		t.Fatalf("id %q has hex shorter than 4 chars; cannot probe a 2-char prefix", id)
	}
	for _, n := range []int{2, 3} {
		short := "act-" + hex[:n]
		out, _ := mustRunAct(t, site, 0, "show", short, "--json")
		if !strings.Contains(out, `"id":"`+id+`"`) && !strings.Contains(out, `"id": "`+id+`"`) {
			t.Errorf("show %s: stdout missing id %q\n%s", short, id, out)
		}
	}
}

// TestDocClaim_CanonicalLoop_HelpOverviewIncludesGitPush pins the
// canonical-loop step that act-ac52 was filed over: `act help` must
// name `git push` as a step. The previous bug was that CLAUDE.md's
// loop omitted it; a similar omission in the binary's own tutorial
// (which is what a fresh agent reads via `act help`) would re-create
// the failure mode for any project that doesn't override the tutorial
// in its own CLAUDE.md.
//
// Asserted at the subprocess stdout boundary, not on the helpOverview
// const, so a refactor that splits the constant into multiple chunks
// or pulls it from a file still has to keep `git push` in the loop.
func TestDocClaim_CanonicalLoop_HelpOverviewIncludesGitPush(t *testing.T) {
	site := t.TempDir()
	// `act help` does not need a git repo; runAct just exec's the
	// binary and reads stdout. Using a temp dir keeps cwd predictable.
	out, _ := mustRunAct(t, site, 0, "help")

	// The loop is delimited by "THE CANONICAL WORK LOOP" and the next
	// all-caps section header. Pull that slice and check `git push`
	// appears inside it, not just somewhere later in the page.
	const loopStart = "THE CANONICAL WORK LOOP"
	const loopEnd = "WHEN TO FILE FOLLOW-UPS"
	startIdx := strings.Index(out, loopStart)
	endIdx := strings.Index(out, loopEnd)
	if startIdx < 0 || endIdx < 0 || endIdx <= startIdx {
		t.Fatalf("could not isolate canonical-loop section: start=%d end=%d", startIdx, endIdx)
	}
	loop := out[startIdx:endIdx]
	if !strings.Contains(loop, "git push") {
		t.Errorf("canonical loop section missing `git push`:\n%s", loop)
	}
}

// TestDocClaim_CommitMarker_AppearsInGitLogAfterCreate pins the
// commit-marker contract claimed in `act help workflow` ("the work
// commit's message must embed the issue's commit_marker") and surfaced
// for cold-start agents via `act show <id> --commit-marker`. The
// double-prefix bug (act-d3a5 era) passed every op-file test because
// the assertion was on bytes inside the envelope, not on the literal
// `git log -1 --format=%s` string the doctor greps.
//
// This is a thin doctest layer on top of TestCommitFormat_Create in
// commit_format_test.go; that test asserts the canonical subject
// regex, this one asserts the contract a doc reader would extract:
// after `act create`, `git log -1 --format=%s` contains `(act-XXXX)`
// somewhere, with the marker matching what `--commit-marker` returns.
func TestDocClaim_CommitMarker_AppearsInGitLogAfterCreate(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	createOut, _ := mustRunAct(t, site, 0, "create", "marker probe", "--json")
	id := pickIDFromJSON(t, createOut)

	// `act show <id> --commit-marker` is the canonical accessor the
	// help text tells agents to use. Pull it and compare to git log.
	markerOut, _ := mustRunAct(t, site, 0, "show", id, "--commit-marker")
	markerOut = strings.TrimSpace(markerOut)
	if !regexp.MustCompile(`^\(act-[0-9a-f]{4,16}\)$`).MatchString(markerOut) {
		t.Fatalf("commit_marker %q does not match canonical shape", markerOut)
	}

	subj := strings.TrimSpace(runOut(t, site, "git", "log", "-1", "--format=%s"))
	if !strings.Contains(subj, markerOut) {
		t.Errorf("git log subject %q does not contain marker %q", subj, markerOut)
	}
}

// TestDocClaim_AmbiguousPrefix_ExitsTwoWithIdAmbiguous pins the
// error-envelope claim from `act help errors` and from CLAUDE.md's
// versioning-rationale entry: an ambiguous short-id prefix returns
// `id_ambiguous` with candidates, not `issue_not_found`, and exits 2
// (usage error) per spec-v2.md's universal exit-code table.
//
// The full error-envelope shape is pinned by tests in
// ambiguous_prefix_test.go that exercise the Go API directly. This
// version is the doctest that asserts the contract at the subprocess
// boundary, which is what a cold-start agent reading `act help errors`
// would expect: `act show <prefix>` produces a JSON envelope with
// error=id_ambiguous and exits 2.
func TestDocClaim_AmbiguousPrefix_ExitsTwoWithIdAmbiguous(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	// Two issues; we need their actual prefixes to share. Easiest
	// path: create N issues and find any two that collide on at
	// least 2 hex chars. With random 64-bit ids, a 2-char prefix
	// collision is extremely likely within a handful of creates.
	ids := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		o, _ := mustRunAct(t, site, 0, "create", "ambig probe", "--json")
		ids = append(ids, pickIDFromJSON(t, o))
	}
	prefix := findShared2CharPrefix(ids)
	if prefix == "" {
		t.Skip("no 2-char prefix collision after 20 creates; rerun if flaky")
	}

	out, _, code := runAct(t, site, "show", prefix, "--json")
	if code != 2 {
		t.Fatalf("show %s --json: exit = %d, want 2; out=%s", prefix, code, out)
	}
	if !strings.Contains(out, `"error":"id_ambiguous"`) && !strings.Contains(out, `"error": "id_ambiguous"`) {
		t.Errorf("show %s --json: stdout missing id_ambiguous code\n%s", prefix, out)
	}
	if !strings.Contains(out, `"candidates"`) {
		t.Errorf("show %s --json: stdout missing candidates field\n%s", prefix, out)
	}
}

// TestDocClaim_ActHelpListsSubcommands pins the "Subcommands:" line in
// `act help` overview. A cold-start agent uses this list to know what
// surface exists without reading source.
func TestDocClaim_ActHelpListsSubcommands(t *testing.T) {
	site := t.TempDir()
	out, _ := mustRunAct(t, site, 0, "help")
	for _, sub := range []string{"init", "create", "ready", "close", "show", "list", "update", "doctor"} {
		if !strings.Contains(out, sub) {
			t.Errorf("act help output missing subcommand listing for %q", sub)
		}
	}
}

// TestDocClaim_GoInstallPath pins the README's getting-started promise:
// `go install github.com/aac/act/cmd/act@latest` is the canonical
// bootstrap. The literal string lives in README.md and `act help`; a
// rename of the import path would change one place and miss the other
// silently. This asserts both surfaces.
func TestDocClaim_GoInstallPath(t *testing.T) {
	root := repoRootForDocClaim(t)

	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	const wantPath = "go install github.com/aac/act/cmd/act@latest"
	if !strings.Contains(string(readme), wantPath) {
		t.Errorf("README.md missing %q", wantPath)
	}

	site := t.TempDir()
	out, _ := mustRunAct(t, site, 0, "help")
	if !strings.Contains(out, wantPath) {
		t.Errorf("act help missing %q", wantPath)
	}
}

// configureSite writes git user config to a fresh repo so commits are
// non-anonymous. Pulled out of smoke_test.go's identically-named
// helper would create a duplicate; we depend on the existing helper.
// (configureSite is defined in smoke_test.go in the same package.)

// pickIDFromJSON extracts the "id" field from a create-style JSON
// response. The shape is `{"id":"act-...","commit_marker":"..."}` — a
// regex is sufficient and keeps the test free of struct definitions.
func pickIDFromJSON(t *testing.T, jsonOut string) string {
	t.Helper()
	m := regexp.MustCompile(`"id"\s*:\s*"(act-[0-9a-f]+)"`).FindStringSubmatch(jsonOut)
	if len(m) != 2 {
		t.Fatalf("could not extract id from JSON:\n%s", jsonOut)
	}
	return m[1]
}

// findShared2CharPrefix returns an `act-XX` prefix string that at least
// two ids share, or "" if none collide. We deliberately stop at 2 chars
// (the most ambiguous useful case) — wider prefixes are easier; tighter
// prefixes are not what an agent would type.
func findShared2CharPrefix(ids []string) string {
	seen := map[string]int{}
	for _, id := range ids {
		hex := strings.TrimPrefix(id, "act-")
		if len(hex) < 2 {
			continue
		}
		seen[hex[:2]]++
	}
	for p, n := range seen {
		if n >= 2 {
			return "act-" + p
		}
	}
	return ""
}

// repoRootForDocClaim returns the repo root inferred from the current
// source file's location (this test file lives at
// internal/cli/docclaim_test.go; the root is two directories up).
func repoRootForDocClaim(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
}
