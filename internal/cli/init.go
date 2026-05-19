// Package cli wires the act subcommands into a single binary entry point.
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aac/act/internal/config"
)

// rfc3339Millis is the millisecond-precision RFC 3339 layout used throughout
// the on-disk format. It matches the HLC wall format so timestamps written by
// init are comparable with those embedded in op files.
const rfc3339Millis = "2006-01-02T15:04:05.000Z"

// writerVersion is the on-disk writer version stamped into config.json.
const writerVersion = "0.1.0"

// errorOutput is the structured shape returned to the caller when init refuses.
type errorOutput struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// successOutput is the structured shape returned on a successful init under
// Phase 1 (coordination-plane design, docs/coordination-plane-design.md).
//
// Phase 1 bootstraps a nested .act/ git repo distinct from the host repo, so
// the success envelope now distinguishes the act-side commit from the host-side
// effects:
//
//   - NestedCommitted reflects whether the nested .act/ repo's initial commit
//     landed. This is the load-bearing piece: without it the act state has
//     no history and doctor cannot reconcile.
//   - HostCommitted reflects whether the host repo's .gitignore +
//     pre-commit hook + (optional) CONTRIBUTING update committed in a single
//     host-side commit. May be false when the host repo has no commits yet
//     (we still write the files, just don't auto-commit) or when the commit
//     step failed; the on-disk state is still valid in either case.
//   - PartialFailures lists per-step warnings: nested-commit ok but host
//     gitignore failed, hook install failed, CONTRIBUTING stanza failed,
//     etc. Per the failure-mode contract (docs/coordination-plane-design.md
//     "Failure-mode write order"), we leave nested in place and surface the
//     partial state for the operator to remediate.
type successOutput struct {
	OK                  bool     `json:"ok"`
	ActDir              string   `json:"act_dir"`
	NodeID              string   `json:"node_id"`
	NestedCommitted     bool     `json:"nested_committed"`
	HostCommitted       bool     `json:"host_committed"`
	GitignoreUpdated    bool     `json:"gitignore_updated"`
	HookInstalled       bool     `json:"hook_installed"`
	ContributingEmitted bool     `json:"contributing_emitted,omitempty"`
	PartialFailures     []string `json:"partial_failures,omitempty"`
}

// gitignoreEntry is the host-repo .gitignore line act init appends. Matching
// is exact (whole-line, trim-space); other shapes (`/.act`, `**/.act/`,
// `.act` without trailing slash, scoped via a parent .gitignore) are
// detected separately by ignoresActPath.
//
// act init writes ONLY this entry. It does NOT write `.ask/` or any other
// non-act path to the host `.gitignore` — sibling tools (ask, etc.) own
// their own .gitignore footprint (act-d4a2). The test
// TestDocClaim_Init_GitignoreNoAskEntry asserts this at the user-visible
// boundary.
const gitignoreEntry = ".act/"

// contributingStanzaStart and contributingStanzaEnd wrap the act-emitted
// CONTRIBUTING.md section. The HTML-comment delimiters are the idempotency
// key: re-init sees the start marker and skips re-emission. The wording
// between the markers can evolve without breaking the skip check.
const (
	contributingStanzaStart = "<!-- act:contributing-stanza:start -->"
	contributingStanzaEnd   = "<!-- act:contributing-stanza:end -->"
)

// contributingStanzaBody is the human-readable content emitted between the
// start/end markers. External contributors should not need to interact with
// the Act-Id trailer convention; this stanza tells them so explicitly.
const contributingStanzaBody = `## act commit-marker convention

This repo uses [act](https://github.com/aac/act) for agent task tracking.
Agent-authored commits include an ` + "`Act-Id: act-XXXX`" + ` trailer in the commit
body that pairs the commit with its tracked issue.

You don't need to interact with this convention — ` + "`Act-Id:`" + ` trailers are
ignored by conventional-commit linters, semantic-release, and CHANGELOG
generators, and have no effect on merge or review. If you'd like to add
them to your own commits, see act's docs; otherwise, ignore.
`

// preCommitHookHeader marks the act-managed region of the host repo's
// pre-commit hook. When re-init runs we look for this marker to decide
// whether to skip (already installed) or augment (existing user hook
// without the act block).
const preCommitHookHeader = "# act: reject staged .act/* paths"

// preCommitHookBlock is the shell snippet appended to the host's
// .git/hooks/pre-commit. It's deliberately POSIX-sh, no bashisms, so it
// works on the broadest set of platforms. Rejects any commit whose staged
// tree includes a .act/ entry with a clear remedy.
const preCommitHookBlock = `# act: reject staged .act/* paths (managed by act init; do not remove)
if git diff --cached --name-only -- '.act' '.act/' 2>/dev/null | grep -qE '^\.act(/|$)'; then
  echo "act: refusing to commit .act/ paths to the host repo." >&2
  echo "  The .act/ tree is the nested act state repo and must not ride host commits." >&2
  echo "  Remedy: git rm -r --cached .act/ && git commit" >&2
  exit 1
fi
`

// publicRemoteRegex matches the URL shape of a host remote we treat as
// "public-looking" for the CONTRIBUTING stanza heuristic. Per the spec:
// github.com / gitlab.com / bitbucket.org over HTTPS or SSH. Anything else
// (private hosts, file://, ssh to a private domain, no remote) is treated
// as not public-looking.
var publicRemoteRegex = regexp.MustCompile(`^(?:https://|git@|ssh://git@)?(?:github\.com|gitlab\.com|bitbucket\.org)[:/]`)

// RunInit executes the `act init` command logic under Phase 1 of the
// coordination-plane design (docs/coordination-plane-design.md). It is
// decoupled from stdin/stdout/exec so tests can drive it directly.
//
// Phase 1 makes act init a two-repo bootstrap: a nested git repo at .act/
// (with its own history for the op-log) plus host-side changes (gitignore
// entry, pre-commit hook rejecting accidental .act/ stages, and an optional
// CONTRIBUTING stanza when the host has a public-looking remote).
//
// Write order is load-bearing per the failure-mode contract: nested init
// runs FIRST. If it fails, no host-side changes happen and the caller can
// retry. If a host-side step fails AFTER nested init succeeds, the nested
// .act/ stays in place and the partial state is surfaced via
// PartialFailures for the operator to remediate.
//
// Returns a JSON-encodable value (errorOutput on failure, successOutput on
// success) plus a process exit code.
//
// The legacy `commit` and `noCommit` parameters from the pre-Phase-1
// single-repo flow are removed: the nested-repo bootstrap commit is what
// `act init` does; there is no flag to suppress it (suppressing would
// leave the act state without an initial commit, which doctor cannot
// reconcile).
func RunInit(repoRoot string, force bool, machineID, gitEmail string, now func() time.Time) (any, int) {
	if now == nil {
		now = time.Now
	}

	// Refuse if repoRoot is not inside a git working tree. We walk upward
	// looking for a `.git` entry; this matches the resolution helper in
	// main.go but defends in depth in case a caller passes an arbitrary path.
	if !hasGitDir(repoRoot) {
		return errorOutput{
			Error:   "not_in_git",
			Message: fmt.Sprintf("act init: %s is not inside a git working tree", repoRoot),
		}, 3
	}

	paths := config.Layout(repoRoot)

	// Refuse re-init unless --force. We detect existing init via .act/config.json
	// (the canonical sentinel; .act/ may be an empty dir on a stale partial
	// init, but config.json is only written by a complete init).
	if _, err := os.Stat(paths.ConfigJSON); err == nil && !force {
		return errorOutput{
			Error:   "act_already_initialized",
			Message: fmt.Sprintf("act init: %s already exists; pass --force to reinitialize", paths.ConfigJSON),
		}, 1
	}

	nodeID := config.ComputeNodeID(machineID, gitEmail)

	// ---------- Step 1: nested .act/ git repo bootstrap ----------
	//
	// Before any host-side change, lay down the nested act state and its
	// initial commit. Per the failure-mode contract: if this step fails we
	// abort entirely, no host change made.

	if err := config.InitDirs(paths); err != nil {
		return errorOutput{
			Error:   "init_dirs_failed",
			Message: err.Error(),
		}, 1
	}

	cfg := config.Config{
		NodeID:         nodeID,
		BundleStrategy: config.BundleStrategyPerSession,
		CreatedAt:      now().UTC().Format(rfc3339Millis),
		Version:        writerVersion,
		LastHLC:        config.HLCState{},
	}
	if err := config.WriteConfig(paths, cfg); err != nil {
		return errorOutput{
			Error:   "write_config_failed",
			Message: err.Error(),
		}, 1
	}

	// Ensure .act/ops/.gitkeep exists so the empty op-log directory is
	// representable in the nested repo's initial commit (git doesn't track
	// empty directories). This is what "empty op-log" means on disk under
	// Phase 1.
	if err := writeKeepFile(filepath.Join(paths.Ops, ".gitkeep")); err != nil {
		return errorOutput{
			Error:   "init_ops_failed",
			Message: err.Error(),
		}, 1
	}

	// Git-init the nested .act/ repo (idempotent: git init on an existing
	// repo is a no-op) and stake out its initial commit. On --force re-init
	// we still call git init; if a .git already exists git init prints a
	// "reinitialized" line and exits 0. The initial-commit step is skipped
	// when the repo already has commits (re-init case).
	nestedCommitted, nerr := bootstrapNestedRepo(paths.Root, machineID, gitEmail)
	if nerr != nil {
		return errorOutput{
			Error:   "nested_init_failed",
			Message: nerr.Error(),
		}, 1
	}

	out := successOutput{
		OK:              true,
		ActDir:          paths.Root,
		NodeID:          nodeID,
		NestedCommitted: nestedCommitted,
	}

	// ---------- Step 2: host-side effects ----------
	//
	// From this point on, errors are partial-failure warnings: the nested
	// repo is durable, so we keep going and surface what didn't land.

	// 2a. Append .act/ to host .gitignore (idempotent).
	if changed, err := ensureGitignoreEntry(repoRoot, gitignoreEntry); err != nil {
		out.PartialFailures = append(out.PartialFailures,
			fmt.Sprintf("gitignore: %v", err))
	} else {
		out.GitignoreUpdated = changed
	}

	// 2b. Install host pre-commit hook that hard-rejects staged .act/* paths.
	if installed, err := installHostPreCommitHook(repoRoot); err != nil {
		out.PartialFailures = append(out.PartialFailures,
			fmt.Sprintf("pre-commit hook: %v", err))
	} else {
		out.HookInstalled = installed
	}

	// 2c. CONTRIBUTING.md stanza (only when host has a public-looking remote).
	if isPublic, _ := hasPublicLookingRemote(repoRoot); isPublic {
		if added, err := ensureContributingStanza(repoRoot); err != nil {
			out.PartialFailures = append(out.PartialFailures,
				fmt.Sprintf("CONTRIBUTING.md: %v", err))
		} else {
			out.ContributingEmitted = added
		}
	}

	// 2d. Commit the host-side changes to the host repo as a single commit.
	// We deliberately do NOT --no-verify here: if a host has a pre-commit
	// hook that rejects something other than .act/ (the act block we just
	// installed doesn't fire because the staged paths are .gitignore /
	// CONTRIBUTING.md / .git/hooks/pre-commit), the user wants to know.
	//
	// Skip the commit attempt if the host repo has no HEAD yet — a fresh
	// `git init` with no initial commit isn't going to accept a commit
	// without prior `git add` of something, and our changes aren't worth
	// forcing the first commit on the user's behalf.
	if hostHasHEAD(repoRoot) && (out.GitignoreUpdated || out.HookInstalled || out.ContributingEmitted) {
		if err := commitHostChanges(repoRoot, out.GitignoreUpdated, out.ContributingEmitted); err != nil {
			out.PartialFailures = append(out.PartialFailures,
				fmt.Sprintf("host commit: %v", err))
		} else {
			out.HostCommitted = true
		}
	}

	return out, 0
}

// writeKeepFile writes a small placeholder so an otherwise-empty directory
// can be committed to the nested repo. The contents are arbitrary; we use
// a one-line comment so a `cat .gitkeep` is self-documenting.
func writeKeepFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte("# placeholder so the empty ops/ directory tracks under the nested act repo\n"), 0o644)
}

// bootstrapNestedRepo runs `git init` inside actDir and, if the repo has no
// commits yet, creates an initial commit pinning the current contents
// (config.json, hooks/, ops/.gitkeep, etc.) as the act state's history root.
//
// Returns (true, nil) when a new commit was created, (false, nil) when the
// nested repo already had commits (re-init, --force on a previously-bootstrapped
// repo), and (false, err) on any failure.
//
// The commit uses --no-verify so the host's pre-commit hook (if any, which we
// are about to install) cannot fire on the nested repo's first commit. The
// nested repo's own hooks dir is empty.
func bootstrapNestedRepo(actDir, machineID, gitEmail string) (bool, error) {
	// `git init -b main` to avoid relying on the user's init.defaultBranch
	// setting. `-q` suppresses the "Initialized empty Git repository" line
	// that would otherwise leak to stdout when callers wire ours through.
	if err := runGitIn(actDir, "init", "-q", "-b", "main"); err != nil {
		return false, fmt.Errorf("git init in %s: %w", actDir, err)
	}

	// Pin commit identity locally so the initial commit doesn't fail on
	// hosts with no global user.{name,email} set (CI containers, fresh
	// installs). We use the same email act already collected for node_id
	// derivation, and a deterministic name so the audit trail attributes
	// the bootstrap to act init.
	commitEmail := gitEmail
	if commitEmail == "" {
		commitEmail = "act@example.invalid"
	}
	if err := runGitIn(actDir, "config", "user.email", commitEmail); err != nil {
		return false, fmt.Errorf("git config user.email: %w", err)
	}
	if err := runGitIn(actDir, "config", "user.name", "act init"); err != nil {
		return false, fmt.Errorf("git config user.name: %w", err)
	}
	// Disable commit signing for the bootstrap commit; the operator can
	// enable it on subsequent op commits if their global config wants it.
	_ = runGitIn(actDir, "config", "commit.gpgsign", "false")

	// Skip the initial commit if the repo already has one (re-init case).
	if hasHEAD(actDir) {
		return false, nil
	}

	if err := runGitIn(actDir, "add", "-A"); err != nil {
		return false, fmt.Errorf("git add -A in %s: %w", actDir, err)
	}
	if err := runGitIn(actDir, "commit", "-q", "--no-verify", "-m", "act init: nested act state bootstrap"); err != nil {
		return false, fmt.Errorf("git commit in %s: %w", actDir, err)
	}
	_ = machineID
	return true, nil
}

// runGitIn runs `git <args>` with cwd=dir. Stderr is captured into the
// returned error on failure so callers see why git refused.
func runGitIn(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// hasHEAD reports whether the git repo rooted at dir has a HEAD ref (i.e.
// at least one commit). Used to skip the initial bootstrap commit on re-
// init, and to skip auto-committing host-side changes when the host has
// no initial commit yet.
func hasHEAD(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "HEAD")
	cmd.Dir = dir
	return cmd.Run() == nil
}

// hostHasHEAD is hasHEAD with an explicit name for the host-repo case.
// Same implementation; the distinct identifier reads more naturally at the
// call site.
func hostHasHEAD(repoRoot string) bool { return hasHEAD(repoRoot) }

// hasGitDir reports whether repoRoot or any of its ancestors contains a
// `.git` entry (file or directory). Walks up to the filesystem root.
func hasGitDir(repoRoot string) bool {
	dir, err := filepath.Abs(repoRoot)
	if err != nil {
		return false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// ensureGitignoreEntry appends `entry` to <repoRoot>/.gitignore if it is not
// already present on its own line. Idempotent.
//
// Edge cases handled per the spec ("gitignore edge cases"):
//
//   - Entry already present (exact line match, trim-space) → no-op,
//     returns (false, nil).
//   - .gitignore missing → file is created with the entry on its own line.
//   - .gitignore exists but the final byte isn't a newline → a newline is
//     added before the entry so we don't accidentally extend the trailing
//     line.
//   - .act/ ignored at a different scope (parent .gitignore, or a different
//     pattern like `**/.act/` or `/.act`) → we still append `.act/` here so
//     the literal-line idempotency check works on re-init. The
//     functional-equivalence ("is .act/ effectively ignored from this
//     directory?") is doctor's job (delta item 5), not init's.
//
// The boolean return is true iff the file was modified.
func ensureGitignoreEntry(repoRoot, entry string) (bool, error) {
	path := filepath.Join(repoRoot, ".gitignore")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("gitignore: read: %w", err)
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == entry {
			return false, nil
		}
	}

	var out strings.Builder
	out.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		out.WriteString("\n")
	}
	out.WriteString(entry)
	out.WriteString("\n")

	if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
		return false, fmt.Errorf("gitignore: write: %w", err)
	}
	return true, nil
}

// installHostPreCommitHook installs (or augments) the host repo's
// pre-commit hook so it hard-rejects staged paths under .act/.
//
// Idempotent: if the file already contains preCommitHookHeader, we leave
// it alone and return (false, nil). If a different pre-commit hook already
// exists, we append the act block to the end so the user's existing
// behavior is preserved. If no hook exists, we create one with a shebang +
// the act block.
//
// The hook is chmod'd 0755 so git actually invokes it; without the execute
// bit git silently skips the hook.
//
// Worktree-aware: in a `git worktree`, the top-level `.git` is a FILE
// containing `gitdir: <path>`, not a directory. We resolve that to the
// real gitdir and install hooks there. Hooks under per-worktree dirs are
// shared across worktrees on most git configurations (the hooks dir
// lives at the main repo's .git/hooks, not the per-worktree dir), which
// matches the host-wide enforcement we want.
func installHostPreCommitHook(repoRoot string) (bool, error) {
	hooksDir, err := resolveGitHooksDir(repoRoot)
	if err != nil {
		return false, fmt.Errorf("pre-commit hook: resolve hooks dir: %w", err)
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return false, fmt.Errorf("pre-commit hook: mkdir hooks dir: %w", err)
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")

	existing, rerr := os.ReadFile(hookPath)
	if rerr != nil && !os.IsNotExist(rerr) {
		return false, fmt.Errorf("pre-commit hook: read: %w", rerr)
	}

	if strings.Contains(string(existing), preCommitHookHeader) {
		// Already installed. Make sure it's executable in case the file was
		// committed without the +x bit somewhere upstream.
		if err := os.Chmod(hookPath, 0o755); err != nil {
			return false, fmt.Errorf("pre-commit hook: chmod: %w", err)
		}
		return false, nil
	}

	var out strings.Builder
	if len(existing) == 0 {
		out.WriteString("#!/usr/bin/env sh\n")
		out.WriteString(preCommitHookBlock)
	} else {
		out.Write(existing)
		if !strings.HasSuffix(string(existing), "\n") {
			out.WriteString("\n")
		}
		out.WriteString("\n")
		out.WriteString(preCommitHookBlock)
	}

	if err := os.WriteFile(hookPath, []byte(out.String()), 0o755); err != nil {
		return false, fmt.Errorf("pre-commit hook: write: %w", err)
	}
	// WriteFile honours the mode for create; ensure +x even when the file
	// already existed (umask, prior chmod) so git actually invokes it.
	if err := os.Chmod(hookPath, 0o755); err != nil {
		return false, fmt.Errorf("pre-commit hook: chmod: %w", err)
	}
	return true, nil
}

// hasPublicLookingRemote queries the host repo for `origin`'s URL and
// returns true iff it matches publicRemoteRegex (github.com / gitlab.com /
// bitbucket.org).
//
// Returns (false, nil) when there is no origin remote configured, when git
// fails for any reason, or when the URL doesn't match. Errors from git are
// surfaced via the second return for callers that want to log them, but
// the boolean is the load-bearing answer.
func hasPublicLookingRemote(repoRoot string) (bool, error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		// No origin → not public-looking; no error.
		return false, nil
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return false, nil
	}
	return publicRemoteRegex.MatchString(url), nil
}

// ensureContributingStanza appends the act commit-marker stanza to
// CONTRIBUTING.md when not already present. The stanza is bracketed by
// HTML comments (contributingStanzaStart / contributingStanzaEnd) so the
// idempotency check is a substring match against the start marker.
//
// If CONTRIBUTING.md exists, the stanza is appended (with a leading blank
// line for separation). If it doesn't exist, a fresh file is created with
// just the stanza. The boolean return is true iff the file was modified.
func ensureContributingStanza(repoRoot string) (bool, error) {
	path := filepath.Join(repoRoot, "CONTRIBUTING.md")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("CONTRIBUTING.md: read: %w", err)
	}
	if strings.Contains(string(existing), contributingStanzaStart) {
		return false, nil
	}

	var out strings.Builder
	if len(existing) > 0 {
		out.Write(existing)
		if !strings.HasSuffix(string(existing), "\n") {
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}
	out.WriteString(contributingStanzaStart)
	out.WriteString("\n\n")
	out.WriteString(contributingStanzaBody)
	if !strings.HasSuffix(contributingStanzaBody, "\n") {
		out.WriteString("\n")
	}
	out.WriteString("\n")
	out.WriteString(contributingStanzaEnd)
	out.WriteString("\n")

	if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
		return false, fmt.Errorf("CONTRIBUTING.md: write: %w", err)
	}
	return true, nil
}

// resolveGitHooksDir returns the absolute path to the host repo's hooks
// directory, handling both the standard-repo case (`<root>/.git/hooks`)
// and the worktree case where `<root>/.git` is a file containing
// `gitdir: <real-gitdir>`. In a worktree, the per-worktree gitdir lives
// under `<main>/.git/worktrees/<name>`; hooks are conventionally shared
// from the main `<main>/.git/hooks` so a hook installed there fires for
// every worktree. We resolve the worktree gitdir, then walk up to the
// main `.git` if applicable.
func resolveGitHooksDir(repoRoot string) (string, error) {
	gitPath := filepath.Join(repoRoot, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return "", fmt.Errorf("stat .git: %w", err)
	}
	if info.IsDir() {
		return filepath.Join(gitPath, "hooks"), nil
	}
	// File: parse the "gitdir: ..." pointer.
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return "", fmt.Errorf("read .git pointer: %w", err)
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf(".git is not a directory and not a gitdir pointer: %q", line)
	}
	worktreeGitDir := strings.TrimSpace(line[len(prefix):])
	// If the pointer leads to a `<main>/.git/worktrees/<name>` path,
	// the conventional hooks dir is `<main>/.git/hooks`. The pointer is
	// absolute; walk up two dirs to reach `<main>/.git`.
	parent := filepath.Dir(worktreeGitDir) // <main>/.git/worktrees
	if filepath.Base(parent) == "worktrees" {
		mainGitDir := filepath.Dir(parent) // <main>/.git
		return filepath.Join(mainGitDir, "hooks"), nil
	}
	// Fallback: drop the per-worktree hooks dir alongside the worktree's
	// gitdir. Less ideal but better than failing.
	return filepath.Join(worktreeGitDir, "hooks"), nil
}

// commitHostChanges stages and commits the host-side artifacts produced by
// step 2 (gitignore + CONTRIBUTING.md, plus an implicit .git/hooks/pre-commit
// which is NOT tracked by git anyway). Never `-A`: we only stage the exact
// paths we wrote, so pre-existing dirty work in the host tree stays out of
// the commit.
//
// The pre-commit hook lives in .git/hooks/ which is not tracked, so it
// doesn't need staging — git will install it on every clone via the next
// init/migration step on that machine, not via this commit.
func commitHostChanges(repoRoot string, gitignoreChanged, contributingChanged bool) error {
	var toStage []string
	if gitignoreChanged {
		toStage = append(toStage, ".gitignore")
	}
	if contributingChanged {
		toStage = append(toStage, "CONTRIBUTING.md")
	}
	if len(toStage) == 0 {
		return nil
	}
	args := append([]string{"add", "--"}, toStage...)
	if err := runGitIn(repoRoot, args...); err != nil {
		return err
	}
	if err := runGitIn(repoRoot, "commit", "-q", "--no-verify",
		"-m", "act init: host gitignore + CONTRIBUTING stanza"); err != nil {
		return err
	}
	return nil
}
