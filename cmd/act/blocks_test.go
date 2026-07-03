package main

import (
	"os/exec"
	"strings"
	"testing"
)

// blocksSite bootstraps a git+act repo in a temp dir and returns its path.
func blocksSite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", dir},
		{"-C", dir, "config", "user.email", "test@example.com"},
		{"-C", dir, "config", "user.name", "Test"},
		{"-C", dir, "config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if _, stderr, code := runActIn(t, dir, "init", "--json"); code != 0 {
		t.Fatalf("act init: exit %d; stderr=%s", code, stderr)
	}
	return dir
}

// createBlocksIssue creates an issue and parses its id from --json output.
func createBlocksIssue(t *testing.T, dir, title string) string {
	t.Helper()
	out, _, code := runActIn(t, dir, "create", title, "--json")
	if code != 0 {
		t.Fatalf("act create %q: exit %d; stdout=%s", title, code, out)
	}
	marker := `"id":"`
	i := strings.Index(out, marker)
	if i < 0 {
		marker = `"id": "`
		i = strings.Index(out, marker)
	}
	if i < 0 {
		t.Fatalf("no id in create output: %s", out)
	}
	rest := out[i+len(marker):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		t.Fatalf("could not parse id: %s", out)
	}
	return rest[:end]
}

func linesOf(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// TestDocClaim_BlockGraphQueries pins the `act help workflow` claim that
// `act blocked-by <id>` lists the ids that block <id> and `act blocks <id>`
// lists the ids that <id> blocks — asserted at the subprocess boundary
// against a real blocks edge (act-2e1070). Direction: for "A blocked-by B",
// blocked-by A -> B and blocks B -> A.
func TestDocClaim_BlockGraphQueries(t *testing.T) {
	dir := blocksSite(t)
	a := createBlocksIssue(t, dir, "downstream A")
	b := createBlocksIssue(t, dir, "prereq B")

	// A is blocked by B (writes a blocks edge on A pointing at B).
	if out, stderr, code := runActIn(t, dir, "dep", "add", a, "--blocked-by", b); code != 0 {
		t.Fatalf("act dep add %s --blocked-by %s: exit %d; out=%s stderr=%s", a, b, code, out, stderr)
	}

	// blocked-by A -> [B]
	out, _, code := runActIn(t, dir, "blocked-by", a)
	if code != 0 {
		t.Fatalf("act blocked-by %s: exit %d", a, code)
	}
	if got := linesOf(out); len(got) != 1 || got[0] != b {
		t.Errorf("act blocked-by %s = %v; want [%s]", a, got, b)
	}

	// blocks B -> [A]
	out, _, code = runActIn(t, dir, "blocks", b)
	if code != 0 {
		t.Fatalf("act blocks %s: exit %d", b, code)
	}
	if got := linesOf(out); len(got) != 1 || got[0] != a {
		t.Errorf("act blocks %s = %v; want [%s]", b, got, a)
	}

	// Reverse directions are empty: A blocks nothing, B is blocked by nothing.
	if out, _, code = runActIn(t, dir, "blocks", a); code != 0 || strings.TrimSpace(out) != "" {
		t.Errorf("act blocks %s = %q (exit %d); want empty", a, out, code)
	}
	if out, _, code = runActIn(t, dir, "blocked-by", b); code != 0 || strings.TrimSpace(out) != "" {
		t.Errorf("act blocked-by %s = %q (exit %d); want empty", b, out, code)
	}
}

// TestBlocks_UsageAndNotFound covers the error surfaces: missing id is a
// usage error (exit 2), and an unresolvable id is issue_not_found (exit 3).
func TestBlocks_UsageAndNotFound(t *testing.T) {
	dir := blocksSite(t)
	if _, _, code := runActIn(t, dir, "blocks"); code != 2 {
		t.Errorf("act blocks (no id): exit %d; want 2", code)
	}
	if _, _, code := runActIn(t, dir, "blocked-by"); code != 2 {
		t.Errorf("act blocked-by (no id): exit %d; want 2", code)
	}
	if _, _, code := runActIn(t, dir, "blocks", "act-zzzzzz"); code != 3 {
		t.Errorf("act blocks act-zzzzzz: exit %d; want 3", code)
	}
}
