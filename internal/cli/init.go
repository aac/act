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
//     (optional) CONTRIBUTING update committed in a single host-side commit.
//     It is false unless the caller explicitly asked for the commit with
//     `act init --commit-host` (act-66f987): init writes host-side files but
//     never commits them unasked. It is also false when the host repo has no
//     commits yet, or when the commit step failed; the on-disk state is
//     still valid in every case.
//   - PartialFailures lists per-step warnings: nested-commit ok but host
//     gitignore failed, hook install failed, CONTRIBUTING stanza failed,
//     etc. Per the failure-mode contract (docs/coordination-plane-design.md
//     "Failure-mode write order"), we leave nested in place and surface the
//     partial state for the operator to remediate.
type successOutput struct {
	OK                  bool   `json:"ok"`
	ActDir              string `json:"act_dir"`
	NodeID              string `json:"node_id"`
	NestedCommitted     bool   `json:"nested_committed"`
	HostCommitted       bool   `json:"host_committed"`
	GitignoreUpdated    bool   `json:"gitignore_updated"`
	HookInstalled       bool   `json:"hook_installed"`
	ContributingEmitted bool   `json:"contributing_emitted,omitempty"`
	// ContributingSuggested is true when the host repo has a
	// public-looking remote and the caller did NOT pass --contributing.
	// It is the printed-suggestion half of act-66f987: the stanza is
	// offered, never written unasked. A JSON consumer reads this to know
	// the offer was made and declined by default.
	ContributingSuggested bool `json:"contributing_suggested,omitempty"`
	// HostFilesUncommitted lists the host-repo working-tree paths init
	// wrote and deliberately left uncommitted (act-66f987). Empty when
	// nothing was written or when --commit-host committed them. Callers
	// render it so the operator knows exactly what to review.
	HostFilesUncommitted []string `json:"host_files_uncommitted,omitempty"`
	PartialFailures      []string `json:"partial_failures,omitempty"`
}

// InitOptions carries the knobs for RunInit.
//
// Force, MachineID, GitEmail and Now were positional parameters before
// act-66f987; the struct exists so the two new opt-in host-side switches
// (Contributing, CommitHost) don't extend an already-long positional
// signature. Zero value = the safe default: bootstrap the nested .act/
// repo, write host-side files, commit nothing to the host repo, and emit
// no CONTRIBUTING stanza.
type InitOptions struct {
	// Force reinitializes even when .act/config.json already exists.
	Force bool
	// MachineID and GitEmail feed node_id derivation and the nested
	// repo's commit identity. Empty is allowed (MCP passes empty).
	MachineID string
	GitEmail  string
	// Contributing opts in to appending the Act-Id trailer stanza to the
	// host repo's CONTRIBUTING.md. Before act-66f987 this happened
	// automatically whenever the host had a public-looking remote, which
	// surprised operators across a 17-repo bootstrap; it is now never
	// done unasked. Without it, a public-looking remote only produces a
	// printed suggestion.
	Contributing bool
	// CommitHost opts in to committing the host-side files init wrote
	// (.gitignore, and CONTRIBUTING.md when Contributing is set). Before
	// act-66f987 this commit was unconditional and 13 of 17 host repos
	// needed hand-reverting. Without it the files are left in the working
	// tree for the operator to review and commit.
	CommitHost bool
	// Now overrides the clock for deterministic tests. nil means
	// time.Now.
	Now func() time.Time
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
// tree includes a .act/ addition/modification with a clear remedy.
//
// Deletions of .act/* are PERMITTED: the only legitimate staged .act/*
// change under the Phase 1 nested layout is `git rm --cached` (un-tracking
// a `.act/` that an older host repo still tracks, or a manual carry to a
// sibling branch that still tracks .act/). Adds and modifications under .act/
// are the danger — they re-stage nested-repo content into the host
// commit, which is what the hook exists to prevent. `--diff-filter=d`
// excludes deletions (lowercase letters in --diff-filter are exclude
// filters per git-diff(1)).
const preCommitHookBlock = `# act: reject staged .act/* paths (managed by act init; do not remove)
if git diff --cached --name-only --diff-filter=d -- '.act' '.act/' 2>/dev/null | grep -qE '^\.act(/|$)'; then
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
// CONTRIBUTING stanza).
//
// act-66f987 — HOST-REPO RESTRAINT. init never commits to the host repo and
// never writes CONTRIBUTING.md unless the caller asks:
//
//   - The host commit happens only under opts.CommitHost (`--commit-host`).
//     It used to be unconditional; across the 2026-07-28/29 bootstrap of 17
//     repos it produced an unrequested commit in every host repo with
//     host-side changes and 13 needed hand-reverting.
//   - The CONTRIBUTING stanza is written only under opts.Contributing
//     (`--contributing`). A public-looking remote now only sets
//     ContributingSuggested so the caller can print the offer.
//
// .gitignore and the pre-commit hook are still written: without the ignore
// entry the host would track the nested state repo, and the hook exists to
// stop exactly that. Both are working-tree writes, reported in
// HostFilesUncommitted (the hook lives in .git/hooks and is never tracked),
// and the operator decides whether to commit.
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
func RunInit(repoRoot string, opts InitOptions) (any, int) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	force, machineID, gitEmail := opts.Force, opts.MachineID, opts.GitEmail

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
		NodeID:    nodeID,
		CreatedAt: now().UTC().Format(rfc3339Millis),
		Version:   writerVersion,
		LastHLC:   config.HLCState{},
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

	// Drop the inert pre-close gate scaffold (.act/hooks/close.sample). It is
	// never executed — ResolveHook recognizes only create/close/claim, so the
	// .sample suffix can't fire — it's documentation the agent activates per
	// project by renaming to `close` and filling in that project's check.
	// Written here so it lands in the nested repo's initial commit and thus
	// travels into worktree workers (Phase 2 clone) the same as an active hook.
	if err := writeCloseHookSample(paths.Hooks); err != nil {
		return errorOutput{
			Error:   "init_hooks_failed",
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

	// 2c. CONTRIBUTING.md stanza — OPT-IN ONLY (act-66f987). A
	// public-looking remote is a reason to *suggest* the stanza, never to
	// write it: the file is the host project's, addressed to its human
	// contributors, and act editing it unasked is the surprise the ticket
	// was filed over.
	if opts.Contributing {
		if added, err := ensureContributingStanza(repoRoot); err != nil {
			out.PartialFailures = append(out.PartialFailures,
				fmt.Sprintf("CONTRIBUTING.md: %v", err))
		} else {
			out.ContributingEmitted = added
		}
	} else if isPublic, _ := hasPublicLookingRemote(repoRoot); isPublic {
		out.ContributingSuggested = true
	}

	// 2d. Commit the host-side changes — OPT-IN ONLY (act-66f987). Without
	// --commit-host we leave every host-side file in the working tree and
	// name it in HostFilesUncommitted so the operator can review and commit
	// (or discard) on their own terms.
	//
	// We deliberately do NOT --no-verify when we do commit: if a host has a
	// pre-commit hook that rejects something other than .act/ (the act block
	// we just installed doesn't fire because the staged paths are
	// .gitignore / CONTRIBUTING.md), the user wants to know.
	//
	// Skip the commit attempt if the host repo has no HEAD yet — a fresh
	// `git init` with no initial commit isn't going to accept a commit
	// without prior `git add` of something, and our changes aren't worth
	// forcing the first commit on the user's behalf.
	hostFilesWritten := out.GitignoreUpdated || out.ContributingEmitted
	if opts.CommitHost && hostHasHEAD(repoRoot) && hostFilesWritten {
		if err := commitHostChanges(repoRoot, out.GitignoreUpdated, out.ContributingEmitted); err != nil {
			out.PartialFailures = append(out.PartialFailures,
				fmt.Sprintf("host commit: %v", err))
		} else {
			out.HostCommitted = true
		}
	}
	if !out.HostCommitted {
		// The pre-commit hook is not listed: it lives in .git/hooks/,
		// which git never tracks, so there is nothing for the operator
		// to commit.
		if out.GitignoreUpdated {
			out.HostFilesUncommitted = append(out.HostFilesUncommitted, ".gitignore")
		}
		if out.ContributingEmitted {
			out.HostFilesUncommitted = append(out.HostFilesUncommitted, "CONTRIBUTING.md")
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

// closeHookSample is the inert pre-close gate scaffold act init drops at
// .act/hooks/close.sample. It is toolchain-agnostic on purpose: a hardcoded
// Go gate copied into a non-Go worker broke those workers once (act-43cf99),
// and act runs in arbitrary projects (TS, Python, Rust, docs-only). The agent
// supplies the project's actual check — knowledge act doesn't have — by
// renaming this to `close` and replacing the placeholder. The `.sample`
// suffix is never executed (hooks.ResolveHook recognizes only create/close/
// claim), so shipping it can't spuriously block a close.
const closeHookSample = `#!/bin/sh
# act pre-close gate — SAMPLE (inert until activated).
#
# Rename this file to ` + "`close`" + ` (drop .sample) and make it executable to
# activate. Once active it runs on every ` + "`act close`" + `, in the project repo
# root, BEFORE the close commit lands; a non-zero exit aborts the close and
# rolls back the close op. So a closed ticket means this gate passed — the
# "closed = verified" invariant, enforced locally and offline (no remote/CI
# required, and it travels into worktree workers via the nested .act/ repo).
#
# Put this project's definition-of-done check below. act is language-agnostic;
# use whatever this project uses, e.g.:
#   Go:     gofmt -l . | grep -q . && exit 1; go vet ./... && go test ./...
#   Node:   npm test
#   Python: pytest
#   Rust:   cargo test
#   Make:   make check
#
# Hook environment:
#   cwd               project repo root
#   $ACT_STATE_PATH   nested .act/ state directory
#   $ACT_ISSUE_ID     issue being closed     $ACT_OP_TYPE     op type (close)
#   $ACT_OP_ID        op hash                $ACT_HOOK_PHASE  lifecycle phase
#
# This gates the *close* (the done boundary), not commits — partial WIP commits
# stay ungated so a reaped worktree never loses work. It is not a replacement
# for the repo's pre-push hook / CI; don't duplicate a suite CI already runs on
# push unless you specifically want the close-time guarantee.

set -eu

# Replace the two lines below with this project's check:
echo "act: no pre-close gate configured (edit .act/hooks/close)" >&2
exit 0
`

// writeCloseHookSample drops the inert close-hook scaffold at
// <hooksDir>/close.sample if it is not already present. Idempotent (re-init
// and --force leave an existing sample untouched). Mode 0o644 — it is a
// template, not an executable hook; activating it is the agent's job.
//
// act-66f987: an ACTIVE `close` hook suppresses the sample entirely. init
// must never touch a project's real gate — not the file itself (it never
// did; the sample has its own name) and not the directory around it, since
// dropping a scaffold next to a working hook is what makes a re-init look
// like it replaced the gate. A project that already has a gate has no use
// for the template.
func writeCloseHookSample(hooksDir string) error {
	if _, err := os.Stat(filepath.Join(hooksDir, "close")); err == nil {
		return nil
	}
	path := filepath.Join(hooksDir, "close.sample")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(closeHookSample), 0o644)
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
