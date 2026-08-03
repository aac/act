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
// The read mode (act-3803ac) short-circuits all of this: opts.NoFetch or
// ACT_NO_FETCH=1 returns before any git command runs, so a read leaves
// the store byte-for-byte untouched and reports how old the on-disk state
// is instead.
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
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/gitops"
)

// ReadCacheTTL is the default freshness window for read-path commands.
// Within this duration of the last FETCH_HEAD touch, read-path commands
// skip the fetch and read on-disk state directly. It is the fallback for
// resolveReadCacheTTL when `act.readCacheTTLSeconds` is unset or invalid;
// it matches config.DefaultEnableDefaults().ReadCacheTTLSeconds (5s).
const ReadCacheTTL = 5 * time.Second

// resolveReadCacheTTL picks the read-cache freshness window for the .act/
// state rooted at actRoot (the `.act/` directory, i.e. Layout(repo).Root).
// Order of precedence:
//
//  1. act.readCacheTTLSeconds from the nested .act/.git/config when the key
//     is readable and parses to a positive integer.
//  2. ReadCacheTTL (the compiled-in default, == DefaultEnableDefaults()).
//
// Best-effort: any error deriving or reading the key falls through to the
// default. A non-positive or unparseable value is ignored (the operator
// gets the default rather than a zero-length window that would defeat the
// cache entirely).
func resolveReadCacheTTL(actRoot string) time.Duration {
	cfgPath := config.ActGitConfigPath(actRoot)
	if v, err := config.GetGitConfig(cfgPath, config.ReadCacheTTLSecondsKey); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return ReadCacheTTL
}

// envDispatchMode is the bypass env var documented in the spec's
// "Read-cache" section. The value is checked for the literal "1" — any
// other value (including "true", "yes", "0", or empty) is treated as
// "not in dispatch mode" so dev-shell variable echoes don't accidentally
// bypass the cache.
const envDispatchMode = "ACT_DISPATCH_MODE"

// envNoFetch is the read-only-mode env var (act-3803ac). Same strict "1"
// check as envDispatchMode, for the same reason: a dev-shell echo of
// ACT_NO_FETCH=true must not silently change what a read command does.
const envNoFetch = "ACT_NO_FETCH"

// MaybeRefreshOptions controls the read-path cache check.
type MaybeRefreshOptions struct {
	// Fresh, when true, forces a fetch regardless of cache freshness.
	// Set by `act ready --fresh` and the `--no-cache` alias.
	Fresh bool
	// NoFetch, when true, makes the read genuinely non-mutating: no
	// fetch, no rebase, no fold-checkpoint or index.db deletion — the
	// command answers from on-disk state (act-3803ac). Set by the
	// `--no-fetch` flag on the read-path commands, or by ACT_NO_FETCH=1
	// in the environment.
	//
	// Why this exists: MaybeRefresh's fetch is `git pull --rebase` inside
	// the store's nested .act/.git, and when HEAD moves it deletes the
	// fold checkpoint and index.db. A sweep across N stores is therefore
	// N rebases into repos an agent fleet may be writing to concurrently
	// — every reader either reimplements a busy-window discipline or
	// accepts the hazard. NoFetch is the way out: an aggregator reads
	// on-disk state and uses Result.Age to judge how stale the answer is.
	//
	// NoFetch wins over Fresh and over ACT_DISPATCH_MODE: a caller that
	// asked for a non-mutating read gets one, and the CLI rejects
	// `--no-fetch --fresh` outright rather than silently picking.
	NoFetch bool
}

// MaybeRefreshResult is the structured return from a cache check.
//
// act-3803ac: read-path callers no longer discard this. They used to
// (`_, _ = MaybeRefresh(...)`), which made a FAILED refresh — unreachable
// bare host, no network, mid-rebase store — look exactly like a
// successful one: the command served whatever was on disk and exited 0
// with nothing to distinguish it. A sweep on a laptop with the bare host
// unreachable returned confidently-wrong answers indistinguishable from
// current ones. Callers now attach a RefreshInfo to their JSON result and
// print a stderr warning on failure.
//
// Age does not close that gap on its own: git touches FETCH_HEAD even on
// a fetch that fails, so a freshly-failed refresh reports an age near
// zero. Error is the signal; Age qualifies it.
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
	// "fresh_flag", "dispatch_mode", "no_remote", "no_act", "cold",
	// "no_fetch".
	Reason string
	// Age is how long ago .act/.git/FETCH_HEAD was last touched, i.e. how
	// stale the on-disk state may be. Set whenever FETCH_HEAD is
	// readable, which is the case that matters: on a NoFetch read it is
	// the "and tell me how old it is" half of the contract, and it saves
	// every aggregator from stat'ing FETCH_HEAD itself. AgeKnown
	// distinguishes "zero seconds old" from "never fetched / no
	// FETCH_HEAD / stat failed".
	Age      time.Duration
	AgeKnown bool
}

// RefreshInfo is the JSON projection of a read's refresh outcome,
// attached to the read commands' result envelopes as `refresh`
// (act-3803ac). It folds MaybeRefresh's (result, error) pair into one
// object so a consumer can tell served-from-cache from freshly-fetched
// from refresh-failed without stat'ing FETCH_HEAD or parsing stderr.
type RefreshInfo struct {
	// Reason mirrors MaybeRefreshResult.Reason.
	Reason string `json:"reason"`
	// Fetched is true when a fetch+rebase actually ran.
	Fetched bool `json:"fetched"`
	// Invalidated is true when that fetch moved HEAD and the fold
	// checkpoint + index.db were dropped. Omitted when false.
	Invalidated bool `json:"invalidated,omitempty"`
	// AgeSeconds is the age of FETCH_HEAD, rounded to whole seconds.
	// Omitted when unknown (never fetched, or stat failed) — a consumer
	// must not read a missing field as "fresh". When Error is also set,
	// read it as "when git last tried": a failed fetch still touches
	// FETCH_HEAD.
	AgeSeconds *int64 `json:"age_seconds,omitempty"`
	// Error is the refresh failure message, when the refresh was
	// attempted and failed. Its presence is the signal that the rows in
	// this response are on-disk state that could not be brought current;
	// the command still exits 0, because stale-but-readable is a usable
	// answer as long as the caller is told.
	Error string `json:"error,omitempty"`
}

// NewRefreshInfo folds a MaybeRefresh (result, error) pair into the JSON
// projection. Returns nil when there is nothing worth reporting — a repo
// with no .act/ at all — so the `refresh` key stays absent on responses
// where it would say nothing.
func NewRefreshInfo(res MaybeRefreshResult, err error) *RefreshInfo {
	if res.Reason == "no_act" && err == nil {
		return nil
	}
	info := &RefreshInfo{
		Reason:      res.Reason,
		Fetched:     res.Fetched,
		Invalidated: res.Invalidated,
	}
	if res.AgeKnown {
		secs := int64(res.Age / time.Second)
		info.AgeSeconds = &secs
	}
	if err != nil {
		info.Error = err.Error()
	}
	return info
}

// FormatRefreshWarning renders the stderr WARNING for a failed refresh,
// or "" when there is nothing to warn about. It goes to stderr in BOTH
// human and --json modes, matching the truncation notice (act-1b816e):
// stderr cannot corrupt the row stream or the JSON document, and a human
// watching a `--json | jq` pipeline has nothing else to look at.
func FormatRefreshWarning(info *RefreshInfo) string {
	if info == nil || info.Error == "" {
		return ""
	}
	age := "no FETCH_HEAD on disk"
	if info.AgeSeconds != nil {
		// Deliberately "FETCH_HEAD is Ns old", not "last fetched Ns ago":
		// git touches FETCH_HEAD even on a fetch that fails, so the
		// timestamp bounds when git last TRIED, not when it last
		// succeeded. Saying "last fetched" here would overstate freshness
		// in exactly the case this warning exists for.
		age = fmt.Sprintf("FETCH_HEAD is %ds old", *info.AgeSeconds)
	}
	return fmt.Sprintf("WARNING: could not refresh .act/ state (%s); answering from on-disk state (%s).\n", info.Error, age)
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

	// act-3803ac: the non-mutating read mode short-circuits BEFORE the
	// remote probe and before anything that could write. Nothing below
	// this point runs, so the store is untouched: no fetch, no rebase,
	// no checkpoint or index deletion.
	if opts.NoFetch || noFetchModeOn() {
		res := MaybeRefreshResult{Reason: "no_fetch"}
		res.Age, res.AgeKnown = fetchHeadAge(paths.Root)
		return res, nil
	}

	gops := gitops.NewActGitOps(paths.Root)

	// No origin remote → local-only path. The cache is effectively
	// always-fresh and there is no fetch to issue.
	if !hasOriginRemote(gops) {
		res := MaybeRefreshResult{Reason: "no_remote"}
		res.Age, res.AgeKnown = fetchHeadAge(paths.Root)
		return res, nil
	}

	bypass := opts.Fresh || dispatchModeOn()

	if !bypass {
		ttl := resolveReadCacheTTL(paths.Root)
		mtime, err := gitops.FetchHeadMtime(paths.Root)
		if err == nil && !mtime.IsZero() && time.Since(mtime) < ttl {
			return MaybeRefreshResult{Reason: "hit", Age: time.Since(mtime), AgeKnown: true}, nil
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
		res := MaybeRefreshResult{Reason: "no_remote"}
		res.Age, res.AgeKnown = fetchHeadAge(paths.Root)
		return res, nil
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
			// Age is still reported: it bounds the staleness of what the
			// caller is about to be served. Note git touches FETCH_HEAD
			// even when the fetch fails, so this is "when git last
			// tried", not "when it last succeeded" — the caller reads it
			// alongside Error, never alone.
			failed := MaybeRefreshResult{Fetched: true, Reason: reason}
			failed.Age, failed.AgeKnown = fetchHeadAge(paths.Root)
			return failed, err
		}
	}

	res := MaybeRefreshResult{Fetched: true, Reason: reason}
	res.Age, res.AgeKnown = fetchHeadAge(paths.Root)

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

// fetchHeadAge reports how long ago the store's FETCH_HEAD was touched.
// The second return is false when there is no FETCH_HEAD (never fetched)
// or the stat failed — cases a caller must not conflate with "fresh".
func fetchHeadAge(actRoot string) (time.Duration, bool) {
	mtime, err := gitops.FetchHeadMtime(actRoot)
	if err != nil || mtime.IsZero() {
		return 0, false
	}
	return time.Since(mtime), true
}

// noFetchModeOn reports whether ACT_NO_FETCH is set to the literal "1".
func noFetchModeOn() bool {
	return os.Getenv(envNoFetch) == "1"
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
