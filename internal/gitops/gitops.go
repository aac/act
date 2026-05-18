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

// ActGitOps is the handle authorized to write act ops and query the act
// state's git history. Under Phase 1 of the coordination-plane design
// (docs/coordination-plane-design.md delta item 2) this will target the
// nested .act/ repo; today it shares findRepoRoot with HostGitOps, so both
// resolve to the same working tree. The split is a type-system enforcement
// of the "writes go through act's handle" invariant — every call site that
// stages an op or commits an act change must use *ActGitOps.
//
// ActGitOps is a type alias for *GitOps so it exposes the full write
// surface (Commit, StageOpFile, Push, PullRebase, SquashActOpRange) plus
// all the read helpers. Migration consists of flipping construction calls
// from NewGitOps to NewActGitOps; the method set is identical.
type ActGitOps = GitOps

// NewActGitOps constructs an ActGitOps for the writer side of the dual-
// handle split. Today this is the same construction as NewGitOps; the
// distinct name documents the writer-vs-reader role at every call site.
func NewActGitOps(repoRoot string) *ActGitOps {
	return NewGitOps(repoRoot)
}

// HostGitOps is the read-only handle act uses to scan the host repo's
// commit log for `(act-XXXX)` markers. Today the host and act states
// share a working tree; under Phase 1 the host repo and the nested act
// repo will be distinct git directories and this handle will target the
// host (the act-9e8c findRepoRoot resolver work).
//
// HostGitOps deliberately exposes only the read surface that doctor and
// show need (RepoRoot, WorkCommitsForIssue). The write methods on the
// underlying *GitOps (Commit, StageOpFile, Push, PullRebase,
// SquashActOpRange) are a compile-time absence from HostGitOps's method
// set — the only way to perform writes is to drop down to *ActGitOps,
// which makes the policy ("act never writes to the host repo") enforced
// by the type system rather than by convention.
type HostGitOps struct {
	inner *GitOps
}

// NewHostGitOps constructs a HostGitOps for the reader side of the dual-
// handle split. The repoRoot argument is the host repo's working tree —
// under Phase 1, the same path as the act state's root; once the nested
// repo migration lands, the two roots diverge and this constructor will
// be passed the host root specifically.
func NewHostGitOps(repoRoot string) *HostGitOps {
	return &HostGitOps{inner: NewGitOps(repoRoot)}
}

// RepoRoot returns the working tree path the host handle targets.
func (h *HostGitOps) RepoRoot() string {
	return h.inner.RepoRoot
}

// WorkCommitsForIssue surfaces the `(act-<markerHex>` marker grep against
// the host repo's git log. Read-only operation — see *GitOps.WorkCommitsForIssue
// for the contract.
func (h *HostGitOps) WorkCommitsForIssue(markerHex string, limit int) ([]WorkCommit, error) {
	return h.inner.WorkCommitsForIssue(markerHex, limit)
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

// WorkCommit is a single git commit attributed to an issue via the
// `(act-XXXX)` marker convention.
type WorkCommit struct {
	SHA        string `json:"sha"`
	Subject    string `json:"subject"`
	AuthorDate string `json:"author_date"`
}

// WorkCommitsForIssue runs `git log --all --extended-regexp --grep=<pattern>`
// and returns up to limit matching commits, most-recent-first. The pattern
// matches either the historical subject-line form `(act-<markerHex>` or the
// trailer form `Act-Id: act-<markerHex>` introduced in act-c4c5 (see docs/
// coordination-plane-design.md v2.1 "Marker placement"). Both shapes are
// recognized for resolution; the trailer is the only emission form going
// forward. The grep operates against the full commit message (subject +
// body), so trailers in the body are matched cleanly.
//
// The caller passes the hex tail of the canonical commit marker — exactly
// MinShortHexLen hex chars for ids at or above that floor (6 since
// act-f9a0), and the full hex tail verbatim for historical ids that were
// minted shorter than the current floor (e.g. 4-hex ids from pre-act-f9a0
// repos). Result includes commits whose marker is the canonical short form
// OR any longer extended marker that starts with the same prefix (i.e.
// same-issue ids that grew on collision) — the pattern is anchored on the
// `act-<markerHex>` substring, not a fixed-length window.
//
// The function accepts any markerHex of length >= 4 so historical 4-hex
// ids stay matchable; 4 is the on-disk syntax floor (idPattern), not the
// generation floor (MinShortHexLen).
//
// limit=0 means unbounded.
//
// An empty repository (no commits yet) is treated as "no matches" rather
// than an error: `git log` on a repo with no HEAD exits non-zero, but to
// the caller the answer "this issue has no work commits" is the right
// shape.
func (g *GitOps) WorkCommitsForIssue(markerHex string, limit int) ([]WorkCommit, error) {
	if len(markerHex) < 4 {
		return nil, fmt.Errorf("gitops: WorkCommitsForIssue: markerHex length %d < 4", len(markerHex))
	}
	// POSIX ERE alternation matching either:
	//   - the historical subject form `(act-<hex>` (open-paren guards
	//     against arbitrary "act-XXXX" text in unrelated commits), or
	//   - the trailer form `Act-Id: act-<hex>` (any case-sensitive
	//     position; git --grep matches the full message body).
	// `\(` escapes the literal open paren in ERE so it isn't read as a
	// grouping operator.
	pattern := `(\(act-` + markerHex + `|Act-Id: act-` + markerHex + `)`
	args := []string{
		"log", "--all",
		"--extended-regexp",
		"--grep=" + pattern,
		// Tab-separated triplet so we can split unambiguously even if the
		// subject contains tabs (it normally doesn't, but author_date
		// follows ISO-8601 with colons that would confuse a colon split).
		"--pretty=format:%H%x09%s%x09%aI",
	}
	if limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", limit))
	}
	out, err := g.run(args...)
	if err != nil {
		// Empty repo / no HEAD → treat as no matches.
		if strings.Contains(err.Error(), "does not have any commits yet") ||
			strings.Contains(err.Error(), "bad default revision 'HEAD'") {
			return nil, nil
		}
		return nil, err
	}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return nil, nil
	}
	var commits []WorkCommit
	for _, line := range strings.Split(out, "\n") {
		// Format: "<sha>\t<subject>\t<author_date>".
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		commits = append(commits, WorkCommit{
			SHA:        parts[0],
			Subject:    parts[1],
			AuthorDate: parts[2],
		})
	}
	return commits, nil
}
