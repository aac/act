package cli

// Doc-claim tests for act-3803ac: the read-path commands get a genuinely
// non-mutating read mode, and they stop discarding the refresh outcome.
//
// The claim being asserted is the strong one, because the weak version is
// what the ticket was filed against: it is not enough that `--no-fetch`
// "skips a fetch". Under --no-fetch the store must come out
// byte-for-byte unchanged — no fetch, no rebase, no FETCH_HEAD write, no
// fold-checkpoint deletion, no index.db deletion, no HEAD movement — so a
// sweep across N stores is safe to run against repos an agent fleet is
// concurrently writing to. TestDocClaim_NoFetch_DoesNotTouchStore asserts
// exactly that against a store where the un-flagged read demonstrably
// DOES mutate (the control half of the test).
//
// The second claim: a refresh outcome is reported rather than discarded,
// so served-from-cache, freshly-fetched and could-not-refresh stop being
// indistinguishable.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/index"
)

// storeFingerprint captures everything the refresh path is capable of
// mutating inside a store: the nested repo's HEAD, FETCH_HEAD's mtime,
// and the presence of the two artifacts a HEAD-moving rebase deletes.
type storeFingerprint struct {
	head          string
	fetchHeadTime time.Time
	checkpoint    bool
	indexDB       bool
}

func fingerprintStore(t *testing.T, repoRoot string) storeFingerprint {
	t.Helper()
	paths := config.Layout(repoRoot)
	fp := storeFingerprint{}
	fp.head = strings.TrimSpace(mustGitOutput(t, paths.Root, "rev-parse", "HEAD"))
	if mt, err := gitops.FetchHeadMtime(paths.Root); err == nil {
		fp.fetchHeadTime = mt
	}
	if _, err := os.Stat(paths.FoldCheckpoint); err == nil {
		fp.checkpoint = true
	}
	if _, err := os.Stat(paths.IndexDB); err == nil {
		fp.indexDB = true
	}
	return fp
}

// seedMutableStore builds a store that a normal read WOULD mutate: a
// fold checkpoint and index.db on disk, a stale FETCH_HEAD so the TTL
// gate misses, and a remote that has moved ahead so the rebase advances
// HEAD.
func seedMutableStore(t *testing.T) string {
	t.Helper()
	root, remote := makeRepoWithRemoteOrigin(t)
	paths := config.Layout(root)

	cp := &fold.Checkpoint{TreeHash: "dummy", Issues: map[string]fold.IssueCheckpoint{}}
	if err := fold.WriteCheckpoint(paths.FoldCheckpoint, cp); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
	// The index image only has to EXIST for the fingerprint (the refresh
	// path deletes the file, it never reads it). Build a real one so the
	// same fixture is usable by the command-level test below, which does
	// open it.
	if idx, err := index.Open(paths.IndexDB); err != nil {
		t.Fatalf("seed index.db: %v", err)
	} else {
		_ = idx.Close()
	}
	remote.AdvanceCommits(1)
	touchFetchHead(t, root, time.Now().Add(-60*time.Second))
	return root
}

// TestDocClaim_NoFetch_DoesNotTouchStore is the core act-3803ac claim.
//
// Two identical stores, both staged so an ordinary read mutates them.
// The control store gets a default read and is expected to move; the
// subject store gets --no-fetch and must come out unchanged on every
// axis the refresh path can touch.
func TestDocClaim_NoFetch_DoesNotTouchStore(t *testing.T) {
	// --- control: the un-flagged read really does mutate the store ---
	control := seedMutableStore(t)
	controlBefore := fingerprintStore(t, control)

	if _, err := MaybeRefresh(control, MaybeRefreshOptions{}); err != nil {
		t.Fatalf("control MaybeRefresh: %v", err)
	}
	controlAfter := fingerprintStore(t, control)
	if controlAfter.head == controlBefore.head {
		t.Fatalf("control store's HEAD did not move; the fixture no longer demonstrates the hazard --no-fetch exists to avoid")
	}
	if controlAfter.checkpoint {
		t.Fatalf("control store kept its fold checkpoint; fixture no longer demonstrates invalidation")
	}
	if controlAfter.indexDB {
		t.Fatalf("control store kept its index.db; fixture no longer demonstrates invalidation")
	}

	// --- subject: --no-fetch leaves the same setup untouched ---
	subject := seedMutableStore(t)
	before := fingerprintStore(t, subject)

	res, err := MaybeRefresh(subject, MaybeRefreshOptions{NoFetch: true})
	if err != nil {
		t.Fatalf("NoFetch MaybeRefresh returned an error: %v", err)
	}
	if res.Fetched || res.Invalidated {
		t.Errorf("NoFetch result reports work done: %+v", res)
	}
	if res.Reason != "no_fetch" {
		t.Errorf("Reason = %q, want %q", res.Reason, "no_fetch")
	}
	if !res.AgeKnown {
		t.Errorf("AgeKnown = false; a --no-fetch read must report how stale the on-disk state is")
	}

	after := fingerprintStore(t, subject)
	if after.head != before.head {
		t.Errorf("nested HEAD moved under --no-fetch: %s -> %s", before.head, after.head)
	}
	if !after.fetchHeadTime.Equal(before.fetchHeadTime) {
		t.Errorf("FETCH_HEAD mtime moved under --no-fetch: %v -> %v", before.fetchHeadTime, after.fetchHeadTime)
	}
	if !after.checkpoint {
		t.Errorf("fold checkpoint deleted under --no-fetch")
	}
	if !after.indexDB {
		t.Errorf("index.db deleted under --no-fetch")
	}
}

// TestDocClaim_NoFetch_EnvVarEquivalent asserts ACT_NO_FETCH=1 has the
// same effect as the flag, which is what makes the mode reachable from a
// sweep that shells out to `act` without threading a flag through every
// call site.
func TestDocClaim_NoFetch_EnvVarEquivalent(t *testing.T) {
	root := seedMutableStore(t)
	before := fingerprintStore(t, root)

	t.Setenv(envNoFetch, "1")
	res, err := MaybeRefresh(root, MaybeRefreshOptions{})
	if err != nil {
		t.Fatalf("MaybeRefresh under ACT_NO_FETCH=1: %v", err)
	}
	if res.Reason != "no_fetch" || res.Fetched {
		t.Fatalf("ACT_NO_FETCH=1 did not take the non-mutating path: %+v", res)
	}
	if after := fingerprintStore(t, root); after.head != before.head || !after.checkpoint {
		t.Errorf("store mutated under ACT_NO_FETCH=1: before=%+v after=%+v", before, after)
	}

	// Strict "1" check, same as ACT_DISPATCH_MODE: a shell echoing
	// ACT_NO_FETCH=true must not silently change read behavior.
	t.Setenv(envNoFetch, "true")
	res, err = MaybeRefresh(root, MaybeRefreshOptions{})
	if err != nil {
		t.Fatalf("MaybeRefresh under ACT_NO_FETCH=true: %v", err)
	}
	if res.Reason == "no_fetch" {
		t.Errorf("ACT_NO_FETCH=true was treated as on; only the literal \"1\" should enable the mode")
	}
}

// TestReadCache_NoFetchBeatsFreshAndDispatchMode pins the precedence the
// option docs state: a caller that asked for a non-mutating read gets
// one, even in a dispatch-mode environment.
func TestReadCache_NoFetchBeatsFreshAndDispatchMode(t *testing.T) {
	root := seedMutableStore(t)
	before := fingerprintStore(t, root)

	t.Setenv(envDispatchMode, "1")
	res, err := MaybeRefresh(root, MaybeRefreshOptions{Fresh: true, NoFetch: true})
	if err != nil {
		t.Fatalf("MaybeRefresh: %v", err)
	}
	if res.Reason != "no_fetch" || res.Fetched {
		t.Fatalf("NoFetch lost to Fresh/dispatch mode: %+v", res)
	}
	if after := fingerprintStore(t, root); after.head != before.head {
		t.Errorf("store mutated: HEAD %s -> %s", before.head, after.head)
	}
}

// TestDocClaim_ReadCommands_SurfaceRefreshOutcome asserts the second half
// of act-3803ac at the command boundary: the read commands attach a
// refresh report to their result instead of discarding it, and a
// --no-fetch read says so along with the age of the state it served.
func TestDocClaim_ReadCommands_SurfaceRefreshOutcome(t *testing.T) {
	root := seedMutableStore(t)

	out, code := RunReady(root, ReadyOptions{NoFetch: true})
	if code != 0 {
		t.Fatalf("RunReady: code=%d out=%+v", code, out)
	}
	res, ok := out.(ReadyResult)
	if !ok {
		t.Fatalf("output type = %T, want ReadyResult", out)
	}
	if res.Refresh == nil {
		t.Fatalf("ReadyResult.Refresh is nil; the refresh outcome is being discarded again")
	}
	if res.Refresh.Reason != "no_fetch" {
		t.Errorf("refresh.reason = %q, want no_fetch", res.Refresh.Reason)
	}
	if res.Refresh.Fetched {
		t.Errorf("refresh.fetched = true under --no-fetch")
	}
	if res.Refresh.AgeSeconds == nil {
		t.Errorf("refresh.age_seconds is absent; a --no-fetch caller cannot judge staleness without it")
	} else if *res.Refresh.AgeSeconds < 0 {
		t.Errorf("refresh.age_seconds = %d, want >= 0", *res.Refresh.AgeSeconds)
	}

	// The same wiring on the other four read commands.
	lout, code := RunList(root, ListOptions{NoFetch: true})
	if code != 0 {
		t.Fatalf("RunList: code=%d out=%+v", code, lout)
	}
	if lr, ok := lout.(ListResult); !ok || lr.Refresh == nil || lr.Refresh.Reason != "no_fetch" {
		t.Errorf("ListResult refresh = %+v, want reason=no_fetch", lout)
	}
	sout, code := RunSearch(root, "anything", SearchOptions{NoFetch: true})
	if code != 0 {
		t.Fatalf("RunSearch: code=%d out=%+v", code, sout)
	}
	if sr, ok := sout.(SearchResult); !ok || sr.Refresh == nil || sr.Refresh.Reason != "no_fetch" {
		t.Errorf("SearchResult refresh = %+v, want reason=no_fetch", sout)
	}
}

// TestDocClaim_RefreshFailure_IsReportedNotSilent is the "the discarded
// error is arguably the worse half" case: when the refresh genuinely
// fails, the command still answers from on-disk state and still exits 0
// — but the failure is now visible in the `refresh` report and in the
// stderr warning, instead of being indistinguishable from success.
func TestDocClaim_RefreshFailure_IsReportedNotSilent(t *testing.T) {
	root, _ := makeRepoWithRemoteOrigin(t)
	paths := config.Layout(root)

	// Point origin at a path that does not exist: the fetch fails, and
	// (the load-bearing detail) writes no FETCH_HEAD, so the TTL never
	// warms and every subsequent read re-fails the same way.
	mustGit(t, paths.Root, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "does-not-exist.git"))
	touchFetchHead(t, root, time.Now().Add(-60*time.Second))

	res, err := MaybeRefresh(root, MaybeRefreshOptions{})
	if err == nil {
		t.Fatalf("MaybeRefresh against an unreachable remote returned no error: %+v", res)
	}
	info := NewRefreshInfo(res, err)
	if info == nil || info.Error == "" {
		t.Fatalf("RefreshInfo does not carry the failure: %+v", info)
	}
	warning := FormatRefreshWarning(info)
	if !strings.Contains(warning, "WARNING") || !strings.Contains(warning, "on-disk state") {
		t.Errorf("FormatRefreshWarning = %q; want a WARNING naming that on-disk state was served", warning)
	}

	// And the command itself still succeeds — stale-but-readable is a
	// usable answer as long as the caller is told. Re-stale FETCH_HEAD
	// first: a failed `git fetch` can still leave an (empty) FETCH_HEAD
	// behind, which would make the next read a TTL hit and skip the
	// failure path we are asserting.
	touchFetchHead(t, root, time.Now().Add(-60*time.Second))
	out, code := RunReady(root, ReadyOptions{})
	if code != 0 {
		t.Fatalf("RunReady after a failed refresh: code=%d out=%+v", code, out)
	}
	rr, ok := out.(ReadyResult)
	if !ok {
		t.Fatalf("output type = %T, want ReadyResult", out)
	}
	if rr.Refresh == nil || rr.Refresh.Error == "" {
		t.Fatalf("ReadyResult.Refresh does not report the failed refresh: %+v", rr.Refresh)
	}
	if FormatRefreshWarning(rr.Refresh) == "" {
		t.Errorf("no stderr warning would be printed for a failed refresh")
	}
}
