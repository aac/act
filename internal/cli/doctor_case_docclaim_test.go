package cli

// Phase 2 ticket 9 (act-aa4f19) — doctor extension TestDocClaim_*
// tests. Each function asserts one user-visible literal — either a
// finding message that appears verbatim in docs/spec-v2.md or a JSON
// field name from the RemoteStatus block. The sweep registry in
// docs_sweep_test.go pins each tuple of (doc surface, claim, asserting
// test) so a doc edit and an implementation drift both surface as
// build breaks.
//
// The five `doctor-case-*` registry entries each map to one of:
//
//	doctor-case-a-prime        → TestDocClaim_DoctorCase_APrime_HookCheckOnOrchestrator
//	doctor-case-c-prime        → TestDocClaim_DoctorCase_CPrime_WorkerWithoutOrigin
//	doctor-case-f              → TestDocClaim_DoctorCase_F_UnpushedCommitsStderr
//	doctor-case-g              → TestDocClaim_DoctorCase_G_OriginUnreachableStderr
//	doctor-case-h              → TestDocClaim_DoctorCase_H_UpstreamDriftStderr
//
// Implementation note: the cases all share one walk inside
// checkPhase2Reconciliation (see doctor_phase2.go); the tests below
// drive RunDoctor with `--check phase2-reconciliation` to scope the
// assertions to the new surface.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/config"
)

// TestDocClaim_DoctorCase_APrime_HookCheckOnOrchestrator asserts the
// case (a') literal: when the nested `.act/.git/config` has
// `act.role=orchestrator` AND `remote.origin.url` set BUT no
// post-receive hook installed, doctor surfaces a finding under the
// CheckRemoteAttachedOrchestrator name. The finding message names the
// remedy `act remote enable` so the operator knows the recovery path.
func TestDocClaim_DoctorCase_APrime_HookCheckOnOrchestrator(t *testing.T) {
	root := makeCreateRepo(t)
	configPath := config.ActGitConfigPath(filepath.Join(root, ".act"))

	// Configure the nested .act/.git as an orchestrator with origin
	// set but NO post-receive hook (we deliberately don't install one).
	mustGit(t, root, "config", "-f", configPath, "act.role", "orchestrator")
	mustGit(t, root, "config", "-f", configPath, "remote.origin.url",
		"file:///nonexistent/orchestrator.git")
	// Ensure the post-receive hook doesn't exist (makeCreateRepo
	// doesn't install one, but we assert the precondition).
	hookPath := config.PostReceiveHookPath(filepath.Join(root, ".act"))
	_ = os.Remove(hookPath)

	out, _ := RunDoctor(root, DoctorOptions{
		Check:   "phase2-reconciliation",
		NoFetch: true, // suppress case (g) so we isolate the (a') finding
	})
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("output type = %T, want DoctorResult", out)
	}
	found := false
	for _, f := range res.Findings {
		if f.Check == CheckRemoteAttachedOrchestrator {
			found = true
			if !strings.Contains(f.Message, "post-receive hook") {
				t.Errorf("case (a') message missing 'post-receive hook': %q", f.Message)
			}
			if !strings.Contains(f.Message, "act remote enable") {
				t.Errorf("case (a') message missing remedy 'act remote enable': %q", f.Message)
			}
		}
	}
	if !found {
		t.Errorf("expected case (a') finding (%s), got %+v",
			CheckRemoteAttachedOrchestrator, res.Findings)
	}
}

// TestDocClaim_DoctorCase_CPrime_WorkerWithoutOrigin asserts the case
// (c') literal: a worker (act.role=worker) without an origin
// configured in the nested .act/.git/config surfaces a finding under
// the CheckWorkerWithoutOrigin name. The remedy literal `act bootstrap-
// worker --from-remote` appears in the finding message.
func TestDocClaim_DoctorCase_CPrime_WorkerWithoutOrigin(t *testing.T) {
	root := makeCreateRepo(t)
	configPath := config.ActGitConfigPath(filepath.Join(root, ".act"))

	mustGit(t, root, "config", "-f", configPath, "act.role", "worker")
	// Deliberately do NOT configure remote.origin.url.

	out, _ := RunDoctor(root, DoctorOptions{
		Check:   "phase2-reconciliation",
		NoFetch: true,
	})
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("output type = %T, want DoctorResult", out)
	}
	found := false
	for _, f := range res.Findings {
		if f.Check == CheckWorkerWithoutOrigin {
			found = true
			if !strings.Contains(f.Message, "act.role=worker") {
				t.Errorf("case (c') message missing 'act.role=worker': %q", f.Message)
			}
			if !strings.Contains(f.Message, "bootstrap-worker --from-remote") {
				t.Errorf("case (c') message missing remedy 'bootstrap-worker --from-remote': %q", f.Message)
			}
		}
	}
	if !found {
		t.Errorf("expected case (c') finding (%s), got %+v",
			CheckWorkerWithoutOrigin, res.Findings)
	}
}

// TestDocClaim_DoctorCase_F_UnpushedCommitsStderr asserts the case (f)
// stderr literal: when the nested `.act/.git` has two commits ahead of
// `origin/<branch>`, doctor surfaces a finding under
// CheckUnpushedCommits whose message contains the literal `local: 2
// unpushed commits ahead of origin`. The JSON field
// `local_unpushed_count` is also populated.
func TestDocClaim_DoctorCase_F_UnpushedCommitsStderr(t *testing.T) {
	root, configPath := makeRepoWithOriginAhead(t, 2)
	// Mark this as a worker with origin so case (a')/(c') don't fire.
	mustGit(t, root, "config", "-f", configPath, "act.role", "worker")

	out, _ := RunDoctor(root, DoctorOptions{
		Check:   "phase2-reconciliation",
		NoFetch: true, // skip case (g) — we don't have a reachable origin
	})
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("output type = %T, want DoctorResult", out)
	}

	if got := res.RemoteStatus.LocalUnpushedCount; got != 2 {
		t.Errorf("RemoteStatus.LocalUnpushedCount = %d, want 2", got)
	}

	found := false
	for _, f := range res.Findings {
		if f.Check == CheckUnpushedCommits {
			found = true
			want := "local: 2 unpushed commits ahead of origin"
			if !strings.Contains(f.Message, want) {
				t.Errorf("case (f) message missing %q: %q", want, f.Message)
			}
		}
	}
	if !found {
		t.Errorf("expected case (f) finding (%s), got %+v",
			CheckUnpushedCommits, res.Findings)
	}
}

// TestDocClaim_DoctorCase_G_OriginUnreachableStderr asserts the case
// (g) literal: when origin is configured to an unreachable URL, doctor
// surfaces a finding whose message is exactly
// `remote: origin unreachable; run 'act remote sync' from the
// orchestrator or check connectivity`. Exit is 4. Under --no-fetch the
// same condition yields a warning (exit 0) and the literal is NOT
// emitted (case-(g) probe was skipped).
func TestDocClaim_DoctorCase_G_OriginUnreachableStderr(t *testing.T) {
	root := makeCreateRepo(t)
	configPath := config.ActGitConfigPath(filepath.Join(root, ".act"))

	// Point origin at a file-URL that doesn't exist — the fetch will
	// fail fast (no DNS, no TCP).
	bogus := filepath.Join(t.TempDir(), "missing-origin.git")
	mustGit(t, root, "config", "-f", configPath, "remote.origin.url", "file://"+bogus)

	// Without --no-fetch: the probe runs and case (g) fires with exit 4.
	out, code := RunDoctor(root, DoctorOptions{Check: "phase2-reconciliation"})
	if code != 4 {
		t.Fatalf("exit = %d, want 4 (case (g) origin-unreachable); out=%+v", code, out)
	}
	res := out.(DoctorResult)
	if res.RemoteStatus.RemoteReachable {
		t.Errorf("RemoteStatus.RemoteReachable = true, want false")
	}
	if res.RemoteStatus.FetchFailureReason == "" {
		t.Errorf("RemoteStatus.FetchFailureReason is empty; want non-empty")
	}
	found := false
	for _, f := range res.Findings {
		if f.Check == CheckRemoteReachable && f.Severity == "error" {
			found = true
			want := "remote: origin unreachable; run 'act remote sync' from the orchestrator or check connectivity"
			if f.Message != want {
				t.Errorf("case (g) message = %q, want %q", f.Message, want)
			}
		}
	}
	if !found {
		t.Errorf("expected case (g) error finding (%s), got %+v",
			CheckRemoteReachable, res.Findings)
	}

	// With --no-fetch: same fixture, but the probe is skipped → warn,
	// exit 0. The error literal MUST NOT appear.
	out, code = RunDoctor(root, DoctorOptions{
		Check:   "phase2-reconciliation",
		NoFetch: true,
	})
	if code != 0 {
		t.Fatalf("--no-fetch exit = %d, want 0; out=%+v", code, out)
	}
	res = out.(DoctorResult)
	for _, f := range res.Findings {
		if f.Check == CheckRemoteReachable && f.Severity == "error" {
			t.Errorf("--no-fetch must not emit the case (g) error literal: %+v", f)
		}
	}
}

// TestDocClaim_DoctorCase_H_UpstreamDriftStderr asserts the case (h)
// literal: when `origin-upstream/<branch>` is N commits behind
// `origin/<branch>` (with N > the configured threshold), doctor
// surfaces a finding whose message contains the literal `upstream:
// origin-upstream is <N> commits behind origin; run 'act remote sync'`.
// Under --no-fetch case (h) is suppressed entirely.
func TestDocClaim_DoctorCase_H_UpstreamDriftStderr(t *testing.T) {
	root, configPath := makeRepoWithUpstreamDrift(t, 60)
	mustGit(t, root, "config", "-f", configPath, "act.role", "worker")

	out, _ := RunDoctor(root, DoctorOptions{
		Check: "phase2-reconciliation",
		// Note: NoFetch=false so case (h) can run. The fetch dry-run
		// against the local file:// origin succeeds (it's a real
		// git repo on disk), so case (g) does NOT fire.
	})
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("output type = %T, want DoctorResult", out)
	}

	if got := res.RemoteStatus.UpstreamDriftCommits; got != 60 {
		t.Errorf("RemoteStatus.UpstreamDriftCommits = %d, want 60", got)
	}

	found := false
	for _, f := range res.Findings {
		if f.Check == CheckUpstreamDrift {
			found = true
			want := "upstream: origin-upstream is 60 commits behind origin; run 'act remote sync'"
			if !strings.Contains(f.Message, want) {
				t.Errorf("case (h) message missing %q: %q", want, f.Message)
			}
		}
	}
	if !found {
		t.Errorf("expected case (h) finding (%s), got %+v",
			CheckUpstreamDrift, res.Findings)
	}

	// Under --no-fetch case (h) must be suppressed entirely.
	out, _ = RunDoctor(root, DoctorOptions{
		Check:   "phase2-reconciliation",
		NoFetch: true,
	})
	res = out.(DoctorResult)
	for _, f := range res.Findings {
		if f.Check == CheckUpstreamDrift {
			t.Errorf("case (h) must be suppressed under --no-fetch, got %+v", f)
		}
	}
}

// TestRunDoctor_SlowWritesSummary_LastHour asserts the AC: write five
// records spaced over two hours and confirm the summary reports two
// (the recent half within the last hour). The records are written
// directly to `.act/.slow-writes` via AppendSlowWrite so the test
// doesn't depend on the fault-injected commit path.
func TestRunDoctor_SlowWritesSummary_LastHour(t *testing.T) {
	root := makeCreateRepo(t)
	actRoot := filepath.Join(root, ".act")

	now := time.Now()
	// Five records at -120min, -90min, -45min, -20min, -5min from now.
	// Only the last three are within the last hour, but the AC names
	// 5 records over 2 hours / 2 in the last hour. Adjust offsets
	// so exactly the second half (2 records) land within the hour:
	//   r0: -110min  (outside)
	//   r1: -80min   (outside)
	//   r2: -50min   (outside — 50min < 60min, INSIDE)
	// hmm, recompute. 2hr / 5 records = 24min spacing. Place at
	// -110, -86, -62, -38, -14 min → last hour: r3 (-38) and
	// r4 (-14) → exactly 2. r2 at -62min is just outside.
	offsets := []time.Duration{
		-110 * time.Minute,
		-86 * time.Minute,
		-62 * time.Minute,
		-38 * time.Minute,
		-14 * time.Minute,
	}
	for i, off := range offsets {
		ts := now.Add(off)
		rec := SlowWriteRecord{
			Timestamp:  FormatSlowWriteTimestamp(ts),
			OpID:       fmt.Sprintf("act-deadbeef%02d", i),
			DurationMs: 2000,
			OpType:     "create",
		}
		if err := AppendSlowWrite(actRoot, rec); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// The summary is computed by countSlowWritesLastHour; doctor exposes
	// it via the RemoteStatus block.
	out, _ := RunDoctor(root, DoctorOptions{
		Check:   "phase2-reconciliation",
		NoFetch: true,
	})
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("output type = %T, want DoctorResult", out)
	}
	if got := res.RemoteStatus.SlowWritesLastHour; got != 2 {
		t.Errorf("RemoteStatus.SlowWritesLastHour = %d, want 2 (offsets=%v)", got, offsets)
	}
}

// TestRunDoctor_RemoteStatus_JSONFields asserts the JSON shape of the
// RemoteStatus block: all five fields are present (no `omitempty`) so
// JSON consumers can rely on the keys existing. This pins the
// remote-status JSON contract pinned by the ticket-9 brief.
func TestRunDoctor_RemoteStatus_JSONFields(t *testing.T) {
	root := makeCreateRepo(t)
	out, _ := RunDoctor(root, DoctorOptions{
		Check:   "phase2-reconciliation",
		NoFetch: true,
	})
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("output type = %T, want DoctorResult", out)
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, field := range []string{
		`"remote_status"`,
		`"remote_reachable"`,
		`"local_unpushed_count"`,
		`"upstream_drift_commits"`,
		`"slow_writes_last_hour"`,
		`"fetch_failure_reason"`,
	} {
		if !strings.Contains(s, field) {
			t.Errorf("JSON output missing field %s in %s", field, s)
		}
	}
}

// --- fixture helpers --------------------------------------------------

// makeRepoWithOriginAhead returns (hostRoot, nestedConfigPath) where
// the nested `.act/.git` has `n` local commits ahead of
// `origin/<branch>`. It builds a second on-disk bare-equivalent git
// repo as the origin so refs/remotes/origin/<branch> can be populated
// without network. After setup, `git rev-list --count
// origin/<branch>..HEAD` inside the nested repo returns exactly `n`.
func makeRepoWithOriginAhead(t *testing.T, n int) (string, string) {
	t.Helper()
	host := makeCreateRepo(t)
	nestedGit := filepath.Join(host, ".act", ".git")
	configPath := filepath.Join(nestedGit, "config")

	// Build a sibling git repo to act as origin. Clone the nested
	// .act repo into it so origin/<branch> is initially at the same
	// commit as the nested HEAD.
	originDir := filepath.Join(t.TempDir(), "origin.git")
	mustGit(t, host, "clone", "--bare", filepath.Join(host, ".act"), originDir)

	// Configure remote.origin.url on the nested .act/.git → bare.
	mustGit(t, host, "config", "-f", configPath, "remote.origin.url", originDir)
	mustGit(t, host, "config", "-f", configPath,
		"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	// One fetch to populate refs/remotes/origin/<branch>.
	cmd := exec.Command("git", "--git-dir="+nestedGit, "fetch", "origin")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initial fetch: %v\n%s", err, out)
	}

	// Add `n` new commits to the nested .act repo (no push). Each
	// commit touches a unique file so git advances HEAD without
	// reconciling against the unpushed origin ref.
	actRoot := filepath.Join(host, ".act")
	for i := 0; i < n; i++ {
		p := filepath.Join(actRoot, fmt.Sprintf("noise-%d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write noise: %v", err)
		}
		mustGit(t, actRoot, "add", filepath.Base(p))
		mustGit(t, actRoot, "commit", "-q", "--no-verify",
			"-m", fmt.Sprintf("noise %d", i))
	}
	return host, configPath
}

// makeRepoWithUpstreamDrift returns (hostRoot, nestedConfigPath) where
// the nested `.act/.git` has `origin/<branch>` `n` commits ahead of
// `origin-upstream/<branch>`. Both remotes are local file:// URLs so
// no network is required.
func makeRepoWithUpstreamDrift(t *testing.T, n int) (string, string) {
	t.Helper()
	host := makeCreateRepo(t)
	nestedGit := filepath.Join(host, ".act", ".git")
	configPath := filepath.Join(nestedGit, "config")
	actRoot := filepath.Join(host, ".act")

	// Build two sibling bare repos: origin and origin-upstream. Both
	// start at the nested HEAD.
	originDir := filepath.Join(t.TempDir(), "origin.git")
	upstreamDir := filepath.Join(t.TempDir(), "origin-upstream.git")
	mustGit(t, host, "clone", "--bare", actRoot, originDir)
	mustGit(t, host, "clone", "--bare", actRoot, upstreamDir)

	// Wire both remotes into the nested config + initial fetches.
	mustGit(t, host, "config", "-f", configPath, "remote.origin.url", originDir)
	mustGit(t, host, "config", "-f", configPath,
		"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	mustGit(t, host, "config", "-f", configPath,
		"remote."+UpstreamRemoteName+".url", upstreamDir)
	mustGit(t, host, "config", "-f", configPath,
		"remote."+UpstreamRemoteName+".fetch",
		"+refs/heads/*:refs/remotes/"+UpstreamRemoteName+"/*")

	// Advance origin by `n` commits (push from nested), leaving
	// origin-upstream behind. Each commit is one noise file.
	for i := 0; i < n; i++ {
		p := filepath.Join(actRoot, fmt.Sprintf("orig-noise-%d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write noise: %v", err)
		}
		mustGit(t, actRoot, "add", filepath.Base(p))
		mustGit(t, actRoot, "commit", "-q", "--no-verify",
			"-m", fmt.Sprintf("orig-noise %d", i))
	}
	mustGit(t, actRoot, "push", "origin", "HEAD")
	// Pre-fetch the remotes so refs/remotes/* are populated before
	// the doctor case-(h) walk runs.
	cmd := exec.Command("git", "--git-dir="+nestedGit, "fetch", "origin")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fetch origin: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "--git-dir="+nestedGit, "fetch", UpstreamRemoteName)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fetch upstream: %v\n%s", err, out)
	}
	// Configure a small threshold so 60 commits exceeds it. The
	// default is 50, so 60 already exceeds; an explicit set here
	// future-proofs the test against a default change.
	mustGit(t, host, "config", "-f", configPath,
		config.UpstreamDriftThresholdCommitsKey, "50")
	return host, configPath
}
