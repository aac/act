package cli

// Phase 2 ticket 5: read-path TTL cache with bypass overrides.
//
// Read-path commands (`act show`, `ready`, `log`, `list`, `search`) check
// `.act/.git/FETCH_HEAD` mtime. Within the TTL window the cached on-disk
// state is read directly; on miss the cache layer runs
// gitops.FetchAndRebase(branch) and, if the rebase added new ops to HEAD,
// invalidates the fold-checkpoint and index.db so the next read produces
// a fresh fold.
//
// Bypass mechanisms (any of which force a fetch):
//
//   - `ACT_DISPATCH_MODE=1` env var — set by the dispatcher when it
//     spawns a coordinated agent; the agent's first read must observe
//     the dispatcher's latest pushes.
//   - opts.Fresh = true — set by `act ready --fresh` (and the `--no-cache`
//     alias) for ad-hoc cache-busting from a human or skill.
//
// The two bypass paths are dispatch-identical: both set the fetch trigger
// to true regardless of mtime. The `--fresh` / `--no-cache` aliasing is
// verified by TestDocClaim_ReadCache_FreshNoCacheAlias.
//
// No-remote repos (no `origin` configured, or no nested .act/.git yet)
// are a silent no-op: there is nothing to fetch from, so the cache is
// effectively always-fresh.

import (
	"errors"
	"os"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/gitops"
)

// ReadCacheTTL is the freshness window for read-path commands. Within
// this duration of the last FETCH_HEAD touch, read-path commands skip
// the fetch and read on-disk state directly.
//
// TODO(act-72d20e / ticket 1a): once the `act.readCacheTTLSeconds` config
// key lands in internal/config, replace this constant with a config-
// driven lookup so operators can tune the window per-repo. Until then
// the 5-second default matches the ticket spec verbatim.
const ReadCacheTTL = 5 * time.Second

// envDispatchMode is the bypass env var documented in the spec's
// "Read-cache" section. The value is checked for the literal "1" — any
// other value (including "true", "yes", "0", or empty) is treated as
// "not in dispatch mode" so dev-shell variable echoes don't accidentally
// bypass the cache.
const envDispatchMode = "ACT_DISPATCH_MODE"

// MaybeRefreshOptions controls the read-path cache check.
type MaybeRefreshOptions struct {
	// Fresh, when true, forces a fetch regardless of cache freshness.
	// Set by `act ready --fresh` and the `--no-cache` alias.
	Fresh bool
}

// MaybeRefreshResult is the structured return from a cache check. Today
// callers ignore the result and treat MaybeRefresh as fire-and-forget,
// but the shape exists so future telemetry / doctor surfaces can read
// what the cache layer did without re-deriving from logs.
type MaybeRefreshResult struct {
	// Fetched is true when the cache layer ran FetchAndRebase. False on
	// a cache hit, on missing-remote / missing-nested-git, or when the
	// repo has no .act/ at all.
	Fetched bool
	// Invalidated is true when the post-rebase HEAD differed from the
	// pre-rebase HEAD, triggering fold-checkpoint + index.db deletion.
	// Implies Fetched.
	Invalidated bool
	// Reason is a short stable slug for the path taken: "hit", "stale",
	// "fresh_flag", "dispatch_mode", "no_remote", "no_act", "cold".
	Reason string
}

// MaybeRefresh is the single entry point read-path commands call before
// reading state. It is safe to invoke unconditionally — a missing .act/,
// missing nested .git, or missing remote all turn into silent no-ops so
// the cache layer never blocks a command that wouldn't have fetched
// anyway.
//
// Error handling: a failure inside FetchAndRebase (network, conflict,
// shallow) is surfaced to the caller. A failure deriving paths or
// reading mtime is treated as "play it safe and don't fetch" — the
// underlying read command will produce the same answer it would have
// before this layer existed.
func MaybeRefresh(repoRoot string, opts MaybeRefreshOptions) (MaybeRefreshResult, error) {
	paths := config.Layout(repoRoot)

	// Missing .act/ → nothing to refresh. Downstream commands surface
	// the no-state error themselves.
	if _, err := os.Stat(paths.Root); err != nil {
		return MaybeRefreshResult{Reason: "no_act"}, nil
	}

	gops := gitops.NewActGitOps(paths.Root)

	// No origin remote → local-only path. The cache is effectively
	// always-fresh and there is no fetch to issue.
	if !hasOriginRemote(gops) {
		return MaybeRefreshResult{Reason: "no_remote"}, nil
	}

	bypass := opts.Fresh || dispatchModeOn()

	if !bypass {
		mtime, err := gitops.FetchHeadMtime(paths.Root)
		if err == nil && !mtime.IsZero() && time.Since(mtime) < ReadCacheTTL {
			return MaybeRefreshResult{Reason: "hit"}, nil
		}
		// Either no FETCH_HEAD yet (cold), or stale, or stat failed —
		// fall through to the fetch in all three cases. A stat error is
		// rare; the conservative move is to fetch rather than serve
		// possibly-stale state.
	}

	branch, err := gops.CurrentBranch()
	if err != nil {
		// Can't determine branch (detached HEAD, fresh repo with no
		// commits). Skip the fetch — downstream commands still operate
		// on whatever is on disk.
		return MaybeRefreshResult{Reason: "no_remote"}, nil
	}

	preHead, _ := gops.HeadSHA()

	reason := "stale"
	if opts.Fresh {
		reason = "fresh_flag"
	} else if dispatchModeOn() {
		reason = "dispatch_mode"
	}

	if err := gops.FetchAndRebase(branch); err != nil {
		// ErrShallowRecovered is the "rebase succeeded after --unshallow"
		// sentinel — from the caller's perspective the fetch worked.
		if !errors.Is(err, gitops.ErrShallowRecovered) {
			return MaybeRefreshResult{Fetched: true, Reason: reason}, err
		}
	}

	res := MaybeRefreshResult{Fetched: true, Reason: reason}

	// Did HEAD move? If so, invalidate fold-checkpoint and index.db so
	// the next read produces a fresh fold over the new ops. Mirrors the
	// pattern in RunHarvest, which calls index.Rebuild after copy+commit
	// (the rebuild implicitly invalidates the index by overwriting it);
	// here we delete the on-disk artifacts and let the next read-path
	// command's existing open+rebuild flow rebuild them from scratch.
	postHead, _ := gops.HeadSHA()
	if preHead != postHead {
		_ = fold.InvalidateCheckpoint(paths.FoldCheckpoint)
		_ = os.Remove(paths.IndexDB)
		res.Invalidated = true
	}

	return res, nil
}

// dispatchModeOn reports whether the ACT_DISPATCH_MODE env var is set to
// the literal "1". Any other value (including "true", "yes", or empty)
// is treated as off; the strict check avoids accidental bypass from
// dev-shell variable inheritance.
func dispatchModeOn() bool {
	return os.Getenv(envDispatchMode) == "1"
}

// hasOriginRemote checks whether the supplied gitops handle has an
// origin remote configured. Wraps the un-exported gitops method behind
// the only public probe (`git remote` succeeding when origin is set).
//
// We can't call gops.hasOriginRemote directly (unexported across the
// package boundary), but `git fetch` returning ErrNoRemote from a
// dry-attempt is too expensive — instead we run a cheap `git remote
// get-url origin` and look for success.
func hasOriginRemote(gops *gitops.GitOps) bool {
	_, err := gops.RemoteURL("origin")
	return err == nil
}
