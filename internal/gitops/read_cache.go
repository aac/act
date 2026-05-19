package gitops

// Phase 2 ticket 5: read-path TTL cache helpers. These live in their own
// file alongside the auto-push helpers from ticket 3a (gitops.go) so the
// two concerns coexist without colliding edits. The read-path code below
// is consumed by internal/cli/cache.go (the cache-check shared entry
// point); production callers do not invoke these helpers directly except
// for the read-cache integration.

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FetchHeadPath returns the FETCH_HEAD path inside the nested .act/.git
// directory at repoActPath. repoActPath is the `.act/` directory itself
// (the path returned by gitops.FindActStatePath). The returned path is
// not required to exist — callers stat it and treat a missing file as
// "no cache entry yet".
func FetchHeadPath(repoActPath string) string {
	return filepath.Join(repoActPath, ".git", "FETCH_HEAD")
}

// FetchHeadMtime returns the modification time of FETCH_HEAD inside
// repoActPath/.git, or (zero time, nil) when the file does not yet
// exist. Stat errors other than "not found" are surfaced so the caller
// can distinguish a permission failure from a cold cache.
func FetchHeadMtime(repoActPath string) (time.Time, error) {
	info, err := os.Stat(FetchHeadPath(repoActPath))
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// RemoteURL returns the configured URL for the named remote (typically
// "origin"). When the remote is not configured the underlying `git
// remote get-url` exits non-zero and the wrapped error is returned —
// callers that only care "is this remote configured?" check for a
// non-nil error.
func (g *GitOps) RemoteURL(name string) (string, error) {
	out, err := g.run("remote", "get-url", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// HeadSHA returns the current HEAD sha of the nested .act/.git repo.
// Returns ("", nil) when the repo has no commits yet (cold init).
// Errors are returned for any other failure.
//
// The cache layer uses this to detect "did the rebase add new ops?":
// snapshot the SHA before FetchAndRebase, compare after, and invalidate
// the fold-checkpoint when they differ.
func (g *GitOps) HeadSHA() (string, error) {
	out, err := g.run("rev-parse", "HEAD")
	if err != nil {
		// `git rev-parse HEAD` exits non-zero on an empty repo with the
		// message "unknown revision or path". Translate to ("", nil) so
		// the cache layer treats this as cold-start, not an error.
		if strings.Contains(err.Error(), "unknown revision") ||
			strings.Contains(err.Error(), "does not have any commits yet") ||
			strings.Contains(err.Error(), "bad revision") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}
