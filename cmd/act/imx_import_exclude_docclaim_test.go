package main

// act-76eaec — every mode of `act state import` leaves the new nested
// repo's info/exclude carrying act's working-file lines, so a write in the
// imported store shows nothing untracked. Run against the real binary.
//
// `--from-remote` makes `.act/` by `git clone`, which starts from git's
// stock info/exclude. The two copy modes carry over whatever the source's
// exclude file holds, so a source made by an older act hands on a file
// without the lines.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func imxGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return string(out)
}

// imxEmptyHost is a git repo with no `.act/` — the directory an import
// lands in.
func imxEmptyHost(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	imxGit(t, dir, "init", "-q", "-b", "main")
	imxGit(t, dir, "config", "user.email", "test@example.com")
	imxGit(t, dir, "config", "user.name", "Test")
	imxGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// imxSource is an initialized store with one issue. With older=true its
// exclude file is put back to what a store made before act-1d3110 had:
// none of act's lines.
func imxSource(t *testing.T, older bool) string {
	t.Helper()
	host := xlockHostRepo(t)
	if out, stderr, code := runActIn(t, host, "create", "imx seed", "--json"); code != 0 {
		t.Fatalf("act create in source: exit %d; stdout=%s stderr=%s", code, out, stderr)
	}
	if older {
		p := filepath.Join(host, ".act", ".git", "info", "exclude")
		if err := os.WriteFile(p, []byte("# git ls-files --others --exclude-from=.git/info/exclude\n"), 0o644); err != nil {
			t.Fatalf("rewrite source exclude: %v", err)
		}
	}
	return host
}

// imxAssertClean checks the imported store at the boundary the ticket
// names: the exclude lines, then nested `git status` after one write.
func imxAssertClean(t *testing.T, target string) {
	t.Helper()
	excludePath := filepath.Join(target, ".act", ".git", "info", "exclude")
	body, _ := os.ReadFile(excludePath)
	var absent []string
	for _, e := range xlockEntries {
		if !strings.Contains("\n"+string(body), "\n"+e+"\n") {
			absent = append(absent, e)
		}
	}
	if len(absent) > 0 {
		t.Errorf("after import, .act/.git/info/exclude lacks %d of %d lines: %s", len(absent), len(xlockEntries), strings.Join(absent, " "))
	}
	if out, stderr, code := runActIn(t, target, "create", "imx write", "--json"); code != 0 {
		t.Fatalf("act create in imported store: exit %d; stdout=%s stderr=%s", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(target, ".act", ".write.lock")); err != nil {
		t.Fatalf("precondition: .act/.write.lock should exist after a write: %v", err)
	}
	if st := xlockNestedUntracked(t, target); strings.TrimSpace(st) != "" {
		t.Errorf("nested repo status after import + one write should be empty, got:\n%s\nexclude file:\n%s", st, body)
	}
	if out, _, code := runActIn(t, target, "doctor", "--check", "nested-exclude"); code != 0 || !strings.Contains(out, "0 findings") {
		t.Errorf("doctor --check nested-exclude on the imported store: exit %d, want 0 findings; out=%s", code, out)
	}
}

func TestDocClaim_StateImport_WritesNestedExclude(t *testing.T) {
	t.Run("from-remote", func(t *testing.T) {
		src := imxSource(t, false)
		bare := filepath.Join(t.TempDir(), "tracker.git")
		imxGit(t, "", "clone", "-q", "--bare", filepath.Join(src, ".act", ".git"), bare)
		target := imxEmptyHost(t)
		if out, stderr, code := runActIn(t, target, "state", "import", "--from-remote", bare, target, "--json"); code != 0 {
			t.Fatalf("state import --from-remote: exit %d; stdout=%s stderr=%s", code, out, stderr)
		}
		imxAssertClean(t, target)
	})
	// The one way left to get a nested repo without the lines on a current
	// act: cloning the tracker by hand, which act never sees. doctor
	// reports it and --fix repairs it.
	t.Run("git clone by hand is left to doctor", func(t *testing.T) {
		src := imxSource(t, false)
		target := imxEmptyHost(t)
		imxGit(t, "", "clone", "-q", filepath.Join(src, ".act", ".git"), filepath.Join(target, ".act"))
		// A clone act never sees gets its commit identity from the
		// user's git, not from act; give it one, as a CI runner has none.
		imxGit(t, filepath.Join(target, ".act"), "config", "user.email", "test@example.com")
		imxGit(t, filepath.Join(target, ".act"), "config", "user.name", "Test")
		out, _, code := runActIn(t, target, "doctor", "--check", "nested-exclude")
		if code != 0 || !strings.Contains(out, "[warn] [nested-exclude]") || !strings.Contains(out, "1 findings") {
			t.Fatalf("doctor on a hand-cloned store: exit %d, want one warn nested-exclude finding; out=%s", code, out)
		}
		if out, _, code := runActIn(t, target, "doctor", "--check", "nested-exclude", "--fix"); code != 0 {
			t.Fatalf("doctor --fix on a hand-cloned store: exit %d; out=%s", code, out)
		}
		imxAssertClean(t, target)
	})
	t.Run("copy", func(t *testing.T) {
		src := imxSource(t, false)
		target := imxEmptyHost(t)
		if out, stderr, code := runActIn(t, src, "state", "import", target, "--json"); code != 0 {
			t.Fatalf("state import: exit %d; stdout=%s stderr=%s", code, out, stderr)
		}
		imxAssertClean(t, target)
	})
	t.Run("copy from an older store", func(t *testing.T) {
		src := imxSource(t, true)
		target := imxEmptyHost(t)
		if out, stderr, code := runActIn(t, src, "state", "import", target, "--json"); code != 0 {
			t.Fatalf("state import: exit %d; stdout=%s stderr=%s", code, out, stderr)
		}
		imxAssertClean(t, target)
	})
	t.Run("from-cwd", func(t *testing.T) {
		src := imxSource(t, false)
		target := imxEmptyHost(t)
		if out, stderr, code := runActIn(t, target, "state", "import", "--from-cwd", src, "--json"); code != 0 {
			t.Fatalf("state import --from-cwd: exit %d; stdout=%s stderr=%s", code, out, stderr)
		}
		imxAssertClean(t, target)
	})
	t.Run("from-cwd from an older store", func(t *testing.T) {
		src := imxSource(t, true)
		target := imxEmptyHost(t)
		if out, stderr, code := runActIn(t, target, "state", "import", "--from-cwd", src, "--json"); code != 0 {
			t.Fatalf("state import --from-cwd: exit %d; stdout=%s stderr=%s", code, out, stderr)
		}
		imxAssertClean(t, target)
	})
}
