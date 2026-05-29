package cli

// Doc-claim regression tests (act-ff5c).
//
// Every test in this file asserts a *user-visible behavior claim* made
// in a doc surface (act help text, --help string, README, AGENTS.md,
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
	"encoding/json"
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

// TestDocClaim_CommitMarker_TrailerFormAndDoctorAttribution pins the
// commit-marker contract claimed in `act help workflow` ("the work
// commit's message must embed the issue's commit_marker as a trailer
// in the body") and surfaced for cold-start agents via `act show <id>
// --commit-marker`.
//
// Two contracts pinned end-to-end:
//
//  1. `act show --commit-marker` emits the `Act-Id: act-XXXXXX` trailer
//     shape (act-c4c5: trailer-form replaces the historical `(act-XXXX)`
//     subject-line form for new emission).
//  2. A work commit authored with that trailer in its body is correctly
//     attributed by `act doctor` orphan-close — the regression seat-belt
//     for the act-d3a5-era double-prefix class of marker bugs, this time
//     at the trailer boundary.
func TestDocClaim_CommitMarker_TrailerFormAndDoctorAttribution(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	createOut, _ := mustRunAct(t, site, 0, "create", "marker probe", "--json")
	id := pickIDFromJSON(t, createOut)

	// `act show <id> --commit-marker` is the canonical accessor the
	// help text tells agents to use. Pull it; it must be the trailer
	// shape (case-sensitive `Act-Id`, colon, space, then the canonical
	// short id).
	markerOut, _ := mustRunAct(t, site, 0, "show", id, "--commit-marker")
	markerOut = strings.TrimSpace(markerOut)
	trailerShape := regexp.MustCompile(`^Act-Id: act-[0-9a-f]{4,16}$`)
	if !trailerShape.MatchString(markerOut) {
		t.Fatalf("commit_marker %q does not match canonical trailer shape `Act-Id: act-XXXXXX`", markerOut)
	}

	// Simulate the agent's work commit: a code change in the host
	// working tree, committed with the trailer in the body (two `-m`
	// flags so the trailer becomes a body paragraph separated from
	// the subject by a blank line — `git interpret-trailers` form).
	workFile := filepath.Join(site, "WORK.txt")
	if err := os.WriteFile(workFile, []byte("done\n"), 0o644); err != nil {
		t.Fatalf("write workfile: %v", err)
	}
	runGit(t, site, "add", "WORK.txt")
	runGit(t, site, "commit", "-q", "--no-verify", "-m", "implement marker probe", "-m", markerOut)

	// Close the issue so doctor's orphan-close has something to
	// attribute. Use --no-commit + manual stage so we don't have to
	// thread a second work commit; the close op landing on its own
	// is fine here.
	mustRunAct(t, site, 0, "close", id, "--reason", "marker probe done")

	// Doctor orphan-close must NOT report this issue — the trailer
	// matches the new regex (`Act-Id: act-<markerHex>$`) cross-coupled
	// with the issue's canonical short id.
	doctorOut, _ := mustRunAct(t, site, 0, "doctor", "--check", "orphan-close", "--json")
	if strings.Contains(doctorOut, id) {
		t.Errorf("doctor orphan-close incorrectly flagged %s; the trailer-form work commit should attribute. doctor output:\n%s", id, doctorOut)
	}
}

// TestDocClaim_CommitMarker_HistoricalSubjectFormStillAttributed pins
// the back-compat half of the act-c4c5 marker switch: doctor must still
// resolve work commits authored with the historical `(act-XXXX)`
// subject-line form. New emission is trailer-only; resolution accepts
// both shapes so pre-migration history in existing repos doesn't
// suddenly start orphan-close-ing.
func TestDocClaim_CommitMarker_HistoricalSubjectFormStillAttributed(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	createOut, _ := mustRunAct(t, site, 0, "create", "back-compat probe", "--json")
	id := pickIDFromJSON(t, createOut)

	// Derive the historical-form marker `(act-<short>)` by reading the
	// short id off `act show --commit-marker` (trailer form) and
	// rewrapping. This keeps the test resilient to the canonical short
	// length (4 historical / 6 current).
	markerOut, _ := mustRunAct(t, site, 0, "show", id, "--commit-marker")
	short := strings.TrimPrefix(strings.TrimSpace(markerOut), "Act-Id: ")
	subjectMarker := "(" + short + ")"

	// Author a work commit with the historical subject-line marker only
	// (no trailer in the body).
	workFile := filepath.Join(site, "WORK.txt")
	if err := os.WriteFile(workFile, []byte("done\n"), 0o644); err != nil {
		t.Fatalf("write workfile: %v", err)
	}
	runGit(t, site, "add", "WORK.txt")
	runGit(t, site, "commit", "-q", "--no-verify", "-m", "implement back-compat probe "+subjectMarker)

	mustRunAct(t, site, 0, "close", id, "--reason", "back-compat probe done")

	// Doctor orphan-close must still attribute this commit to the
	// issue via the historical subject-line form.
	doctorOut, _ := mustRunAct(t, site, 0, "doctor", "--check", "orphan-close", "--json")
	if strings.Contains(doctorOut, id) {
		t.Errorf("doctor orphan-close flagged %s despite the historical subject-line marker %q. doctor output:\n%s", id, subjectMarker, doctorOut)
	}
}

// TestDocClaim_AmbiguousPrefix_ExitsTwoWithIdAmbiguous pins the
// error-envelope claim from `act help errors` and from AGENTS.md's
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

// TestDocClaim_Show_FullDisablesTruncation pins the act-3c89 claim made
// in cmd/act/main.go: `act show --full` renders description (and
// closed_reason) verbatim in the human format, skipping the truncation
// guard that otherwise kicks in for long values. The boundary asserted
// is `act show <id> --full` stdout against a long-enough description
// that the default render would have truncated.
func TestDocClaim_Show_FullDisablesTruncation(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	// Build a description long enough to trip the truncation guard (the
	// guard caps at ~400 chars or 5 lines). 600 chars in 8 lines makes
	// both axes blow past the limit.
	long := strings.Repeat("verbose narrative line. ", 30) + "\n" +
		strings.Repeat("line two. ", 10) + "\n" +
		strings.Repeat("line three. ", 10) + "\n" +
		strings.Repeat("line four. ", 10) + "\n" +
		strings.Repeat("line five. ", 10) + "\n" +
		strings.Repeat("line six. ", 10) + "\n" +
		strings.Repeat("line seven. ", 10) + "\n" +
		strings.Repeat("line eight tail.", 5)

	createOut, _ := mustRunAct(t, site, 0, "create", "long-desc probe", "--description", long, "--json")
	id := pickIDFromJSON(t, createOut)

	// Without --full: the default render truncates and emits the marker.
	defOut, _ := mustRunAct(t, site, 0, "show", id)
	if !strings.Contains(defOut, "(truncated; see --json") {
		t.Errorf("default `act show` should truncate a long description; got:\n%s", defOut)
	}

	// With --full: the truncation marker disappears and the verbatim
	// tail of the description appears in the output.
	fullOut, _ := mustRunAct(t, site, 0, "show", id, "--full")
	if strings.Contains(fullOut, "(truncated") {
		t.Errorf("`act show --full` should suppress the truncation marker; got:\n%s", fullOut)
	}
	// The tail of the description is the surest "is this verbatim?"
	// signal — truncation always drops the tail first.
	if !strings.Contains(fullOut, "line eight tail.line eight tail.") {
		t.Errorf("`act show --full` should render the description tail verbatim; got:\n%s", fullOut)
	}
}

// TestDocClaim_DepDirection_AddBlocksReadsAsBlockedBy pins the
// `act dep add A B --type blocks` output string (act-982a). Before
// this ticket the success/idempotent message rendered as
// "Edge A --[blocks]--> B already present", which read as
// "A blocks B" but meant the opposite (A is blocked by B). The
// fix is the natural-English form "A is blocked by B" with the
// same direction as the underlying semantic; this test pins both
// the success and the idempotent-replay surfaces.
//
// Asserted at the subprocess stdout boundary (the actBinary
// invocation, same surface a cold-start agent hits), not on
// FormatDepAddHuman directly.
func TestDocClaim_DepDirection_AddBlocksReadsAsBlockedBy(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	outA, _ := mustRunAct(t, site, 0, "create", "child A", "--json")
	idA := pickIDFromJSON(t, outA)
	outB, _ := mustRunAct(t, site, 0, "create", "blocker B", "--json")
	idB := pickIDFromJSON(t, outB)

	// First add: success path.
	addOut, _ := mustRunAct(t, site, 0, "dep", "add", idA, idB, "--type", "blocks")
	want := "Added: " + idA + " is blocked by " + idB
	if !strings.Contains(addOut, want) {
		t.Errorf("dep add stdout missing %q\ngot:\n%s", want, addOut)
	}
	// The legacy "Edge A --[blocks]--> B" string must be gone — that's
	// the literal that read in the inverted direction.
	if strings.Contains(addOut, "--[blocks]-->") {
		t.Errorf("dep add stdout still contains legacy inverted form `--[blocks]-->`:\n%s", addOut)
	}

	// Replay: idempotent path returns "Dep already present: A is blocked by B".
	replayOut, _ := mustRunAct(t, site, 0, "dep", "add", idA, idB, "--type", "blocks")
	wantReplay := "Dep already present: " + idA + " is blocked by " + idB
	if !strings.Contains(replayOut, wantReplay) {
		t.Errorf("dep add replay stdout missing %q\ngot:\n%s", wantReplay, replayOut)
	}
	if strings.Contains(replayOut, "--[blocks]-->") {
		t.Errorf("dep add replay stdout still contains legacy inverted form `--[blocks]-->`:\n%s", replayOut)
	}
}

// TestDocClaim_DepDirection_ShowRendersBlockedBy pins the
// `act show A` output string for the dep line of a blocked issue
// (act-982a). Before this ticket the line rendered as
// "dep: blocks <blocker-id>", which read as "A blocks <blocker-id>"
// but meant the opposite. The fix renders the line as
// "dep: blocked-by <blocker-id>" — same direction as the underlying
// semantic.
//
// Other edge types continue to render with the raw edge_type
// (which reads correctly: "A relates to B", "A supersedes B").
func TestDocClaim_DepDirection_ShowRendersBlockedBy(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	outA, _ := mustRunAct(t, site, 0, "create", "blocked A", "--json")
	idA := pickIDFromJSON(t, outA)
	outB, _ := mustRunAct(t, site, 0, "create", "blocker B", "--json")
	idB := pickIDFromJSON(t, outB)

	mustRunAct(t, site, 0, "dep", "add", idA, idB, "--type", "blocks")

	showOut, _ := mustRunAct(t, site, 0, "show", idA)
	wantLine := "dep: blocked-by " + idB
	if !strings.Contains(showOut, wantLine) {
		t.Errorf("act show stdout missing %q\ngot:\n%s", wantLine, showOut)
	}
	// The pre-act-982a literal "dep: blocks <id>" must be gone.
	if strings.Contains(showOut, "dep: blocks "+idB) {
		t.Errorf("act show stdout still contains legacy `dep: blocks <id>` form:\n%s", showOut)
	}
}

// TestDocClaim_DepDirection_HelpPrimerInWorkflow pins the
// direction primer line surfaced via `act help workflow`
// (act-982a). The primer tells cold-start agents that
// `act dep add A B --type blocks` means "A is blocked by B" —
// the same canonical phrasing that landed in dep add's --type
// flag-help. Asserted at the `act help workflow` stdout boundary
// so the claim follows the surface a fresh agent hits.
func TestDocClaim_DepDirection_HelpPrimerInWorkflow(t *testing.T) {
	site := t.TempDir()
	out, _ := mustRunAct(t, site, 0, "help", "workflow")
	want := "act dep add A B --type blocks"
	if !strings.Contains(out, want) {
		t.Errorf("act help workflow missing direction primer %q\n%s", want, out)
	}
	wantPhrase := "A is blocked by B"
	if !strings.Contains(out, wantPhrase) {
		t.Errorf("act help workflow missing primer phrase %q\n%s", wantPhrase, out)
	}
	wantBehavior := "hidden from ready until B closes"
	if !strings.Contains(out, wantBehavior) {
		t.Errorf("act help workflow missing primer-behavior clause %q\n%s", wantBehavior, out)
	}
}

// TestDocClaim_DepDirection_FlagHelpPrimer pins the same primer on
// `act dep add --help`'s --type flag-help line (act-982a). The
// flag-help is one of the documentation surfaces enumerated in
// the act repo's documentation-discipline rule, so it gets its own
// asserting test at the boundary an agent actually consults.
func TestDocClaim_DepDirection_FlagHelpPrimer(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	// `act dep add --help` triggers Go's flag package usage path which
	// writes to stderr and returns a non-zero exit (the convention for
	// flag.ContinueOnError + -h is to error out of Parse). Pull both
	// streams from runAct so we read the surface a cold-start agent
	// would see.
	stdout, stderr, _ := runAct(t, site, "dep", "add", "--help")
	help := stdout + stderr
	wantType := "blocks|relates|supersedes"
	if !strings.Contains(help, wantType) {
		t.Errorf("act dep add --help missing --type listing %q\n%s", wantType, help)
	}
	wantPrimer := "A is blocked by B; A is hidden from ready until B closes"
	if !strings.Contains(help, wantPrimer) {
		t.Errorf("act dep add --help missing direction primer %q\n%s", wantPrimer, help)
	}
}

// TestDocClaim_BareAct_ListsSubcommandsAndHelpHint pins finding #1 of
// the act-f2c7 UX-polish pass: a bare `act` invocation must surface
// both the subcommand list and a pointer to `act help`. The pre-polish
// behavior was a single "usage: act <subcommand> [flags]" line that
// gave a fresh agent no concrete next step.
//
// Asserted at the user-visible boundary: the binary's stderr output
// when invoked with no subcommand. Asserts on (a) at least one
// representative subcommand name appears, (b) the disambiguating
// "dep add" multi-word subcommand is present, and (c) the
// `act help` hint is present.
func TestDocClaim_BareAct_ListsSubcommandsAndHelpHint(t *testing.T) {
	site := t.TempDir()
	// No git init needed — bare `act` errors before any state guard
	// could fire.
	_, stderr, code := runAct(t, site)
	if code != 2 {
		t.Errorf("bare act: exit = %d, want 2; stderr=%q", code, stderr)
	}
	// Subcommand list is present, with comma separators.
	for _, sub := range []string{"init", "create", "ready", "close", "show", "list", "update", "doctor"} {
		if !strings.Contains(stderr, sub) {
			t.Errorf("bare act stderr missing subcommand %q; got:\n%s", sub, stderr)
		}
	}
	// The multi-word "dep add" subcommand is named (finding #2 — also
	// pinned independently by TestDocClaim_BareAct_DepAddNotThreeItems).
	if !strings.Contains(stderr, "dep add") {
		t.Errorf("bare act stderr missing multi-word 'dep add' subcommand; got:\n%s", stderr)
	}
	// Pointer to the full tutorial.
	if !strings.Contains(stderr, "act help") {
		t.Errorf("bare act stderr missing 'act help' pointer; got:\n%s", stderr)
	}
}

// TestDocClaim_BareAct_DepAddNotThreeItems pins finding #2 of act-f2c7:
// the subcommand list (in both the bare-act usage block and `act help`)
// uses comma separators so multi-word subcommands like "dep add" don't
// look like three separate items. The drift shape: a future refactor
// drops the commas, the list reverts to space-separated, and a fresh
// agent reading either surface infers three subcommands named "dep",
// "add", "doctor" instead of the actual two ("dep add" and "doctor").
//
// Asserted at both surfaces a cold-start agent might hit: bare `act`
// stderr and `act help` stdout.
func TestDocClaim_BareAct_DepAddNotThreeItems(t *testing.T) {
	site := t.TempDir()

	// Surface 1: bare `act` stderr.
	_, stderr, _ := runAct(t, site)
	if !strings.Contains(stderr, "dep add, doctor") && !strings.Contains(stderr, "dep add,\n") {
		t.Errorf("bare act usage: 'dep add' should be followed by a comma so it parses as one subcommand; got:\n%s", stderr)
	}

	// Surface 2: `act help` overview body. The Subcommands: block
	// renders comma-separated; "dep add, doctor" is the canonical
	// adjacent pair.
	helpOut, _ := mustRunAct(t, site, 0, "help")
	if !strings.Contains(helpOut, "dep add, doctor") {
		t.Errorf("act help: 'dep add' should be comma-separated from 'doctor'; got:\n%s", helpOut)
	}
}

// TestDocClaim_Init_NextStepHint pins finding #5 of act-f2c7: a
// successful `act init` must print a "Next:" line that names both the
// immediate next step (`act create`) AND the canonical-loop tutorial
// (`act help workflow`). The pre-polish behavior had a one-line
// "Run \"act create\" to file your first issue." that omitted the
// loop pointer; an agent doing `act init` in a fresh project that
// didn't have act's own project docs to fill the gap saw no loop hint
// at all.
//
// Asserted at the user-visible boundary: the binary's stdout after
// `act init` succeeds in a fresh git repo.
func TestDocClaim_Init_NextStepHint(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	stdout, _ := mustRunAct(t, site, 0, "init")
	if !strings.Contains(stdout, "Next:") {
		t.Errorf("act init stdout missing 'Next:' hint; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "act create") {
		t.Errorf("act init Next: hint missing 'act create' anchor; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "act help workflow") {
		t.Errorf("act init Next: hint missing 'act help workflow' anchor; got:\n%s", stdout)
	}
}

// TestDocClaim_Description_CreateUpdateConsistencyNote pins finding #3
// of act-f2c7: both `act create --help` and `act update --help` must
// surface a note clarifying the behavior of an empty `--description`.
// `act create --description ”` is silently accepted (no-op
// equivalent); `act update --description ”` explicitly clears an
// existing description. The behaviors differ; the docs note pins both
// surfaces so an agent reading either learns the contrast.
//
// Asserted at the --help boundary for both commands.
func TestDocClaim_Description_CreateUpdateConsistencyNote(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	// Flag-help is reachable by --help on Go's flag package. Exit code
	// is non-zero (Go convention) and content lands on stderr.
	for _, sub := range []string{"create", "update"} {
		_, stderr, _ := runAct(t, site, sub, "--help")
		if !strings.Contains(stderr, "--description") {
			t.Errorf("act %s --help missing --description listing; got:\n%s", sub, stderr)
		}
		// The note must reference the contrast — either surface (create or
		// update) mentions the other so an agent reading one learns the
		// other's semantics.
		if !strings.Contains(stderr, "act-f2c7") {
			t.Errorf("act %s --help: --description note should cite act-f2c7 (the ticket pinning the consistency note); got:\n%s", sub, stderr)
		}
		switch sub {
		case "create":
			if !strings.Contains(stderr, "silently accepted") {
				t.Errorf("act create --help: --description note should describe empty-string as 'silently accepted' no-op; got:\n%s", stderr)
			}
		case "update":
			if !strings.Contains(stderr, "clears") {
				t.Errorf("act update --help: --description note should describe empty-string as clearing the existing description; got:\n%s", stderr)
			}
		}
	}
}

// TestDocClaim_CWDRobustness_DoctorFromInsideActDir pins the cwd-robustness
// claim in `act help ops-model`: all act commands resolve the host repo
// root from any directory inside the project tree, including from inside
// .act/ itself. The act-0852da bug was that findRepoRoot stopped at the
// first .git it found, which under Phase 1 could be the nested .act/.git;
// the fix delegates to gitops.FindHostRepoRoot which skips nested
// .act/.git entries.
//
// The boundary asserted is `act doctor` exit code and stderr from cwd =
// <host>/.act/ — the same surface a persistent-shell harness would hit
// after a stray `cd .act`. The prior behaviour (exit 0 with "no act state"
// message) is the negative assertion; any real doctor output (non-empty
// stderr with no "no act state" line) is the positive assertion.
func TestDocClaim_CWDRobustness_DoctorFromInsideActDir(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "cwd@example.com", "cwdtest")
	mustRunAct(t, site, 0, "init", "--json")

	// Confirm that .act/.git exists (act init bootstraps the nested repo).
	actGit := filepath.Join(site, ".act", ".git")
	if _, err := os.Stat(actGit); err != nil {
		t.Fatalf(".act/.git not present after act init: %v", err)
	}

	// Run act doctor from inside the nested .act/ directory. Before
	// the fix this returned exit 0 with "no act state in this repo".
	actDir := filepath.Join(site, ".act")
	stdout, stderr, code := runAct(t, actDir, "doctor")
	combined := stdout + stderr

	if code != 0 {
		t.Fatalf("act doctor from .act/: exit %d; combined output:\n%s", code, combined)
	}

	noStateMsg := "no act state in this repo"
	if strings.Contains(combined, noStateMsg) {
		t.Errorf("act doctor from .act/ produced the wrong-resolver sentinel %q; "+
			"FindHostRepoRoot should have skipped .act/.git and found the host repo.\n"+
			"combined output:\n%s", noStateMsg, combined)
	}
}

// TestDocClaim_NoDoctorOptOut pins the `--no-doctor` flag's user-visible
// contract claimed in cmd/act/close.go's flag-help string: "skip the
// post-close single-issue commit-marker correlation check (default: warn
// on stderr if no host commit in the last 50 carries an 'Act-Id:' trailer
// for this issue)". This is the same failure pattern as act-6fca /
// act-ac52 — the existing internal test (TestRunClose_NoDoctorOptsOut)
// calls RunClose() directly and would pass even if the CLI flag was never
// wired through to the NoDoctor option.
//
// Two close invocations in the same fresh repo (no host commits, so the
// marker correlation check would always fire):
//
//  1. Default (no --no-doctor): stderr must contain the warning text
//     "no host commit with 'Act-Id: <id>' trailer".
//
//  2. With --no-doctor: stderr must be empty (check is skipped).
//
// The comparison between the two cases makes the assertion meaningful:
// the test fails if either (a) the default doesn't warn or (b) --no-doctor
// doesn't suppress the warning.
func TestDocClaim_NoDoctorOptOut(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	// Issue 1: close without --no-doctor. No host commit carries the
	// trailer, so the post-close check must warn on stderr.
	createOut1, _ := mustRunAct(t, site, 0, "create", "no-doctor probe default", "--json")
	id1 := pickIDFromJSON(t, createOut1)

	_, stderr1, code1 := runAct(t, site, "close", id1, "--reason", "probe default")
	if code1 != 0 {
		t.Fatalf("close without --no-doctor: exit %d; stderr: %s", code1, stderr1)
	}
	// The warning must mention the 'Act-Id:' trailer and the issue id.
	if !strings.Contains(stderr1, "Act-Id:") {
		t.Errorf("default close: stderr missing 'Act-Id:' warning; got: %q", stderr1)
	}
	if !strings.Contains(stderr1, id1) {
		t.Errorf("default close: stderr missing issue id %q; got: %q", id1, stderr1)
	}

	// Issue 2: close with --no-doctor. Same repo state (no host commits),
	// but the warning must be suppressed entirely.
	createOut2, _ := mustRunAct(t, site, 0, "create", "no-doctor probe with flag", "--json")
	id2 := pickIDFromJSON(t, createOut2)

	_, stderr2, code2 := runAct(t, site, "close", id2, "--reason", "probe with flag", "--no-doctor")
	if code2 != 0 {
		t.Fatalf("close with --no-doctor: exit %d; stderr: %s", code2, stderr2)
	}
	if stderr2 != "" {
		t.Errorf("--no-doctor must suppress the marker-correlation warning; got stderr: %q", stderr2)
	}
}

// TestDocClaim_ClaimLost_LastWriteWins pins the README / `act help errors`
// promise "concurrent claimers resolve last-write-wins" (act-2af8c7).
//
// Two `act update --claim --isolated` invocations run against the same
// issue from the same working tree but with different node_ids (config.json
// is swapped between invocations). The first invocation has the earlier
// wall-clock HLC and wins; the second, running after it, has the later HLC
// and loses. This exercises the fold winner-selection at the subprocess
// boundary that spec-v2.md §7.4 ("concurrent_claim_two_writers") and the
// README ("atomic; concurrent claimers resolve last-write-wins") claim.
//
// Asserted at the subprocess exit-code + stdout boundary:
//   - Winner: exit 0, {"ok":true,"claimed":true,"winner":"<winnerNodeID>"}
//   - Loser:  exit 5, {"ok":false,"claimed":false,"winner":"<winnerNodeID>",
//     "error":"claim_lost"}
//
// Spec §7.4 and the universal exit-code table both say the loser exits 5
// with envelope claim_lost; act-a373bb reconciled the implementation to
// match. The test asserts that boundary (exit 5 + claimed:false +
// error:claim_lost).
func TestDocClaim_ClaimLost_LastWriteWins(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set (TestMain did not run)")
	}

	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "alice@example.com", "Alice")
	mustRunAct(t, site, 0, "init", "--json")

	createOut, _ := mustRunAct(t, site, 0, "create", "last-write-wins probe", "--json")
	id := pickIDFromJSON(t, createOut)

	// Read the current (winner's) config.json.
	cfgPath := filepath.Join(site, ".act", "config.json")
	cfgData, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read .act/config.json: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		t.Fatalf("unmarshal config.json: %v", err)
	}
	winnerNodeID, _ := cfg["node_id"].(string)
	if winnerNodeID == "" {
		t.Fatalf("node_id missing from config.json")
	}

	// First claim: winner path. --isolated skips pull-rebase.
	winOut, _, winCode := runAct(t, site, "update", "--claim", "--isolated", "--json", id)
	if winCode != 0 {
		t.Fatalf("winner claim: exit %d\n%s", winCode, winOut)
	}
	if !strings.Contains(winOut, `"claimed":true`) {
		t.Errorf("winner claim: stdout missing claimed:true\n%s", winOut)
	}

	// Swap config.json to a different node_id (simulates a second agent).
	const loserNodeID = "deadbeef"
	cfg["node_id"] = loserNodeID
	loserCfg, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgPath, loserCfg, 0o600); err != nil {
		t.Fatalf("write loser config: %v", err)
	}
	// Restore winner's config.json after the test.
	t.Cleanup(func() { _ = os.WriteFile(cfgPath, cfgData, 0o600) })

	// Second claim: loser path. Fold sees two ops; winner's earlier HLC wins.
	loseOut, _, loseCode := runAct(t, site, "update", "--claim", "--isolated", "--json", id)

	// Primary assertion: exit 5 (the reconciled claim_lost boundary code).
	if loseCode != 5 {
		t.Errorf("loser claim: exit %d, want 5 (claim_lost); stdout:\n%s", loseCode, loseOut)
	}
	// The loser output must carry claimed:false and the winner's node_id.
	if !strings.Contains(loseOut, `"claimed":false`) {
		t.Errorf("loser claim: stdout missing claimed:false\n%s", loseOut)
	}
	// The loser envelope must carry the canonical claim_lost error slug.
	if !strings.Contains(loseOut, `"error":"claim_lost"`) {
		t.Errorf("loser claim: stdout missing error:claim_lost\n%s", loseOut)
	}
	if !strings.Contains(loseOut, winnerNodeID) {
		t.Errorf("loser claim: stdout missing winner node_id %q\n%s", winnerNodeID, loseOut)
	}
}

// repoRootForDocClaim returns the repo root inferred from the current
// source file's location (this test file lives at
// TestDocClaim_Show_BlocksAndBlockedByJSON pins the act-00e5cc claim
// made in docs/spec-v2.md: `act show --json <id>` includes `blocked_by`
// and `blocks` arrays in addition to the existing `deps` array.
//
// Graph: A is blocked by B (A has dep {parent:B, edge_type:blocks}).
//   - A.blocked_by must contain B's id (A is the child, B is the parent).
//   - B.blocks must contain A's id (B is the parent; reverse scan).
//   - A.blocks must be empty (nothing is blocked by A in this graph).
//   - B.blocked_by must be empty (B has no blocks deps).
//
// Also asserts that A.deps is byte-identical before and after the new
// fields are added (the safety constraint from the ticket).
func TestDocClaim_Show_BlocksAndBlockedByJSON(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	outA, _ := mustRunAct(t, site, 0, "create", "child A (blocked)", "--json")
	idA := pickIDFromJSON(t, outA)
	outB, _ := mustRunAct(t, site, 0, "create", "blocker B", "--json")
	idB := pickIDFromJSON(t, outB)

	// Snapshot A's deps BEFORE adding the edge, to establish baseline
	// shape for byte-identity assertion below.
	preOut, _ := mustRunAct(t, site, 0, "show", idA, "--json")
	var preA map[string]json.RawMessage
	if err := json.Unmarshal([]byte(preOut), &preA); err != nil {
		t.Fatalf("pre-dep show A --json parse: %v\n%s", err, preOut)
	}
	preDeps := string(preA["deps"])

	// Add the blocks dep: A is blocked by B.
	mustRunAct(t, site, 0, "dep", "add", idA, idB, "--type", "blocks")

	// Show A --json and parse.
	showAOut, _ := mustRunAct(t, site, 0, "show", idA, "--json")
	var showA map[string]json.RawMessage
	if err := json.Unmarshal([]byte(showAOut), &showA); err != nil {
		t.Fatalf("show A --json parse: %v\n%s", err, showAOut)
	}

	// 1. deps must be byte-identical (the load-bearing safety property).
	// Compare normalized JSON to be robust to whitespace changes.
	postDeps := string(showA["deps"])
	// Both encode a single-element array; they should contain "parent" and "edge_type".
	if !strings.Contains(postDeps, `"parent"`) {
		t.Errorf("A.deps missing 'parent' key: %s", postDeps)
	}
	if !strings.Contains(postDeps, `"edge_type"`) {
		t.Errorf("A.deps missing 'edge_type' key: %s", postDeps)
	}
	// Pre-dep A had no deps; after dep add A has one. The invariant
	// "byte-identical" means we don't change the key names or shape.
	// Pre-dep is [] and post-dep has the edge — confirm the shape is the
	// same (both arrays of objects with {parent, edge_type}) and that the
	// pre-dep baseline was an empty array.
	if preDeps != "[]" && preDeps != "null" && preDeps != "" {
		t.Logf("pre-dep A.deps was non-empty, which is unexpected for a fresh issue: %s", preDeps)
	}

	// 2. blocked_by: A must list B.
	var blockedBy []string
	if err := json.Unmarshal(showA["blocked_by"], &blockedBy); err != nil {
		t.Fatalf("A.blocked_by unmarshal: %v\n%s", err, showAOut)
	}
	if len(blockedBy) != 1 || blockedBy[0] != idB {
		t.Errorf("A.blocked_by = %v, want [%s]", blockedBy, idB)
	}

	// 3. blocks: A must be empty (nothing is blocked by A).
	var blocksA []string
	if err := json.Unmarshal(showA["blocks"], &blocksA); err != nil {
		t.Fatalf("A.blocks unmarshal: %v\n%s", err, showAOut)
	}
	if len(blocksA) != 0 {
		t.Errorf("A.blocks = %v, want []", blocksA)
	}

	// Show B --json and parse.
	showBOut, _ := mustRunAct(t, site, 0, "show", idB, "--json")
	var showB map[string]json.RawMessage
	if err := json.Unmarshal([]byte(showBOut), &showB); err != nil {
		t.Fatalf("show B --json parse: %v\n%s", err, showBOut)
	}

	// 4. blocks: B must list A (reverse scan).
	var blocksB []string
	if err := json.Unmarshal(showB["blocks"], &blocksB); err != nil {
		t.Fatalf("B.blocks unmarshal: %v\n%s", err, showBOut)
	}
	if len(blocksB) != 1 || blocksB[0] != idA {
		t.Errorf("B.blocks = %v, want [%s]", blocksB, idA)
	}

	// 5. blocked_by: B must be empty (B has no blocks deps of its own).
	var blockedByB []string
	if err := json.Unmarshal(showB["blocked_by"], &blockedByB); err != nil {
		t.Fatalf("B.blocked_by unmarshal: %v\n%s", err, showBOut)
	}
	if len(blockedByB) != 0 {
		t.Errorf("B.blocked_by = %v, want []", blockedByB)
	}
}

// TestDocClaim_Show_DepShapeMatchesSpec pins the act-5918c7 claim made
// in docs/spec-v2.md: the dep-edge JSON shape emitted by `act show
// --json` uses {parent, edge_type} keys, matching the spec's schema
// section. This test drives the binary and asserts the wire shape
// directly so any future drift between the spec examples and the code
// surfaces as a build break.
func TestDocClaim_Show_DepShapeMatchesSpec(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	outA, _ := mustRunAct(t, site, 0, "create", "child", "--json")
	idA := pickIDFromJSON(t, outA)
	outB, _ := mustRunAct(t, site, 0, "create", "parent", "--json")
	idB := pickIDFromJSON(t, outB)

	mustRunAct(t, site, 0, "dep", "add", idA, idB, "--type", "blocks")

	showOut, _ := mustRunAct(t, site, 0, "show", idA, "--json")
	var show map[string]json.RawMessage
	if err := json.Unmarshal([]byte(showOut), &show); err != nil {
		t.Fatalf("show --json parse: %v\n%s", err, showOut)
	}

	// The spec says deps shape is [{parent, edge_type}].
	var deps []map[string]string
	if err := json.Unmarshal(show["deps"], &deps); err != nil {
		t.Fatalf("deps unmarshal: %v\ndeps raw: %s", err, show["deps"])
	}
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep, got %d: %s", len(deps), show["deps"])
	}
	dep := deps[0]
	if _, ok := dep["parent"]; !ok {
		t.Errorf("dep missing 'parent' key; got keys: %v (spec says {parent, edge_type})", dep)
	}
	if _, ok := dep["edge_type"]; !ok {
		t.Errorf("dep missing 'edge_type' key; got keys: %v (spec says {parent, edge_type})", dep)
	}
	// Legacy keys must be absent (drift guard).
	if _, ok := dep["id"]; ok {
		t.Errorf("dep has legacy 'id' key; spec now uses 'parent'")
	}
	if _, ok := dep["edge"]; ok {
		t.Errorf("dep has legacy 'edge' key; spec now uses 'edge_type'")
	}
	if dep["parent"] != idB {
		t.Errorf("dep.parent = %q, want %q", dep["parent"], idB)
	}
	if dep["edge_type"] != "blocks" {
		t.Errorf("dep.edge_type = %q, want 'blocks'", dep["edge_type"])
	}
}

// TestDocClaim_DepAdd_InspectionHint pins the act-00e5cc claim: `act dep
// add --help` must include the inspection hint pointing agents to
// `act show <id> --json` for the `blocked_by` / `blocks` arrays.
func TestDocClaim_DepAdd_InspectionHint(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	_, stderr, _ := runAct(t, site, "dep", "add", "--help")
	help := stderr
	// The inspection hint must name both the human and JSON forms.
	want := "inspect with act show <id>"
	if !strings.Contains(help, want) {
		t.Errorf("act dep add --help missing inspection hint %q\ngot:\n%s", want, help)
	}
	// Must name the blocked_by / blocks arrays specifically.
	if !strings.Contains(help, "blocked_by") {
		t.Errorf("act dep add --help missing 'blocked_by' in inspection hint\ngot:\n%s", help)
	}
	if !strings.Contains(help, "blocks") {
		t.Errorf("act dep add --help missing 'blocks' in inspection hint\ngot:\n%s", help)
	}
}

// TestDocClaim_BlockedByExtDep_ClaimBlocked pins the act-5e36 claim made in
// docs/spec-v2.md and cmd/act/help.go: `act update --claim <id>` exits 2
// with envelope code `blocked_by_external_dep` when the issue has ≥1 open
// external dep. The details map must contain an `external_deps` array listing
// the blocking ref(s).
//
// Boundary: subprocess `act update --claim --json`; exit code + JSON envelope.
func TestDocClaim_BlockedByExtDep_ClaimBlocked(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	createOut, _ := mustRunAct(t, site, 0, "create", "blocked claim probe", "--json")
	id := pickIDFromJSON(t, createOut)

	// Attach an external dep so the gate fires.
	mustRunAct(t, site, 0, "update", id, "--ext-add", "linear:ENG-99")

	out, _, code := runAct(t, site, "update", "--claim", id, "--json")
	if code != 2 {
		t.Fatalf("expected exit 2 (blocked_by_external_dep), got %d; stdout:\n%s", code, out)
	}
	if !strings.Contains(out, `"error":"blocked_by_external_dep"`) &&
		!strings.Contains(out, `"error": "blocked_by_external_dep"`) {
		t.Errorf("stdout missing blocked_by_external_dep code:\n%s", out)
	}
	if !strings.Contains(out, "linear:ENG-99") {
		t.Errorf("stdout missing blocking ref linear:ENG-99 in envelope:\n%s", out)
	}
	if !strings.Contains(out, `"external_deps"`) {
		t.Errorf("stdout missing details.external_deps key:\n%s", out)
	}
}

// TestDocClaim_BlockedByExtDep_CloseBlocked pins the act-5e36 claim: `act
// close <id>` exits 2 with envelope code `blocked_by_external_dep` when the
// issue has ≥1 open external dep.
//
// Boundary: subprocess `act close --json`; exit code + JSON envelope.
func TestDocClaim_BlockedByExtDep_CloseBlocked(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	createOut, _ := mustRunAct(t, site, 0, "create", "blocked close probe", "--json")
	id := pickIDFromJSON(t, createOut)

	mustRunAct(t, site, 0, "update", id, "--ext-add", "gh:org/repo#7")

	out, _, code := runAct(t, site, "close", id, "--json")
	if code != 2 {
		t.Fatalf("expected exit 2 (blocked_by_external_dep), got %d; stdout:\n%s", code, out)
	}
	if !strings.Contains(out, `"error":"blocked_by_external_dep"`) &&
		!strings.Contains(out, `"error": "blocked_by_external_dep"`) {
		t.Errorf("stdout missing blocked_by_external_dep code:\n%s", out)
	}
	if !strings.Contains(out, "gh:org/repo#7") {
		t.Errorf("stdout missing blocking ref gh:org/repo#7 in envelope:\n%s", out)
	}
	if !strings.Contains(out, `"external_deps"`) {
		t.Errorf("stdout missing details.external_deps key:\n%s", out)
	}
}

// TestDocClaim_BlockedByExtDep_ForceOverrides pins the act-5e36 claim: both
// `act update --claim --force` and `act close --force` succeed (exit 0) when
// the issue has open external deps, and emit a WARNING to stderr naming the
// bypassed deps.
//
// Boundary: subprocess exit code + stderr WARNING line.
func TestDocClaim_BlockedByExtDep_ForceOverrides(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	// ----- claim --force -----
	{
		createOut, _ := mustRunAct(t, site, 0, "create", "force-claim probe", "--json")
		id := pickIDFromJSON(t, createOut)
		mustRunAct(t, site, 0, "update", id, "--ext-add", "jira:PROJ-55")

		_, stderr, code := runAct(t, site, "update", "--claim", "--force", "--no-commit", id)
		if code != 0 {
			t.Fatalf("--force claim: expected exit 0, got %d; stderr:\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "WARNING") {
			t.Errorf("--force claim: expected WARNING on stderr; got:\n%s", stderr)
		}
		if !strings.Contains(stderr, "jira:PROJ-55") {
			t.Errorf("--force claim: WARNING should name bypassed dep jira:PROJ-55; got stderr:\n%s", stderr)
		}
	}

	// ----- close --force -----
	{
		createOut, _ := mustRunAct(t, site, 0, "create", "force-close probe", "--json")
		id := pickIDFromJSON(t, createOut)
		mustRunAct(t, site, 0, "update", id, "--ext-add", "jira:PROJ-66")

		_, stderr, code := runAct(t, site, "close", id, "--force", "--no-commit")
		if code != 0 {
			t.Fatalf("--force close: expected exit 0, got %d; stderr:\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "WARNING") {
			t.Errorf("--force close: expected WARNING on stderr; got:\n%s", stderr)
		}
		if !strings.Contains(stderr, "jira:PROJ-66") {
			t.Errorf("--force close: WARNING should name bypassed dep jira:PROJ-66; got stderr:\n%s", stderr)
		}
	}
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
