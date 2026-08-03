// act-650378 — self-healing the untracked-op-file/checkout collision.
//
// THE FAILURE THIS FILES FIXES, verbatim from a real `act close`:
//
//	error: The following untracked working tree files would be overwritten by checkout:
//		ops/act-d5100b/2026-07/2026-07-31T06-17-02.114Z-close.json
//	Please move or remove them before you switch branches.
//	Aborting
//	error: could not detach HEAD
//
// It comes from the `git rebase origin/<branch>` inside FetchAndRebase:
// rebase detaches HEAD onto origin/<branch>, and git refuses to detach
// when checking out that tree would clobber an untracked working-tree
// file. In act's nested `.act/` repo the colliding paths are act's OWN
// op files: an op that exists on disk but is not (yet) in this repo's
// index, at a path origin already tracks. Concurrent writers in a shared
// checkout and state imports both produce that state routinely.
//
// Two things made it bad out of proportion to its cause:
//
//  1. The rebase failure was classified as a rebase CONFLICT, retried to
//     exhaustion, and reported as a failed command — on a write whose op
//     had already been committed. Operators saw failure, retried, and got
//     "Already closed". (The exit-status half of that is fixed in
//     internal/cli/publish.go; act-89a595.)
//  2. Git's remedy — "move or remove them" — asks a human to delete files
//     inside act's own state directory, which an agent's permission
//     classifier is right to refuse. act owns `ops/`; it should resolve
//     this itself.
//
// The resolution below is deliberately conservative:
//
//   - Only paths under `ops/` are touched. Any other colliding path is
//     left alone and the original error is returned unchanged.
//   - Only files git agrees are untracked are touched.
//   - A local file byte-identical to origin's version is deleted: there
//     is nothing to lose, the checkout is about to write the same bytes.
//   - A local file that DIFFERS is moved into `.collisions/<stamp>/` in
//     the state root, never deleted. `.collisions/` is act's own dotdir,
//     so it can never itself collide with a tracked path.
package gitops

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// collisionHeader is the first line of git's refusal. Matching on it
// (rather than on "could not detach HEAD", which is the downstream
// symptom) keys the recovery to the actual cause.
const collisionHeader = "The following untracked working tree files would be overwritten by checkout:"

// collisionQuarantineDir is the state-root-relative directory differing
// op files are moved into. A dotted name keeps it out of the op-log
// namespace and out of any tracked tree.
const collisionQuarantineDir = ".collisions"

// untrackedCheckoutCollisionPaths extracts the repo-relative paths git
// listed as blocking the checkout. Returns nil when the output is not a
// collision refusal.
//
// Git's format is the header line followed by one tab-indented path per
// line, terminated by an unindented line ("Please move or remove...").
func untrackedCheckoutCollisionPaths(out string) []string {
	idx := strings.Index(out, collisionHeader)
	if idx < 0 {
		return nil
	}
	lines := strings.Split(out[idx:], "\n")
	var paths []string
	for _, ln := range lines[1:] {
		if !strings.HasPrefix(ln, "\t") {
			break
		}
		p := strings.TrimSpace(ln)
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// resolveOpFileCollisions clears the colliding untracked op files so the
// rebase can proceed, per the policy in this file's header. It returns
// an error — leaving the caller to surface the original rebase failure —
// when any colliding path is outside `ops/`, is actually tracked, or
// cannot be cleared.
//
// Reports the paths it acted on so the caller can tell the operator what
// act did to their state directory.
func (g *GitOps) resolveOpFileCollisions(branch string, paths []string) (deleted, quarantined []string, stamp string, err error) {
	if len(paths) == 0 {
		return nil, nil, "", fmt.Errorf("gitops: no collision paths to resolve")
	}
	// Fence first, act second: if ANY path is out of act's lane, do
	// nothing at all rather than half-heal and leave the operator with
	// a partially-modified tree plus the same error.
	for _, p := range paths {
		if !isOpLogPath(p) {
			return nil, nil, "", fmt.Errorf("gitops: collision path %q is outside ops/; not act's to resolve", p)
		}
		if g.isTracked(p) {
			return nil, nil, "", fmt.Errorf("gitops: collision path %q is tracked; refusing to touch it", p)
		}
	}

	stamp = time.Now().UTC().Format("2006-01-02T15-04-05.000Z")
	for _, p := range paths {
		local := filepath.Join(g.RepoRoot, filepath.FromSlash(p))
		localBytes, rerr := os.ReadFile(local)
		if rerr != nil {
			// Already gone (the concurrent writer staged it, or another
			// act process healed the same collision). Nothing to do.
			if os.IsNotExist(rerr) {
				continue
			}
			return deleted, quarantined, stamp, fmt.Errorf("gitops: read colliding file %q: %w", p, rerr)
		}
		remoteBytes, serr := g.run("show", "origin/"+branch+":"+p)
		if serr == nil && bytes.Equal(localBytes, []byte(remoteBytes)) {
			// Identical to what the checkout is about to write.
			if err := os.Remove(local); err != nil {
				return deleted, quarantined, stamp, fmt.Errorf("gitops: remove redundant colliding file %q: %w", p, err)
			}
			deleted = append(deleted, p)
			continue
		}
		// Different content (or origin's copy is unreadable): preserve it.
		dst := filepath.Join(g.RepoRoot, collisionQuarantineDir, stamp, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return deleted, quarantined, stamp, fmt.Errorf("gitops: create quarantine dir for %q: %w", p, err)
		}
		if err := os.Rename(local, dst); err != nil {
			return deleted, quarantined, stamp, fmt.Errorf("gitops: quarantine colliding file %q: %w", p, err)
		}
		quarantined = append(quarantined, p)
	}
	return deleted, quarantined, stamp, nil
}

// isOpLogPath reports whether a repo-relative path is inside the op log
// act owns. Rejects absolute paths and any `..` escape.
func isOpLogPath(p string) bool {
	if p == "" || filepath.IsAbs(p) || strings.Contains(p, "..") {
		return false
	}
	return strings.HasPrefix(filepath.ToSlash(p), "ops/")
}

// isTracked reports whether git has the path in its index. Used as a
// safety check: the recovery is only ever allowed to touch untracked
// files, so a tracked path aborts the whole resolution.
func (g *GitOps) isTracked(p string) bool {
	_, err := g.run("ls-files", "--error-unmatch", "--", p)
	return err == nil
}

// warnCollisionResolved tells the operator what act moved. Silence here
// would be its own bug: act deleted or relocated files inside the state
// directory, and a later "where did my op file go" needs an answer that
// is already in the logs.
func warnCollisionResolved(stamp string, deleted, quarantined []string) {
	if len(deleted) > 0 {
		fmt.Fprintf(os.Stderr,
			"act: cleared %d untracked op file(s) already present on origin so the sync could proceed: %s\n",
			len(deleted), strings.Join(deleted, ", "))
	}
	if len(quarantined) > 0 {
		fmt.Fprintf(os.Stderr,
			"act: moved %d untracked op file(s) that differ from origin into %s/%s/ so the sync could proceed (nothing was deleted): %s\n",
			len(quarantined), collisionQuarantineDir, stamp, strings.Join(quarantined, ", "))
	}
}
