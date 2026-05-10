// Package gitops provides a concrete, shellout-based implementation of the
// git mutations needed by the writer pipeline (auto-commit, push, atomic
// claim's pull-rebase, and squash-of-contiguous-act-op-range).
//
// Design notes:
//
//   - Every method invokes /usr/bin/env git via os/exec with a fixed working
//     directory (RepoRoot). No shell is involved, so paths with spaces and
//     unusual characters round-trip safely.
//   - Default verify behavior matches spec §5.B: op-commits use --no-verify
//     because the commit only touches .act/ops/**, which the host's
//     pre-commit hooks should not police. Set Verify=true to opt in.
//   - The concrete *GitOps satisfies the claim.GitOps interface declared by
//     act-9824 (Commit / PullRebase / Push). No adjustment to that interface
//     was required; this package's API is a strict superset.
package gitops

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/aac/act/internal/claim"
)

// Compile-time assertion: *GitOps satisfies the claim.GitOps interface
// declared by act-9824. If a future signature drift breaks this, the build
// will fail loudly here rather than at the call site.
var _ claim.GitOps = (*GitOps)(nil)

// ErrNoRemote is returned by PullRebase and Push when the working tree has
// no upstream configured. Callers translate this to spec exit code 2 (usage
// error) when the user explicitly asked for --push.
//
// Aliased to claim.ErrNoUpstream so the claim package's PullRebase
// short-circuit (act-fdb2) detects it via errors.Is without the gitops
// package having to expose a second sentinel.
var ErrNoRemote = claim.ErrNoUpstream

// GitOps is a concrete implementation of the git side-effects used by the
// claim and write-op flows. The zero value is not safe; use NewGitOps.
type GitOps struct {
	// RepoRoot is the absolute path to the working tree root. All git
	// commands run with -C <RepoRoot>; relative paths passed to StageOpFile
	// are resolved by git relative to this directory.
	RepoRoot string
	// Verify, when true, causes Commit to omit --no-verify so the host's
	// pre-commit hooks run. Default (false) matches spec §5.B.
	Verify bool

	// runner is an internal indirection so tests can assert the exact argv
	// passed to git. Defaults to exec.Command. Exposed via WithRunner.
	runner func(name string, args ...string) *exec.Cmd
}

// NewGitOps constructs a GitOps rooted at repoRoot with default settings
// (Verify=false). Verify can be flipped on the returned struct directly.
func NewGitOps(repoRoot string) *GitOps {
	return &GitOps{RepoRoot: repoRoot, runner: exec.Command}
}

// run executes `git <args...>` with cwd=RepoRoot and returns stdout. stderr
// is included in the error message on failure.
func (g *GitOps) run(args ...string) (string, error) {
	r := g.runner
	if r == nil {
		r = exec.Command
	}
	cmd := r("git", args...)
	cmd.Dir = g.RepoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w (stderr: %s)",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// StageOpFile runs `git add <opPath>` with cwd=RepoRoot. opPath may be
// absolute or relative to RepoRoot.
func (g *GitOps) StageOpFile(opPath string) error {
	if opPath == "" {
		return fmt.Errorf("gitops: empty op path")
	}
	if _, err := g.run("add", "--", opPath); err != nil {
		return err
	}
	return nil
}

// Commit creates a single commit with the given message. By default the
// commit uses --no-verify (spec §5.B); set GitOps.Verify=true to run host
// pre-commit hooks. Cross-platform safe: no shell, no /dev/null redirect.
func (g *GitOps) Commit(message string) error {
	if message == "" {
		return fmt.Errorf("gitops: empty commit message")
	}
	args := []string{"commit", "-m", message}
	if !g.Verify {
		args = append(args, "--no-verify")
	}
	if _, err := g.run(args...); err != nil {
		return err
	}
	return nil
}

// PullRebase runs `git pull --rebase`. If no upstream is configured the
// method returns ErrNoRemote so the caller can decide whether to surface a
// usage error or silently no-op (e.g. atomic claim with --isolated).
func (g *GitOps) PullRebase() error {
	if _, err := g.upstream(); err != nil {
		return err
	}
	if _, err := g.run("pull", "--rebase"); err != nil {
		return err
	}
	return nil
}

// Push runs `git push -u origin <current-branch>`. Returns ErrNoRemote if
// the repo has no `origin` remote at all; an unconfigured upstream on the
// branch is still pushable because we pass `-u origin <branch>` explicitly.
func (g *GitOps) Push() error {
	if !g.hasOriginRemote() {
		return ErrNoRemote
	}
	branch, err := g.CurrentBranch()
	if err != nil {
		return err
	}
	if _, err := g.run("push", "-u", "origin", branch); err != nil {
		return err
	}
	return nil
}

// IsClean reports whether the working tree has no staged or unstaged
// changes (`git status --porcelain` produces empty output).
func (g *GitOps) IsClean() (bool, error) {
	out, err := g.run("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// HasNonActChanges reports whether the working tree has any staged or unstaged
// changes outside the .act/ tree. Used by the close path to decide whether to
// auto-commit standalone (clean elsewhere → standalone close commit) or leave
// the staged close op for the agent's next git commit to subsume (act-a659).
//
// Detection: parse `git status --porcelain` and ignore any path with the
// .act/ prefix. The porcelain v1 format puts paths in columns 4..N; rename
// entries (`R `) use ` -> ` between old and new. We treat both endpoints as
// .act/ if either is — a rename moving into or out of .act/ counts as an
// act-only change for our purposes.
func (g *GitOps) HasNonActChanges() (bool, error) {
	out, err := g.run("status", "--porcelain")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		path := line[3:]
		// Handle rename "old -> new"
		if i := strings.Index(path, " -> "); i >= 0 {
			oldPath := strings.TrimSpace(path[:i])
			newPath := strings.TrimSpace(path[i+4:])
			if !isActPath(oldPath) || !isActPath(newPath) {
				return true, nil
			}
			continue
		}
		if !isActPath(strings.TrimSpace(path)) {
			return true, nil
		}
	}
	return false, nil
}

// isActPath reports whether p lives under the .act/ tree at the repo root.
// Quoted paths (porcelain wraps paths containing unusual chars in double
// quotes) are unwrapped first.
func isActPath(p string) bool {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		p = p[1 : len(p)-1]
	}
	return p == ".act" || strings.HasPrefix(p, ".act/")
}

// CurrentBranch returns the short-form current branch name (e.g. "main").
// Detached-HEAD repositories return "HEAD"; callers that need to reject
// detached-HEAD should check the returned value.
func (g *GitOps) CurrentBranch() (string, error) {
	out, err := g.run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// upstream returns the symbolic upstream of the current branch (e.g.
// "origin/main") or ErrNoRemote if none is configured.
func (g *GitOps) upstream() (string, error) {
	out, err := g.run("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		// `git rev-parse @{u}` exits non-zero when there is no upstream;
		// translate any failure here to ErrNoRemote (the upstream check is
		// purely advisory in our flow).
		return "", ErrNoRemote
	}
	up := strings.TrimSpace(out)
	if up == "" {
		return "", ErrNoRemote
	}
	return up, nil
}

// hasOriginRemote returns true iff `git remote` lists an `origin` entry.
func (g *GitOps) hasOriginRemote() bool {
	out, err := g.run("remote")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "origin" {
			return true
		}
	}
	return false
}

// ContiguousActOpRange walks `git log` from HEAD looking back for a
// maximal contiguous run of commits whose subject starts with "act-op:".
// Returns (firstSHA, lastSHA, count, nil): firstSHA is the OLDEST act-op
// commit in the run; lastSHA is HEAD if HEAD itself is an act-op commit;
// count is the run length. If HEAD is not an act-op commit the run is
// empty and (\"\", \"\", 0, nil) is returned.
func (g *GitOps) ContiguousActOpRange() (string, string, int, error) {
	// `git log --format=%H%x00%s` emits SHA<NUL>SUBJECT<LF> so a NUL split
	// makes subject parsing unambiguous even if the subject contains tabs.
	out, err := g.run("log", "--format=%H%x09%s", "HEAD")
	if err != nil {
		return "", "", 0, err
	}
	var first, last string
	count := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		// Format: "<sha>\t<subject>".
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			break
		}
		sha := line[:tab]
		subject := line[tab+1:]
		if !strings.HasPrefix(subject, "act-op:") {
			break
		}
		if last == "" {
			last = sha
		}
		first = sha
		count++
	}
	return first, last, count, nil
}

// SquashActOpRange collapses the contiguous run [firstSHA..lastSHA] (where
// firstSHA is the OLDEST commit in the range and lastSHA is HEAD) into a
// single squashed commit with message
// `act-squash: writer_version=<maxWriterVersion>`.
//
// Returns nil with no side effects when count == 1 (single commit).
//
// maxWriterVersion is supplied by the caller (typically the writer that
// inspected envelopes inside the range). The caller is responsible for the
// version_skew gate per spec §5.B "Squash-and-push refused on version_skew";
// this method is a pure git-level squash and does not consult writer
// versions on its own.
func (g *GitOps) SquashActOpRange(firstSHA, lastSHA, maxWriterVersion string) error {
	if firstSHA == "" || lastSHA == "" {
		return fmt.Errorf("gitops: empty SHA")
	}
	if firstSHA == lastSHA {
		// Single-commit range: no-op.
		return nil
	}
	if maxWriterVersion == "" {
		return fmt.Errorf("gitops: empty maxWriterVersion")
	}
	// Resolve parent of firstSHA. `git rev-parse <sha>^` exits non-zero if
	// firstSHA is the root commit; treat that as an error (squashing the
	// root commit is unsupported).
	parent, err := g.run("rev-parse", firstSHA+"^")
	if err != nil {
		return fmt.Errorf("gitops: parent of %s: %w", firstSHA, err)
	}
	parent = strings.TrimSpace(parent)
	if _, err := g.run("reset", "--soft", parent); err != nil {
		return err
	}
	msg := fmt.Sprintf("act-squash: writer_version=%s", maxWriterVersion)
	args := []string{"commit", "-m", msg}
	if !g.Verify {
		args = append(args, "--no-verify")
	}
	if _, err := g.run(args...); err != nil {
		return err
	}
	return nil
}
