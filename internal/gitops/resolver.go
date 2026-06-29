package gitops

// Phase 1 host-vs-nested repo-root resolution. Under the coordination-plane
// design (docs/coordination-plane-design.md delta item 3) the nearest .git
// walking up from cwd may belong to the nested act repo rather than the host
// project. The resolver distinguishes:
//
//   - the host repo root (the working tree whose .gitignore excludes .act/),
//     identified by walking up looking for a .git whose parent is NOT named
//     .act; nested .act/.git encounters are skipped and the walk continues
//     from the .act directory's parent.
//   - the act state path, which is hostRepoRoot/.act if that directory exists.
//
// Callers wire the returned paths into NewActGitOps(actStatePath) and
// NewHostGitOps(hostRepoRoot) — see gitops.go for the dual-handle types
// (act-3604).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNoHostRepo signals that walking up from the starting directory found no
// surrounding host git repo at all (no .git ever encountered, or every .git
// encountered belonged to a nested .act/.git). Callers translate this to the
// CLI-layer "no act state here — this is normal in CI / fresh clones"
// message (delta item 8, OSS-friendly no-state behavior).
var ErrNoHostRepo = errors.New("gitops: no host git repo found in cwd or any parent")

// ErrNoActState signals that the host repo root has no .act/ directory.
// Common in fresh clones, CI checkouts, and any project that hasn't run
// `act init`. Distinct from ErrNoHostRepo: the host repo exists, it just
// has no act state.
var ErrNoActState = errors.New("gitops: no act state found at host repo root")

// ErrStandaloneActUnsupported signals that act was invoked from inside a
// .act/ directory (or its .git) when no surrounding host repo exists. The
// operator-decided-scope case (Phase 2 design space) where act state lives
// without a paired code repo is not supported in Phase 1.
var ErrStandaloneActUnsupported = errors.New("gitops: standalone act state not supported in Phase 1")

// FindHostRepoRoot walks upward from start looking for the host repo's .git.
// If the .git encountered has a parent directory named ".act", that .git
// belongs to the nested act repo (per the Phase 1 design); the walk skips it
// and continues from the .act directory's grandparent — i.e. the directory
// that *contains* the .act/.
//
// Returns:
//   - ErrStandaloneActUnsupported if the only .git found has a .act parent
//     and there is no further host repo above it (act invoked from inside
//     a free-standing .act/).
//   - ErrNoHostRepo if no .git is ever encountered walking up.
//   - (hostRepoRoot, nil) on success.
//
// The "no host repo" determination only fires after exhausting the walk to
// the filesystem root. A non-existent or unreadable start directory returns
// an error that wraps the underlying os call.
func FindHostRepoRoot(start string) (string, error) {
	if start == "" {
		return "", fmt.Errorf("gitops: FindHostRepoRoot: empty start path")
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("gitops: FindHostRepoRoot: abs(%q): %w", start, err)
	}
	// Confirm the start path exists; otherwise the walk will silently climb
	// past a missing leaf and surface ErrNoHostRepo at the filesystem root,
	// which is a less useful diagnostic than "you passed a path that isn't
	// there."
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("gitops: FindHostRepoRoot: stat(%q): %w", start, err)
	}

	// Track whether we ever saw a nested .act/.git during the walk. If the
	// walk exhausts without finding a host .git, but we did skip a nested
	// .act/.git, the standalone-act case applies; otherwise the cwd simply
	// isn't inside any git repo.
	sawNestedActGit := false

	dir := abs
	for {
		gitPath := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			// Found a .git at dir. Check whether dir is a nested .act/.
			if filepath.Base(dir) == ".act" {
				// This .git belongs to the nested act repo. Skip it: continue
				// the walk from the directory that contains the .act/.
				sawNestedActGit = true
				parent := filepath.Dir(dir)
				if parent == dir {
					// .act/ at filesystem root — pathological but handle.
					return "", ErrStandaloneActUnsupported
				}
				dir = parent
				continue
			}
			// Host .git. Return the directory containing it.
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding a host .git.
			if sawNestedActGit {
				return "", ErrStandaloneActUnsupported
			}
			return "", ErrNoHostRepo
		}
		dir = parent
	}
}

// FindActStatePath returns hostRepoRoot/.act if that directory exists, or
// ErrNoActState if it does not. Symlinks are followed (os.Stat semantics);
// the directory must be a directory, not a regular file.
//
// The act state path is what callers pass to NewActGitOps. Callers that
// need to distinguish "no act state, run `act init`" from "no host repo at
// all" check errors.Is against the two sentinels.
func FindActStatePath(hostRepoRoot string) (string, error) {
	if hostRepoRoot == "" {
		return "", fmt.Errorf("gitops: FindActStatePath: empty host repo root")
	}
	actPath := filepath.Join(hostRepoRoot, ".act")
	info, err := os.Stat(actPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNoActState
		}
		return "", fmt.Errorf("gitops: FindActStatePath: stat(%q): %w", actPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("gitops: FindActStatePath: %q exists but is not a directory", actPath)
	}
	return actPath, nil
}
