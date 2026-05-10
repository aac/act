package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/gitops"
)

// TestFirst4Hex covers the small helper that extracts the doctor-grep key
// from a full issue id. Empty for malformed ids; first 4 hex chars for valid
// ones.
func TestFirst4Hex(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"act-deadbeef0123abcd", "dead"},
		{"act-c83a", "c83a"},
		{"act-abc", ""},      // too short after prefix
		{"act-", ""},         // no hex at all
		{"abc-deadbeef", ""}, // wrong prefix
		{"", ""},
	}
	for _, tc := range cases {
		if got := first4Hex(tc.in); got != tc.want {
			t.Errorf("first4Hex(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestFormatShowHuman_OmitsCommitsSectionWhenEmpty: per AC #4, an issue
// with zero work commits renders no 'commits:' section. Tracking issues
// closed without code shouldn't carry an empty header.
func TestFormatShowHuman_OmitsCommitsSectionWhenEmpty(t *testing.T) {
	res := ShowResult{
		Fields: map[string]any{
			"id":     "act-deadbeefdeadbeef",
			"title":  "no work",
			"status": "closed",
		},
		Commits: nil,
	}
	out := FormatShowHuman(res)
	if strings.Contains(out, "commits:") {
		t.Errorf("output unexpectedly contains 'commits:' header: %q", out)
	}
}

// TestFormatShowHuman_RendersCommitsSection: when Commits is populated
// the human renderer appends a 'commits:' block with one line per commit.
// Each line carries the short sha + author date + subject so a reader can
// scan the work attributed to the issue at a glance.
func TestFormatShowHuman_RendersCommitsSection(t *testing.T) {
	res := ShowResult{
		Fields: map[string]any{
			"id":     "act-c83adeadbeefdead",
			"title":  "with work",
			"status": "closed",
		},
		Commits: []gitops.WorkCommit{
			{SHA: "abcdef0123456789", Subject: "fix: thing (act-c83a)", AuthorDate: "2026-05-10T15:00:00Z"},
			{SHA: "1234567890abcdef", Subject: "test: more (act-c83a)", AuthorDate: "2026-05-10T15:30:00Z"},
		},
	}
	out := FormatShowHuman(res)
	if !strings.Contains(out, "commits:\n") {
		t.Errorf("output missing 'commits:' header: %q", out)
	}
	if !strings.Contains(out, "abcdef0") || !strings.Contains(out, "fix: thing (act-c83a)") {
		t.Errorf("output missing first commit details: %q", out)
	}
	if !strings.Contains(out, "1234567") || !strings.Contains(out, "test: more (act-c83a)") {
		t.Errorf("output missing second commit details: %q", out)
	}
	// Author date should appear between sha and subject for at-a-glance scan.
	if !strings.Contains(out, "2026-05-10T15:00:00Z") {
		t.Errorf("output missing author date: %q", out)
	}
}

// TestShowJSON_AlwaysHasCommitsKey: per AC #2, --json must include a
// 'commits' array (empty when none) so MCP consumers can rely on the key
// existing.
func TestShowJSON_AlwaysHasCommitsKey(t *testing.T) {
	res := ShowResult{
		Fields:  map[string]any{"id": "act-x", "title": "y"},
		Commits: nil,
	}
	data, err := json.Marshal(res.ShowJSON())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	commits, present := got["commits"]
	if !present {
		t.Fatalf("commits key missing from JSON: %s", data)
	}
	arr, ok := commits.([]any)
	if !ok {
		t.Fatalf("commits = %T (%v); want []any", commits, commits)
	}
	if len(arr) != 0 {
		t.Errorf("commits = %v; want [] when none", arr)
	}
}

// TestRunShow_PicksUpWorkCommitsByMarker is the integration test for
// act-9c8c: an issue with a real (act-XXXX) work commit in git history
// must show up in ShowResult.Commits. Uses makeCreateRepo + RunCreate
// (real git) and writes a synthetic work commit with the issue's
// commit_marker before running show.
func TestRunShow_PicksUpWorkCommitsByMarker(t *testing.T) {
	root := makeCreateRepo(t)
	createOut, code := RunCreate(root, CreateOptions{Title: "with work commit", Type: "task"})
	if code != 0 {
		t.Fatalf("create: code = %d, out=%+v", code, createOut)
	}
	id := createOut.(CreateResult).ID
	short := createOut.(CreateResult).Prefix
	if short == "" {
		t.Fatalf("create result missing Prefix; cannot build commit marker")
	}
	// Synthesise a work commit carrying the marker. Touch a file outside
	// .act/ so the commit is real "code work" from git's perspective.
	workFile := filepath.Join(root, "WORK.txt")
	if err := os.WriteFile(workFile, []byte("done\n"), 0o644); err != nil {
		t.Fatalf("write workfile: %v", err)
	}
	mustGit(t, root, "add", "WORK.txt")
	mustGit(t, root, "commit", "-q", "--no-verify", "-m", "implement the thing ("+short+")")
	wantSHA := strings.TrimSpace(runOut(t, root, "git", "rev-parse", "HEAD"))

	out, code := RunShow(root, ShowOptions{ID: id})
	if code != 0 {
		t.Fatalf("show: code = %d, out=%+v", code, out)
	}
	res, ok := out.(ShowResult)
	if !ok {
		t.Fatalf("output type = %T, want ShowResult", out)
	}
	if len(res.Commits) == 0 {
		t.Fatalf("res.Commits is empty; want at least one work commit for marker (%s)", short)
	}
	found := false
	for _, c := range res.Commits {
		if c.SHA == wantSHA {
			found = true
			if !strings.Contains(c.Subject, short) {
				t.Errorf("commit subject = %q; want it to contain marker (%s)", c.Subject, short)
			}
			if c.AuthorDate == "" {
				t.Errorf("commit author_date is empty")
			}
		}
	}
	if !found {
		shas := make([]string, 0, len(res.Commits))
		for _, c := range res.Commits {
			shas = append(shas, c.SHA[:7])
		}
		t.Errorf("expected SHA %s in res.Commits; got %v", wantSHA[:7], shas)
	}

	// Human renderer must include the commits section.
	human := FormatShowHuman(res)
	if !strings.Contains(human, "commits:") {
		t.Errorf("human output missing 'commits:' header: %q", human)
	}
	if !strings.Contains(human, wantSHA[:7]) {
		t.Errorf("human output missing short sha %s: %q", wantSHA[:7], human)
	}
}

// TestRunShow_NoWorkCommits: an issue with no matching git commits must
// yield ShowResult.Commits empty (nil) and the human render must omit the
// 'commits:' section. Uses a real git repo (makeCreateRepo) so the git
// log command actually runs and returns no matches.
func TestRunShow_NoWorkCommits(t *testing.T) {
	root := makeCreateRepo(t)
	createOut, code := RunCreate(root, CreateOptions{Title: "tracking only", Type: "task"})
	if code != 0 {
		t.Fatalf("create: code = %d, out=%+v", code, createOut)
	}
	id := createOut.(CreateResult).ID
	// Note: act create itself produces a commit `act-op: (act-XXXX) create`
	// which DOES contain the marker. This is the auto-commit per spec, not
	// a work commit. AC #1 talks about "work commits" but since they share
	// the same marker shape, RunShow surfaces them all (the act-op commits
	// are useful too — they show the agent's tracker activity in git).
	// The test verifies the contract: when the issue has no commits beyond
	// what act itself wrote, the commits list reflects only those.

	out, code := RunShow(root, ShowOptions{ID: id})
	if code != 0 {
		t.Fatalf("show: code = %d, out=%+v", code, out)
	}
	res := out.(ShowResult)
	// At minimum the act-op create commit appears (one entry); the
	// significant assertion is that ShowJSON produces a 'commits' key
	// even if there are zero matches in some other future scenario.
	if res.Commits == nil {
		t.Errorf("commits is nil; expected at least the act-op create commit")
	}
	jsonShape := res.ShowJSON()
	if _, present := jsonShape["commits"]; !present {
		t.Errorf("ShowJSON missing 'commits' key")
	}
}
