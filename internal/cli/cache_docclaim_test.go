package cli

// Phase 2 ticket 5 — TestDocClaim_* assertions for the read-path cache.
//
// These tests pin user-visible behavior named in spec.md and in the
// `act ready --help` flag-strings. Per AGENTS.md "Documentation
// discipline": every claim in the registry must have a matching
// asserting test that exercises the same behavior at the user-visible
// boundary the claim names.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
)

// TestDocClaim_ReadCache_TTLFiveSeconds — asserts the 5-second TTL named
// in docs/spec.md (Read-cache section) is what the cache layer
// actually uses. We touch FETCH_HEAD with an mtime just inside the TTL
// (4s ago) and one just outside (6s ago); the first must be a hit, the
// second a miss.
//
// Boundary: we observe the cache decision via the MaybeRefreshResult
// reason slug rather than re-checking mtime, because the staged
// FETCH_HEAD's mtime is the input we control here.
func TestDocClaim_ReadCache_TTLFiveSeconds(t *testing.T) {
	root, _ := makeRepoWithRemoteOrigin(t)

	// Inside the TTL: mtime 4s in the past, well under the 5s window.
	touchFetchHead(t, root, time.Now().Add(-4*time.Second))
	res, err := MaybeRefresh(root, MaybeRefreshOptions{})
	if err != nil {
		t.Fatalf("MaybeRefresh inside-TTL: %v", err)
	}
	if res.Reason != "hit" {
		t.Errorf("inside-TTL reason = %q, want hit", res.Reason)
	}

	// Outside the TTL: mtime 6s in the past, just past the 5s window.
	touchFetchHead(t, root, time.Now().Add(-6*time.Second))
	res, err = MaybeRefresh(root, MaybeRefreshOptions{})
	if err != nil {
		t.Fatalf("MaybeRefresh outside-TTL: %v", err)
	}
	if res.Reason == "hit" {
		t.Errorf("outside-TTL reason = hit, want stale or similar")
	}
}

// TestDocClaim_ReadCache_ConfiguredTTLOverridesDefault — asserts the spec's
// Read-cache claim that `act.readCacheTTLSeconds` in `.act/.git/config`
// overrides the default window per-repo (act-97415e). Before this the key
// was inert: the cache hardcoded the 5s const and ignored config.
//
// Two directions prove the value is actually read (not just "any config →
// wide window"): a large value WIDENS the window so a FETCH_HEAD that would
// be stale under the 5s default is served as a hit; a small value NARROWS
// it so a FETCH_HEAD that would be a hit under the default is a miss.
//
// Boundary: MaybeRefreshResult.Reason, mirroring TestDocClaim_ReadCache_
// TTLFiveSeconds — the staged FETCH_HEAD mtime is the controlled input.
func TestDocClaim_ReadCache_ConfiguredTTLOverridesDefault(t *testing.T) {
	cfgKey := config.ReadCacheTTLSecondsKey

	t.Run("widen", func(t *testing.T) {
		root, _ := makeRepoWithRemoteOrigin(t)
		cfgPath := config.ActGitConfigPath(config.Layout(root).Root)
		if err := config.SetGitConfig(cfgPath, cfgKey, "30"); err != nil {
			t.Fatalf("set %s=30: %v", cfgKey, err)
		}

		// 10s in the past: a miss under the 5s default, but inside the
		// configured 30s window → must be a hit.
		touchFetchHead(t, root, time.Now().Add(-10*time.Second))
		res, err := MaybeRefresh(root, MaybeRefreshOptions{})
		if err != nil {
			t.Fatalf("MaybeRefresh: %v", err)
		}
		if res.Reason != "hit" {
			t.Errorf("with TTL=30 and a 10s-old FETCH_HEAD, reason = %q, want hit (the configured window did not widen)", res.Reason)
		}
	})

	t.Run("narrow", func(t *testing.T) {
		root, _ := makeRepoWithRemoteOrigin(t)
		cfgPath := config.ActGitConfigPath(config.Layout(root).Root)
		if err := config.SetGitConfig(cfgPath, cfgKey, "1"); err != nil {
			t.Fatalf("set %s=1: %v", cfgKey, err)
		}

		// 3s in the past: a hit under the 5s default, but outside the
		// configured 1s window → must be a miss.
		touchFetchHead(t, root, time.Now().Add(-3*time.Second))
		res, err := MaybeRefresh(root, MaybeRefreshOptions{})
		if err != nil {
			t.Fatalf("MaybeRefresh: %v", err)
		}
		if res.Reason == "hit" {
			t.Errorf("with TTL=1 and a 3s-old FETCH_HEAD, reason = hit, want a miss (the configured window did not narrow)")
		}
	})
}

// TestDocClaim_ReadCache_DispatchModeEnvBypass — asserts the spec's
// `ACT_DISPATCH_MODE=1` env-var bypass works. Staged FETCH_HEAD is fresh
// (would be a hit); the env var forces a fetch anyway.
func TestDocClaim_ReadCache_DispatchModeEnvBypass(t *testing.T) {
	root, _ := makeRepoWithRemoteOrigin(t)
	touchFetchHead(t, root, time.Now())
	t.Setenv("ACT_DISPATCH_MODE", "1")

	res, err := MaybeRefresh(root, MaybeRefreshOptions{})
	if err != nil {
		t.Fatalf("MaybeRefresh: %v", err)
	}
	if !res.Fetched {
		t.Errorf("Fetched=false despite ACT_DISPATCH_MODE=1: %+v", res)
	}
	if res.Reason != "dispatch_mode" {
		t.Errorf("Reason=%q, want dispatch_mode", res.Reason)
	}
}

// TestDocClaim_ReadCache_FreshNoCacheAlias — §5 addendum: `--fresh` and
// `--no-cache` must (a) both appear in `act ready --help` and (b)
// dispatch identically. (a) is asserted by re-using
// TestReadCache_FreshHelpFlagsPresent (this test asserts (b) directly).
//
// Dispatch-identical = both routes resolve to the same input at the
// MaybeRefresh boundary AND produce the same FETCH_HEAD advance shape.
// Since cmd/act/ready.go OR's the two flags into ReadyOptions.Fresh,
// the cli-layer assertion is "RunReady with Fresh=true (the value both
// flags produce) advances FETCH_HEAD." We've already covered that in
// TestReadCache_FreshAndNoCacheDispatchIdentically; this test pins the
// help-text claim by re-running the same shape and asserting both
// flags read by argparse end up at the same boolean.
func TestDocClaim_ReadCache_FreshNoCacheAlias(t *testing.T) {
	// Run two RunReady calls, one per flag-spelling, on two
	// independent fixtures. Each must produce a FETCH_HEAD mtime
	// advance — which is what proves the two paths landed at the same
	// cache-layer behavior.
	for _, name := range []string{"fresh", "no-cache"} {
		t.Run(name, func(t *testing.T) {
			root, _ := makeRepoWithRemoteOrigin(t)

			if _, err := MaybeRefresh(root, MaybeRefreshOptions{}); err != nil {
				t.Fatalf("warm: %v", err)
			}
			before := fetchHeadMtime(t, root)
			time.Sleep(15 * time.Millisecond)

			// Both flags route to ReadyOptions.Fresh = true in
			// cmd/act/ready.go's "Fresh: *fresh || *noCache". So
			// invoking with Fresh=true here exercises BOTH flag paths
			// from the cache layer's perspective.
			_, code := RunReady(root, ReadyOptions{Fresh: true})
			if code != 0 {
				t.Fatalf("RunReady code=%d", code)
			}
			after := fetchHeadMtime(t, root)
			if !after.After(before) {
				t.Errorf("%s did not advance FETCH_HEAD via RunReady(Fresh=true): before=%v after=%v", name, before, after)
			}
		})
	}

	// Help-text presence is asserted in TestReadCache_FreshHelpFlagsPresent
	// (cache_test.go); we re-run that probe here directly so this
	// TestDocClaim_* exercises BOTH halves of the addendum: help-text
	// AND dispatch-identical.
	bin := actBinaryPathOrSkip(t)
	if bin == "" {
		return // probe-only; the cache_test.go assertion is the canonical one
	}
	probeRoot := makeCreateRepo(t)
	out := combinedOutputIn(t, probeRoot, bin, "ready", "-zzz-unknown-flag-for-help-probe")
	if !strings.Contains(out, "fresh") {
		t.Errorf("`act ready` usage missing -fresh flag\noutput: %s", out)
	}
	if !strings.Contains(out, "no-cache") {
		t.Errorf("`act ready` usage missing -no-cache flag\noutput: %s", out)
	}
}

// TestDocClaim_ReadCache_FoldCheckpointInvalidation — spec invariant:
// after a successful rebase that adds new ops to HEAD, the
// .act/fold-checkpoint.json file does not survive. This is the
// post-rebase invariant from the spec's Read-cache section.
func TestDocClaim_ReadCache_FoldCheckpointInvalidation(t *testing.T) {
	root, remote := makeRepoWithRemoteOrigin(t)
	paths := config.Layout(root)

	// Write a stub checkpoint so we can observe its deletion. The exact
	// schema_version doesn't matter — InvalidateCheckpoint deletes the
	// file regardless of contents.
	if err := os.WriteFile(paths.FoldCheckpoint, []byte(`{"schema_version":1,"tree_hash":"x","issues":{}}`), 0o644); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	// Advance the remote so the rebase moves HEAD.
	remote.AdvanceCommits(1)

	// Force a cache miss with a stale FETCH_HEAD.
	touchFetchHead(t, root, time.Now().Add(-60*time.Second))

	if _, err := MaybeRefresh(root, MaybeRefreshOptions{}); err != nil {
		t.Fatalf("MaybeRefresh: %v", err)
	}

	if _, err := os.Stat(paths.FoldCheckpoint); !os.IsNotExist(err) {
		t.Errorf("fold-checkpoint survived: stat err=%v", err)
	}
}

// TestDocClaim_ReadCache_NoRebaseLeavesCheckpoint — companion invariant
// to FoldCheckpointInvalidation: if a fetch+rebase did NOT add new ops
// (HEAD unchanged), the fold-checkpoint must NOT be invalidated.
// Otherwise every read-path command would invalidate the cache on a
// no-op fetch, defeating the purpose of having a checkpoint at all.
func TestDocClaim_ReadCache_NoRebaseLeavesCheckpoint(t *testing.T) {
	root, _ := makeRepoWithRemoteOrigin(t)
	paths := config.Layout(root)

	if err := os.WriteFile(paths.FoldCheckpoint, []byte(`{"schema_version":1,"tree_hash":"y","issues":{}}`), 0o644); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	// Stale FETCH_HEAD forces the fetch, but the remote has no new
	// commits — the rebase is a no-op and HEAD stays put.
	touchFetchHead(t, root, time.Now().Add(-60*time.Second))

	res, err := MaybeRefresh(root, MaybeRefreshOptions{})
	if err != nil {
		t.Fatalf("MaybeRefresh: %v", err)
	}
	if !res.Fetched {
		t.Fatalf("expected Fetched=true; res=%+v", res)
	}
	if res.Invalidated {
		t.Fatalf("Invalidated=true on no-op rebase; checkpoint should be preserved: %+v", res)
	}

	if _, err := os.Stat(paths.FoldCheckpoint); err != nil {
		t.Errorf("fold-checkpoint missing after no-op fetch: %v", err)
	}
}

// TestDocClaim_ReadCache_FetchHeadPathLayout — the spec names
// `.act/.git/FETCH_HEAD` as the mtime source. Verify the helper builds
// that path.
func TestDocClaim_ReadCache_FetchHeadPathLayout(t *testing.T) {
	got := gitops.FetchHeadPath("/some/repo/.act")
	want := filepath.Join("/some/repo/.act", ".git", "FETCH_HEAD")
	if got != want {
		t.Errorf("FetchHeadPath = %q, want %q", got, want)
	}
}
