// Package cli — `act migrate-to-nested` command for converting an existing
// host-tracked `.act/` installation to the Phase 1 nested-repo layout.
//
// See docs/coordination-plane-design.md (v2.1) Phase 1 delta item 6
// (migration) and docs/migration-runbook.md for the operator-facing story.
//
// Pre-migration shape (legacy, single-repo): `.act/` and its op files are
// tracked by the host repo. Every op produces an `act-op: (act-XXXX)` commit
// in the host log. CONTRIBUTING.md and host pre-commit hook are typically
// absent.
//
// Post-migration shape (nested-repo, Phase 1): `.act/` has its own `.git`
// directory with the existing op files committed as the initial commit.
// The host repo's `.gitignore` ignores `.act/`. The host pre-commit hook
// rejects any accidentally-staged `.act/*` path. CONTRIBUTING.md gets the
// Act-Id trailer stanza when the host has a public-looking remote.
//
// The implementation reuses the c1b4 helpers in init.go: same gitignore
// idempotency rules, same pre-commit hook installer, same CONTRIBUTING
// stanza emitter. The migration-specific steps are (a) the initial nested
// commit message (calls out "migrated from host-tracked .act/" so the
// audit trail is clear) and (b) the `git rm -r --cached .act/` step that
// stops the host repo from tracking op files going forward.
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aac/act/internal/config"
)

// MigrateToNestedOptions are the knobs for `act migrate-to-nested`.
//
// There are no required knobs today; the migration is one-shot and
// non-destructive (the nested-repo bootstrap stages existing op files
// verbatim, the host `git rm --cached` only un-tracks, never deletes).
// AsJSON toggles the JSON envelope; the struct exists so future flags
// (e.g. `--dry-run`) can plug in without breaking the call shape.
type MigrateToNestedOptions struct {
	AsJSON bool
}

// MigrateToNestedResult is the success envelope. Mirrors the field shape
// of successOutput in init.go where it's load-bearing; the migration
// produces the SAME on-disk state as a fresh init plus the host-untrack
// step.
//
//   - AlreadyMigrated is true when the caller invoked migrate on a repo
//     that already has a nested `.act/.git`. The command exits 0 with no
//     other side effects.
//   - NestedCommitted reflects the initial nested-repo commit (always
//     true on a fresh migration; false in the already-migrated case).
//   - HostUntracked is true iff `git rm -r --cached .act/` actually
//     produced index changes (false on a repo whose `.act/` was never
//     tracked — rare but possible if a partial earlier migration left
//     things in a mixed state).
//   - GitignoreUpdated, HookInstalled, ContributingEmitted, HostCommitted
//     mirror init.go's successOutput fields with the same semantics.
//   - PartialFailures lists per-step warnings, same shape as init's.
type MigrateToNestedResult struct {
	OK                  bool     `json:"ok"`
	AlreadyMigrated     bool     `json:"already_migrated,omitempty"`
	ActDir              string   `json:"act_dir"`
	NestedCommitted     bool     `json:"nested_committed"`
	HostUntracked       bool     `json:"host_untracked"`
	GitignoreUpdated    bool     `json:"gitignore_updated"`
	HookInstalled       bool     `json:"hook_installed"`
	ContributingEmitted bool     `json:"contributing_emitted,omitempty"`
	HostCommitted       bool     `json:"host_committed"`
	PartialFailures     []string `json:"partial_failures,omitempty"`
}

// nestedBootstrapMigrationMsg is the initial-commit message for migrated
// repos. Distinct from the fresh-init bootstrap message so a future
// archaeology pass can tell a migrated repo from a born-nested one by
// reading the first commit in `.act/`.
const nestedBootstrapMigrationMsg = "act init: nested act state bootstrap (migrated from host-tracked .act/)"

// hostMigrateCommitMsg is the host-side commit subject. Calls out the
// three host-visible changes: untrack `.act/`, add gitignore + (optional)
// CONTRIBUTING. The pre-commit hook isn't named because it lives under
// `.git/hooks/` which isn't tracked.
const hostMigrateCommitMsg = "act migrate: untrack .act/ from host, set up nested-repo + pre-commit hook + CONTRIBUTING"

// RunMigrateToNested converts a host-tracked `.act/` installation to the
// Phase 1 nested-repo layout. Idempotent: re-running on an already-migrated
// repo (detected via `.act/.git` existing) is a no-op exit 0.
//
// Refuses (exit 3) when `.act/` doesn't exist — the migration target must
// be an act-using repo; bootstrapping a fresh act state is what `act init`
// is for.
//
// Refuses (exit 3) when not invoked inside a git working tree — the host
// must be a real repo for the untrack step to mean anything.
//
// Returns either an errorOutput (mirroring init.go's shape) or a
// MigrateToNestedResult plus an exit code.
func RunMigrateToNested(repoRoot, machineID, gitEmail string, opts MigrateToNestedOptions) (any, int) {
	// Same git-tree gate as init: we walk upward looking for `.git`. The
	// migration changes host-repo state, so a host repo must exist.
	if !hasGitDir(repoRoot) {
		return errorOutput{
			Error:   "not_in_git",
			Message: fmt.Sprintf("act migrate-to-nested: %s is not inside a git working tree", repoRoot),
		}, 3
	}

	paths := config.Layout(repoRoot)

	// `.act/` must exist. We require config.json (the canonical sentinel of
	// a complete init) so a half-broken state doesn't get mistaken for a
	// migratable repo.
	if _, err := os.Stat(paths.ConfigJSON); err != nil {
		return errorOutput{
			Error:   "act_not_initialized",
			Message: fmt.Sprintf("act migrate-to-nested: %s does not exist; run `act init` first", paths.ConfigJSON),
		}, 3
	}

	// Per-step idempotency: a present `.act/.git` means the nested-repo
	// bootstrap already ran. We skip step 1 in that case but STILL run
	// the host-side steps — those are idempotent on their own (gitignore
	// trim-match, hook header marker, git rm --cached --ignore-unmatch)
	// and may need to finish on a partially-migrated repo where the
	// nested bootstrap landed but the host untrack / gitignore / hook
	// didn't. The result envelope sets AlreadyMigrated=true so callers
	// can tell the two paths apart; if every host-side step is also a
	// no-op, the overall run is a fully no-op idempotent re-run.
	nestedAlreadyPresent := false
	if _, err := os.Stat(filepath.Join(paths.Root, ".git")); err == nil {
		nestedAlreadyPresent = true
	}

	// ---------- Step 1: nested .act/ git repo bootstrap ----------
	//
	// Same shape as init.go's bootstrapNestedRepo, but with a migration-
	// specific commit message so the audit trail records the origin.
	// `git init -b main` is idempotent; `git add -A` stages every existing
	// op file (the entire pre-migration history of the act state) into the
	// initial commit. Pre-migration ids stay reachable from the nested repo.
	//
	// Skipped when the nested repo already exists. In that case we
	// proceed straight to host-side reconciliation.
	out := MigrateToNestedResult{
		OK:              true,
		ActDir:          paths.Root,
		AlreadyMigrated: nestedAlreadyPresent,
	}
	if !nestedAlreadyPresent {
		nestedCommitted, nerr := bootstrapMigratedNestedRepo(paths.Root, machineID, gitEmail)
		if nerr != nil {
			return errorOutput{
				Error:   "nested_init_failed",
				Message: nerr.Error(),
			}, 1
		}
		out.NestedCommitted = nestedCommitted
	}

	// ---------- Step 2: host-side effects ----------
	//
	// From here on, errors are partial-failure warnings: the nested repo is
	// durable, so we keep going and surface anything that didn't land.

	// 2a. Untrack `.act/` from the host repo's index (stage-only — does not
	// delete files from disk; the `--cached` flag is the whole point).
	// `git rm -r --cached .act/` exits 0 when the path was tracked and
	// non-zero with "did not match any files" if it wasn't — that's the
	// rare prior-partial-migration case. We treat the no-match case as
	// a non-error: HostUntracked=false but no PartialFailures entry.
	untracked, urerr := untrackHostActDir(repoRoot)
	if urerr != nil {
		out.PartialFailures = append(out.PartialFailures,
			fmt.Sprintf("git rm --cached .act/: %v", urerr))
	} else {
		out.HostUntracked = untracked
	}

	// 2b. Append `.act/` to host `.gitignore` (idempotent).
	if changed, err := ensureGitignoreEntry(repoRoot, gitignoreEntry); err != nil {
		out.PartialFailures = append(out.PartialFailures,
			fmt.Sprintf("gitignore: %v", err))
	} else {
		out.GitignoreUpdated = changed
	}

	// 2c. Install host pre-commit hook.
	if installed, err := installHostPreCommitHook(repoRoot); err != nil {
		out.PartialFailures = append(out.PartialFailures,
			fmt.Sprintf("pre-commit hook: %v", err))
	} else {
		out.HookInstalled = installed
	}

	// 2d. CONTRIBUTING.md stanza (only when public-looking remote).
	if isPublic, _ := hasPublicLookingRemote(repoRoot); isPublic {
		if added, err := ensureContributingStanza(repoRoot); err != nil {
			out.PartialFailures = append(out.PartialFailures,
				fmt.Sprintf("CONTRIBUTING.md: %v", err))
		} else {
			out.ContributingEmitted = added
		}
	}

	// 2e. Commit the host-side changes as a single commit. Same constraint
	// as init: skip if the host has no HEAD yet (a fresh repo without an
	// initial commit doesn't accept a commit without prior `git add`).
	if hostHasHEAD(repoRoot) && (out.HostUntracked || out.GitignoreUpdated || out.ContributingEmitted) {
		if err := commitHostMigrateChanges(repoRoot, out.HostUntracked, out.GitignoreUpdated, out.ContributingEmitted); err != nil {
			out.PartialFailures = append(out.PartialFailures,
				fmt.Sprintf("host commit: %v", err))
		} else {
			out.HostCommitted = true
		}
	}

	return out, 0
}

// bootstrapMigratedNestedRepo is the migration-flavoured twin of
// bootstrapNestedRepo (init.go). Same mechanics — `git init -b main`,
// pin commit identity locally, `git add -A`, initial commit with
// `--no-verify` — different commit message so the audit trail records
// that the initial commit was an import of pre-existing op files.
//
// Returns (true, nil) when a new commit was created, (false, nil) when
// the repo already had commits (re-run after partial earlier success),
// and (false, err) on any failure.
func bootstrapMigratedNestedRepo(actDir, machineID, gitEmail string) (bool, error) {
	if err := runGitIn(actDir, "init", "-q", "-b", "main"); err != nil {
		return false, fmt.Errorf("git init in %s: %w", actDir, err)
	}

	commitEmail := gitEmail
	if commitEmail == "" {
		commitEmail = "act@example.invalid"
	}
	if err := runGitIn(actDir, "config", "user.email", commitEmail); err != nil {
		return false, fmt.Errorf("git config user.email: %w", err)
	}
	if err := runGitIn(actDir, "config", "user.name", "act migrate"); err != nil {
		return false, fmt.Errorf("git config user.name: %w", err)
	}
	_ = runGitIn(actDir, "config", "commit.gpgsign", "false")

	if hasHEAD(actDir) {
		// Repo already has commits — likely a re-run after a partial
		// earlier migration. Leave history alone.
		return false, nil
	}

	if err := runGitIn(actDir, "add", "-A"); err != nil {
		return false, fmt.Errorf("git add -A in %s: %w", actDir, err)
	}
	if err := runGitIn(actDir, "commit", "-q", "--no-verify", "-m", nestedBootstrapMigrationMsg); err != nil {
		return false, fmt.Errorf("git commit in %s: %w", actDir, err)
	}
	_ = machineID
	return true, nil
}

// untrackHostActDir runs `git rm -r --cached .act/` in repoRoot, returning
// (true, nil) when the index was modified, (false, nil) when there was
// nothing tracked to remove (already-untracked case), and (false, err) on
// any other git error.
//
// We detect "nothing tracked" by parsing the stderr message; git exits
// non-zero in that case so distinguishing it from a real error needs a
// substring check. The exact phrasing depends on git version: older git
// says "pathspec '.act/' did not match any files"; newer git is similar.
// We match on the stable substring "did not match".
func untrackHostActDir(repoRoot string) (bool, error) {
	cmd := exec.Command("git", "rm", "-r", "--cached", "--ignore-unmatch", ".act/")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git rm --cached .act/: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	// `--ignore-unmatch` makes git exit 0 when nothing matched. We then
	// detect whether anything actually changed by asking `git diff --cached`.
	staged, derr := hasStagedActChanges(repoRoot)
	if derr != nil {
		// Diff probe failed; assume change happened so we don't suppress a
		// real untrack from the host commit later.
		return true, nil
	}
	return staged, nil
}

// hasStagedActChanges reports whether the host index has any staged change
// affecting paths under `.act/`. Used to disambiguate "git rm --cached
// actually removed something" from "no-op rm".
func hasStagedActChanges(repoRoot string) (bool, error) {
	cmd := exec.Command("git", "diff", "--cached", "--name-only", "--", ".act", ".act/")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

// commitHostMigrateChanges stages and commits the host-side artifacts
// produced by step 2 of the migration. The pre-commit hook lives in
// `.git/hooks/` which isn't tracked, so it isn't staged here.
//
// Unlike init's commitHostChanges, this also accounts for the `git rm
// --cached` already having staged its own set of removals; we just need
// to add the .gitignore / CONTRIBUTING.md changes alongside.
func commitHostMigrateChanges(repoRoot string, untracked, gitignoreChanged, contributingChanged bool) error {
	// Stage the writable paths. The untrack step already left its removals
	// staged via `git rm --cached`, so we don't re-stage `.act/*`.
	if gitignoreChanged {
		if err := runGitIn(repoRoot, "add", "--", ".gitignore"); err != nil {
			return err
		}
	}
	if contributingChanged {
		if err := runGitIn(repoRoot, "add", "--", "CONTRIBUTING.md"); err != nil {
			return err
		}
	}
	// If nothing actually staged anything, skip the commit silently.
	if !untracked && !gitignoreChanged && !contributingChanged {
		return nil
	}
	// Guard: a `git diff --cached --quiet` exits 0 when there's nothing
	// staged. If somehow nothing landed (e.g. all helpers reported true
	// but git considers them no-op), bail rather than producing an empty
	// commit error.
	if err := runGitIn(repoRoot, "diff", "--cached", "--quiet"); err == nil {
		// Nothing staged — nothing to commit. Treat as success.
		return nil
	}
	if err := runGitIn(repoRoot, "commit", "-q", "--no-verify", "-m", hostMigrateCommitMsg); err != nil {
		return err
	}
	return nil
}
