package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// clpNextClaimed runs `act next --json <args>`, wants exit 0 and a claim, and
// returns the claimed issue's id and stderr.
func clpNextClaimed(t *testing.T, dir string, args ...string) (string, string) {
	t.Helper()
	out, stderr := pass5Run(t, dir, 0, "", append([]string{"next", "--json"}, args...)...)
	var res struct {
		Claimed bool `json:"claimed"`
		Issue   struct {
			ID string `json:"id"`
		} `json:"issue"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil || !res.Claimed || res.Issue.ID == "" {
		t.Fatalf("act next --json %v: not a claiming result (%v)\nstdout=%s\nstderr=%s", args, err, out, stderr)
	}
	return res.Issue.ID, stderr
}

// TestDocClaim_Spec_ClaimIsAutoPublished asserts the claim sentences of the
// "Auto-publish on write" paragraph in docs/spec.md: in a tracker with an
// `origin`, a won claim is pushed like every other write, without `--push`,
// whether it came from `act update --claim` or `act next`; `--isolated`
// holds it back; and an origin that cannot be reached defers it (act-2cfc30:
// a claim without `--push` used to stay local, unqueued, until the next
// write, so no other checkout could see the issue was taken).
func TestDocClaim_Spec_ClaimIsAutoPublished(t *testing.T) {
	t.Run("update --claim, origin", func(t *testing.T) {
		dir := sp6Site(t)
		bare := wfxAddBareOrigin(t, dir)
		made, _ := wfxCreate(t, dir, "clp claim target")
		if !wfxOriginHasHead(t, dir, bare) {
			t.Fatalf("setup: origin does not hold the create commit")
		}

		res, stderr := upuUpdate(t, dir, made.ID, "--claim")
		if !res.Claimed || res.PushDeferred || len(res.Warnings) != 0 {
			t.Fatalf("act update --json --claim with origin: claimed %v, push_deferred %v, warnings %v; want claimed, no deferral, no warnings\nstderr=%s",
				res.Claimed, res.PushDeferred, res.Warnings, stderr)
		}
		if !wfxOriginHasHead(t, dir, bare) {
			t.Errorf("after act update --claim (no --push), origin does not hold the claim commit")
		}
		if q := pnrPendingPushes(t, dir); q != "" {
			t.Errorf("a claim that reached origin left .act/.pending-pushes non-empty:\n%s", q)
		}
	})

	t.Run("update --claim --isolated, origin", func(t *testing.T) {
		dir := sp6Site(t)
		bare := wfxAddBareOrigin(t, dir)
		made, _ := wfxCreate(t, dir, "clp isolated claim target")

		res, stderr := upuUpdate(t, dir, made.ID, "--claim", "--isolated")
		if !res.Claimed || res.PushDeferred {
			t.Fatalf("act update --json --claim --isolated: claimed %v, push_deferred %v; want claimed and no deferral\nstderr=%s", res.Claimed, res.PushDeferred, stderr)
		}
		if wfxOriginHasHead(t, dir, bare) {
			t.Errorf("act update --claim --isolated pushed the claim commit to origin")
		}
		if q := pnrPendingPushes(t, dir); q != "" {
			t.Errorf("act update --claim --isolated queued a push in .act/.pending-pushes:\n%s", q)
		}
		// The next write without --isolated carries the held claim along.
		upuUpdate(t, dir, made.ID, "--description", "clp follow-up write")
		if !wfxOriginHasHead(t, dir, bare) {
			t.Errorf("the write after an isolated claim did not reach origin")
		}
	})

	t.Run("update --claim, unreachable origin", func(t *testing.T) {
		dir := sp6Site(t)
		made, _ := wfxCreate(t, dir, "clp deferred claim target")
		upuAddMissingOrigin(t, dir)

		res, stderr := upuUpdate(t, dir, made.ID, "--claim")
		if res.Error != "" || !res.Claimed || !res.PushDeferred || res.PushDeferredReason == "" {
			t.Errorf("act update --json --claim with an unreachable origin: error %q, claimed %v, push_deferred %v, push_deferred_reason %q; want no error, claimed, deferred, a reason\nstderr=%s",
				res.Error, res.Claimed, res.PushDeferred, res.PushDeferredReason, stderr)
		}
		if st := sp8Str(sp8Show(t, dir, made.ID)["status"]); st != "in_progress" {
			t.Errorf("after update --claim with an unreachable origin, show reports status %q, want in_progress", st)
		}
		if n := strings.Count(stderr, "COMMITTED LOCALLY but NOT PUSHED"); n != 1 {
			t.Errorf("update --claim with an unreachable origin: stderr has %d not-pushed warning blocks, want 1:\n%s", n, stderr)
		}
		if q := strings.TrimSpace(pnrPendingPushes(t, dir)); q == "" || strings.Count(q, "\n") != 0 || !strings.Contains(q, `"claim"`) {
			t.Errorf("update --claim with an unreachable origin: .act/.pending-pushes should hold exactly one claim entry, got:\n%s", q)
		}
	})

	t.Run("update --claim --offline, origin", func(t *testing.T) {
		dir := sp6Site(t)
		bare := wfxAddBareOrigin(t, dir)
		made, _ := wfxCreate(t, dir, "clp offline claim target")

		res, stderr := upuUpdate(t, dir, made.ID, "--claim", "--offline")
		if !res.Claimed || res.PushDeferred || stderr != "" {
			t.Fatalf("act update --json --claim --offline: claimed %v, push_deferred %v, stderr %q; want claimed, no deferral, quiet", res.Claimed, res.PushDeferred, stderr)
		}
		if wfxOriginHasHead(t, dir, bare) {
			t.Errorf("act update --claim --offline pushed the claim commit to origin")
		}
		if q := pnrPendingPushes(t, dir); !strings.Contains(q, `"claim"`) {
			t.Errorf("act update --claim --offline: .act/.pending-pushes should hold a claim entry, got:\n%s", q)
		}
	})

	t.Run("re-claim of a held issue, unreachable origin", func(t *testing.T) {
		dir := sp6Site(t)
		made, _ := wfxCreate(t, dir, "clp re-claim target")
		upuUpdate(t, dir, made.ID, "--claim") // no origin yet: local only
		upuAddMissingOrigin(t, dir)

		res, stderr := upuUpdate(t, dir, made.ID, "--claim")
		if !res.Claimed || res.PushDeferred || stderr != "" {
			t.Errorf("re-claim of an issue this checkout holds, origin unreachable: claimed %v, push_deferred %v, stderr %q; want claimed, no deferral, quiet (it wrote no op, so there is nothing to push)", res.Claimed, res.PushDeferred, stderr)
		}
		if q := pnrPendingPushes(t, dir); q != "" {
			t.Errorf("re-claim of a held issue queued a push in .act/.pending-pushes:\n%s", q)
		}
	})

	t.Run("next, origin", func(t *testing.T) {
		dir := sp6Site(t)
		bare := wfxAddBareOrigin(t, dir)
		made, _ := wfxCreate(t, dir, "clp next target")

		id, stderr := clpNextClaimed(t, dir)
		if id != made.ID {
			t.Fatalf("act next claimed %s, want %s", id, made.ID)
		}
		if !wfxOriginHasHead(t, dir, bare) {
			t.Errorf("after act next, origin does not hold the claim commit\nstderr=%s", stderr)
		}
	})

	t.Run("next --isolated, origin", func(t *testing.T) {
		dir := sp6Site(t)
		bare := wfxAddBareOrigin(t, dir)
		wfxCreate(t, dir, "clp isolated next target")

		clpNextClaimed(t, dir, "--isolated")
		if wfxOriginHasHead(t, dir, bare) {
			t.Errorf("act next --isolated pushed the claim commit to origin")
		}
		if q := pnrPendingPushes(t, dir); q != "" {
			t.Errorf("act next --isolated queued a push in .act/.pending-pushes:\n%s", q)
		}
	})
}
