package cli

// Doc-claim tests for act-66f987: `act init` must not mutate the HOST repo
// unrequested.
//
// Field data behind the ticket (2026-07-28/29 mini bootstrap, 17 repos):
// init committed a CONTRIBUTING.md stanza to the host repo on every repo
// with a public-looking remote and 13 of 17 host repos needed hand-
// reverting. The claims asserted here are the user-visible contract that
// replaced that behavior, as stated in `act init --help` and README:
//
//   - No host commit unless --commit-host.
//   - No CONTRIBUTING.md edit unless --contributing; a public-looking
//     remote produces a suggestion only.
//   - An existing pre-close gate (.act/hooks/close) is never touched, and
//     no sample is dropped beside it.
//
// The assertions are at the RunInit boundary plus the git state of a real
// host repo — the surface the field failure was observed on. `cmd/act`
// flag help is covered by the docs sweep registry.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mustAddOrigin points the repo's origin at a public-looking URL. No
// network is involved: init only reads `git remote get-url origin`.
func mustAddOrigin(t *testing.T, root, url string) {
	t.Helper()
	cmd := exec.Command("git", "remote", "add", "origin", url)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add origin: %v\n%s", err, out)
	}
}

// TestDocClaim_Init_NoHostCommitByDefault is the core act-66f987 claim: a
// default `act init` on a real host repo with a public-looking remote
// leaves the host repo's commit count unchanged, reports the files it
// wrote as uncommitted, and reports the CONTRIBUTING stanza as merely
// suggested.
func TestDocClaim_Init_NoHostCommitByDefault(t *testing.T) {
	root := makeRealGitRepo(t)
	mustAddOrigin(t, root, "https://github.com/example/repo.git")

	before := mustGitCommitCount(t, root)
	beforeHead := strings.TrimSpace(mustGitOutput(t, root, "rev-parse", "HEAD"))

	out, code := RunInit(root, InitOptions{MachineID: "m", GitEmail: "alice@example.com"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	succ, ok := out.(successOutput)
	if !ok {
		t.Fatalf("output type = %T, want successOutput", out)
	}

	if succ.HostCommitted {
		t.Errorf("HostCommitted = true; act init must not commit to the host repo without --commit-host")
	}
	if after := mustGitCommitCount(t, root); after != before {
		t.Errorf("host commit count went %d -> %d; want unchanged", before, after)
	}
	if afterHead := strings.TrimSpace(mustGitOutput(t, root, "rev-parse", "HEAD")); afterHead != beforeHead {
		t.Errorf("host HEAD moved %s -> %s; want unchanged", beforeHead, afterHead)
	}

	// The .gitignore entry is still WRITTEN (act's own correctness depends
	// on the host not tracking .act/) — it is just left for the operator.
	if !succ.GitignoreUpdated {
		t.Errorf("GitignoreUpdated = false; init should still write the .act/ ignore entry")
	}
	if !containsPath(succ.HostFilesUncommitted, ".gitignore") {
		t.Errorf("HostFilesUncommitted = %v, want it to name .gitignore", succ.HostFilesUncommitted)
	}
	status := mustGitOutput(t, root, "status", "--porcelain", "--", ".gitignore")
	if strings.TrimSpace(status) == "" {
		t.Errorf("`git status --porcelain -- .gitignore` is empty; the ignore entry should be sitting uncommitted in the working tree")
	}

	// CONTRIBUTING.md: suggested, never written.
	if succ.ContributingEmitted {
		t.Errorf("ContributingEmitted = true without --contributing")
	}
	if !succ.ContributingSuggested {
		t.Errorf("ContributingSuggested = false; a public-looking remote should produce the printed offer")
	}
	if _, err := os.Stat(filepath.Join(root, "CONTRIBUTING.md")); err == nil {
		t.Errorf("CONTRIBUTING.md was created; act init must not edit it without --contributing")
	}
	if containsPath(succ.HostFilesUncommitted, "CONTRIBUTING.md") {
		t.Errorf("HostFilesUncommitted names CONTRIBUTING.md but no stanza was written: %v", succ.HostFilesUncommitted)
	}
}

// TestDocClaim_Init_NoContributingWithoutOptIn is the narrow half of the
// claim on a plain (non-committing) repo: the public-remote heuristic no
// longer writes the file by itself, and --contributing still does.
func TestDocClaim_Init_NoContributingWithoutOptIn(t *testing.T) {
	root := makeRepo(t)
	mustAddOrigin(t, root, "git@github.com:example/repo.git")

	out, code := RunInit(root, InitOptions{MachineID: "m", GitEmail: "e"})
	if code != 0 {
		t.Fatalf("init code = %d; out=%+v", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, "CONTRIBUTING.md")); !os.IsNotExist(err) {
		t.Fatalf("CONTRIBUTING.md exists after a default init (stat err = %v); want not created", err)
	}

	// The opt-in still works, and re-init with it is what writes the file.
	if _, code := RunInit(root, InitOptions{Force: true, MachineID: "m", GitEmail: "e", Contributing: true}); code != 0 {
		t.Fatalf("re-init --contributing code = %d", code)
	}
	body, err := os.ReadFile(filepath.Join(root, "CONTRIBUTING.md"))
	if err != nil {
		t.Fatalf("read CONTRIBUTING.md after --contributing: %v", err)
	}
	if !strings.Contains(string(body), contributingStanzaStart) {
		t.Errorf("CONTRIBUTING.md missing the stanza marker: %q", string(body))
	}
}

// TestDocClaim_Init_PreservesExistingCloseHook asserts the second half of
// act-66f987: an already-active pre-close gate at .act/hooks/close survives
// init and --force re-init byte-identical, and no close.sample is dropped
// beside it.
func TestDocClaim_Init_PreservesExistingCloseHook(t *testing.T) {
	root := makeRepo(t)
	hooks := filepath.Join(root, ".act", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	gate := filepath.Join(hooks, "close")
	const gateBody = "#!/bin/sh\n# project gate\nexit 0\n"
	if err := os.WriteFile(gate, []byte(gateBody), 0o755); err != nil {
		t.Fatalf("seed close hook: %v", err)
	}

	for _, force := range []bool{false, true} {
		if _, code := RunInit(root, InitOptions{Force: force, MachineID: "m", GitEmail: "e"}); code != 0 {
			t.Fatalf("init(force=%v) code = %d", force, code)
		}
		got, err := os.ReadFile(gate)
		if err != nil {
			t.Fatalf("read close hook after init(force=%v): %v", force, err)
		}
		if string(got) != gateBody {
			t.Fatalf("close hook rewritten by init(force=%v):\n got %q\nwant %q", force, string(got), gateBody)
		}
		if _, err := os.Stat(filepath.Join(hooks, "close.sample")); err == nil {
			t.Fatalf("init(force=%v) dropped close.sample beside an active close hook", force)
		}
	}
}

// containsPath is a tiny slice membership helper local to this file
// (`contains` is already taken by the substring helper in log_test.go).
func containsPath(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
