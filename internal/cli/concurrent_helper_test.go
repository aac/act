package cli

// Helpers for the concurrency / rebase-contention end-to-end tests.
//
// These tests drive real `act` and `git` subprocesses against a shared bare
// repo to exercise the multi-writer scenarios from spec-v2 §7.4 / brief
// "Multi-writer semantics". A single `act` binary is built once in TestMain
// and reused by every test in the package.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// actBinaryPath is set by TestMain to the absolute path of the freshly-built
// `act` binary. Tests that need to invoke `act` as a subprocess read this.
var actBinaryPath string

// TestMain compiles the act binary into a temp file shared across the
// package's tests, then runs the rest of the suite. The binary is removed
// on package exit.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "act-test-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: mkdtemp: %v\n", err)
		os.Exit(2)
	}
	defer os.RemoveAll(tmp)

	bin := filepath.Join(tmp, "act")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/act")
	// Walk up to repo root so `go build ./cmd/act` resolves regardless of
	// where `go test` ran from. The cli package lives at internal/cli, so
	// two `..` reach the module root.
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: getcwd: %v\n", err)
		os.Exit(2)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: go build act: %v\n", err)
		os.Exit(2)
	}
	actBinaryPath = bin
	os.Exit(m.Run())
}

// runGit runs `git <args>` in dir and fails the test on non-zero exit.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
}

// runGitOut runs `git <args>` in dir and returns combined output. Errors
// are returned, not fatal, so callers can implement retry-on-rejection.
func runGitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// runAct invokes the prebuilt act binary in `site` with `args` and returns
// stdout, stderr, exit code. Does not fail the test on non-zero exit; the
// caller asserts.
func runAct(t *testing.T, site string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	if actBinaryPath == "" {
		t.Fatalf("runAct: act binary not built (TestMain did not run?)")
	}
	cmd := exec.Command(actBinaryPath, args...)
	cmd.Dir = site
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("runAct: %s %s: %v", site, strings.Join(args, " "), err)
		}
	}
	return so.String(), se.String(), code
}

// mustRunAct invokes runAct and fails the test if exitCode != want.
func mustRunAct(t *testing.T, site string, want int, args ...string) (stdout, stderr string) {
	t.Helper()
	so, se, code := runAct(t, site, args...)
	if code != want {
		t.Fatalf("act %s in %s: exit %d (want %d)\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), site, code, want, so, se)
	}
	return so, se
}

// pushWithRebase runs `git push origin main`. If push is rejected
// (non-fast-forward), it runs `git pull --rebase origin main` and retries
// up to maxRetries. Returns the final combined output and error.
func pushWithRebase(t *testing.T, dir string, maxRetries int) {
	t.Helper()
	for attempt := 0; attempt <= maxRetries; attempt++ {
		out, err := runGitOut(dir, "push", "origin", "main")
		if err == nil {
			return
		}
		// On non-fast-forward, try pull --rebase + retry. Any other
		// error is fatal. The "rejected" / "non-fast-forward" / "fetch
		// first" tokens cover the common variants of git's message.
		lo := strings.ToLower(out)
		if !strings.Contains(lo, "rejected") && !strings.Contains(lo, "non-fast-forward") && !strings.Contains(lo, "fetch first") {
			t.Fatalf("push (in %s): unexpected error: %v\n%s", dir, err, out)
		}
		if attempt == maxRetries {
			t.Fatalf("push (in %s): exhausted retries (%d)\n%s", dir, maxRetries, out)
		}
		if pullOut, perr := runGitOut(dir, "pull", "--rebase", "origin", "main"); perr != nil {
			t.Fatalf("pull --rebase (in %s): %v\n%s", dir, perr, pullOut)
		}
	}
}

// makeShared builds a three-tree fixture: a bare repo at <tmp>/remote.git
// and two clones at <tmp>/siteA and <tmp>/siteB, each with its own .act/
// initialized via `act init` and a distinct git user.email so node_id
// differs. Returns absolute paths for the three locations.
//
// `.act/config.json` is per-site (different node_id per writer) and so
// MUST NOT be committed to the shared remote — otherwise a `git pull`
// from another site would clobber the local node identity. The fixture
// adds `.act/config.json` to `.git/info/exclude` on every site so it
// stays untracked, and only commits a `.act/ops/` directory anchor so
// later op-files have a tracked parent dir.
func makeShared(t *testing.T) (siteA, siteB, bareDir string) {
	t.Helper()
	root := t.TempDir()
	bareDir = filepath.Join(root, "remote.git")
	siteA = filepath.Join(root, "siteA")
	siteB = filepath.Join(root, "siteB")

	// Bare remote.
	runGit(t, root, "init", "--bare", "-q", "-b", "main", bareDir)

	// Site A: clone, configure identity, init act (untracked
	// config.json), commit a .gitignore + ops anchor, push.
	runGit(t, root, "clone", "-q", bareDir, siteA)
	configureSite(t, siteA, "alice@example.com", "Alice")
	excludeConfig(t, siteA)
	mustRunAct(t, siteA, 0, "init", "--json")
	seedRepo(t, siteA, "act init A")
	runGit(t, siteA, "push", "-u", "origin", "main")

	// Site B: clone (gets A's seed commit), configure distinct identity,
	// exclude config.json before init so the file never enters git's
	// tracked set, then init --force to write B's local config.
	runGit(t, root, "clone", "-q", bareDir, siteB)
	configureSite(t, siteB, "bob@example.com", "Bob")
	excludeConfig(t, siteB)
	mustRunAct(t, siteB, 0, "init", "--force", "--json")

	// Sanity: the two node_ids must differ. Read each .act/config.json.
	idA := readNodeID(t, siteA)
	idB := readNodeID(t, siteB)
	if idA == idB {
		t.Fatalf("makeShared: siteA and siteB share node_id %q; tests need distinct ids", idA)
	}
	return siteA, siteB, bareDir
}

// excludeConfig adds .act/config.json (and the local sqlite index) to the
// per-clone .git/info/exclude file so they never become tracked. This
// preserves per-site node identity across pulls.
func excludeConfig(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, ".git", "info", "exclude")
	body := "\n.act/config.json\n.act/index.db\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("excludeConfig: open: %v", err)
	}
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		t.Fatalf("excludeConfig: write: %v", err)
	}
	f.Close()
}

// seedRepo commits a tiny anchor so the clone has something to push and
// any subsequent .act/ops/ writes have a parent on the index. The anchor
// is a top-level README plus a .gitignore that excludes config.json.
func seedRepo(t *testing.T, dir, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte(".act/config.json\n.act/index.db\n"), 0o644); err != nil {
		t.Fatalf("seedRepo: gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README"),
		[]byte("act concurrency fixture\n"), 0o644); err != nil {
		t.Fatalf("seedRepo: README: %v", err)
	}
	runGit(t, dir, "add", ".gitignore", "README")
	runGit(t, dir, "commit", "-q", "--no-verify", "-m", msg)
}

// configureSite sets the git identity, disables gpg signing, and clears
// the SSH-signing override that some sandboxes inject. Idempotent.
func configureSite(t *testing.T, dir, email, name string) {
	t.Helper()
	runGit(t, dir, "config", "user.email", email)
	runGit(t, dir, "config", "user.name", name)
	runGit(t, dir, "config", "commit.gpgsign", "false")
	runGit(t, dir, "config", "tag.gpgsign", "false")
	// Belt-and-braces: some environments set gpg.format=ssh with a
	// per-user signing key that fails offline. Local override unsets it.
	runGit(t, dir, "config", "gpg.format", "openpgp")
}

// workingTreeClean reports whether `git status --porcelain` is empty.
func workingTreeClean(t *testing.T, dir string) bool {
	t.Helper()
	out, err := runGitOut(dir, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status (in %s): %v\n%s", dir, err, out)
	}
	return strings.TrimSpace(out) == ""
}

// readNodeID reads .act/config.json from dir and returns the node_id.
func readNodeID(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".act", "config.json"))
	if err != nil {
		t.Fatalf("readNodeID: %v", err)
	}
	var cfg struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("readNodeID unmarshal: %v", err)
	}
	return cfg.NodeID
}

// listOpFiles returns the relative paths of every op file under
// .act/ops/<issueID>/, sorted lexicographically.
func listOpFiles(t *testing.T, dir, issueID string) []string {
	t.Helper()
	root := filepath.Join(dir, ".act", "ops", issueID)
	var out []string
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return out
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("listOpFiles: %v", err)
	}
	return out
}

// readShowJSON runs `act show --json <id>` in `dir` and decodes the result.
// Returns the rendered fields map. On exit code != 0 or invalid JSON the
// test fails.
func readShowJSON(t *testing.T, dir, id string) map[string]any {
	t.Helper()
	so, _ := mustRunAct(t, dir, 0, "show", "--json", id)
	var m map[string]any
	if err := json.Unmarshal([]byte(so), &m); err != nil {
		t.Fatalf("show --json %s in %s: invalid JSON: %v\n%s", id, dir, err, so)
	}
	return m
}

// createIssueOnA runs `act create --json --push <title>` on siteA and
// returns the new issue id.
func createIssueOnA(t *testing.T, siteA, title string) string {
	t.Helper()
	so, _ := mustRunAct(t, siteA, 0, "create", "--json", "--push", title)
	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(so), &res); err != nil {
		t.Fatalf("create JSON parse: %v\n%s", err, so)
	}
	if res.ID == "" {
		t.Fatalf("create: empty id\n%s", so)
	}
	return res.ID
}

// pullRebase runs `git pull --rebase origin main` and fails on error.
func pullRebase(t *testing.T, dir string) {
	t.Helper()
	if out, err := runGitOut(dir, "pull", "--rebase", "origin", "main"); err != nil {
		t.Fatalf("pull --rebase (in %s): %v\n%s", dir, err, out)
	}
}
