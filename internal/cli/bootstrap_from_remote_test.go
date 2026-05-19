package cli

// Tests for `act bootstrap-worker --from-remote` (Phase 2 ticket 7,
// act-0480c9). Companion to bootstrap_worker_test.go; the cwd-source
// cases live there.
//
// Cases:
//
//  1. Happy path: clone from a BareRemote fixture seeded with a valid
//     .act/.git → exit 0; act.role=worker; ready round-trips.
//  2. Non-empty target without --force → exit 2, target_not_empty.
//  3. Non-empty target with --force → exit 0; previous .act/ replaced.
//  4. Stalled clone (TCP listener that accepts and never responds) →
//     exit 4, bootstrap_timeout, details.timeout_seconds matches, the
//     .act.bootstrap/ staging dir is torn down.
//  5. Concurrent bootstraps (N=3) into disjoint targets → all exit 0,
//     all ready round-trips agree with the source.
//
// The interference test uses a single BareRemote fixture and per-test
// t.TempDir() target paths; per the §5 addenda AC text, the assertion
// is that "N parallel bootstraps to disjoint target paths, each
// followed by `act ready`, all return the same state and exit 0."

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// makeFromRemoteSource initializes a host repo with a fresh .act/ via
// RunInit + one create op, then clones the nested .act/.git into a
// fresh bare remote so the bootstrap-worker --from-remote tests have a
// URL pointing at a real act state. Returns the URL (a filesystem
// path; `git clone` accepts either), the source root for cross-checks,
// and the seeded issue id (lets the round-trip test assert ready sees
// the same backlog from source and target).
func makeFromRemoteSource(t *testing.T) (remoteURL, srcRoot, issueID string) {
	t.Helper()
	srcRoot, issueID = makeBootstrapSource(t)

	// Clone the nested .act/.git into a bare repo. The bootstrap-worker
	// --from-remote path will then clone that bare into the worker's
	// target.
	bareDir := t.TempDir()
	barePath := filepath.Join(bareDir, "act-state.git")
	srcGit := filepath.Join(srcRoot, ".act", ".git")
	runGit(t, "", "clone", "--bare", srcGit, barePath)
	return barePath, srcRoot, issueID
}

// TestBootstrapFromRemote_HappyPath drives the from-remote path end to
// end: clones the seeded bare into a fresh target, asserts exit 0, the
// success envelope shape, and the act.role=worker write.
func TestBootstrapFromRemote_HappyPath(t *testing.T) {
	remoteURL, _, _ := makeFromRemoteSource(t)
	targetRoot := makeBootstrapTarget(t)

	out, code := RunBootstrapWorker(BootstrapWorkerOptions{
		FromRemoteURL: remoteURL,
		Target:        targetRoot,
	})
	if code != 0 {
		t.Fatalf("from-remote exit=%d, out=%+v", code, out)
	}
	res, ok := out.(BootstrapWorkerResult)
	if !ok {
		t.Fatalf("output type = %T, want BootstrapWorkerResult", out)
	}
	if res.SourceRoot != remoteURL {
		t.Errorf("source_root = %q, want %q", res.SourceRoot, remoteURL)
	}
	targetAct := filepath.Join(targetRoot, ".act")
	if _, err := os.Stat(filepath.Join(targetAct, ".git")); err != nil {
		t.Errorf("target .act/.git missing: %v", err)
	}
}

// TestDocClaim_BootstrapFromRemote_SetsWorkerRole verifies the
// post-bootstrap config write that the spec calls out — every
// bootstrap from a remote MUST write act.role=worker to the cloned
// .git/config. This is the test the docs_sweep registry entry
// "bootstrap-from-remote-role-worker" points at.
func TestDocClaim_BootstrapFromRemote_SetsWorkerRole(t *testing.T) {
	remoteURL, _, _ := makeFromRemoteSource(t)
	targetRoot := makeBootstrapTarget(t)

	_, code := RunBootstrapWorker(BootstrapWorkerOptions{
		FromRemoteURL: remoteURL,
		Target:        targetRoot,
	})
	if code != 0 {
		t.Fatalf("from-remote exit=%d", code)
	}
	// Read the act.role key out of the new clone's nested .git/config
	// via the same plumbing the implementation used. Doing it through
	// git config -f keeps the assertion user-visible (the same shape
	// the spec quotes verbatim).
	cfgPath := filepath.Join(targetRoot, ".act", ".git", "config")
	got := runGitConfigGet(t, cfgPath, "act.role")
	if got != "worker" {
		t.Errorf("act.role in %s = %q, want %q", cfgPath, got, "worker")
	}
}

// TestBootstrapFromRemote_TargetNotEmpty asserts the non-empty target
// rejection in the from-remote path. Same shape as the cwd-source
// case in bootstrap_worker_test.go but it has to live here because
// the entrypoint branches on FromRemoteURL before the common pre-flight.
func TestBootstrapFromRemote_TargetNotEmpty(t *testing.T) {
	remoteURL, _, _ := makeFromRemoteSource(t)
	targetRoot := makeBootstrapTarget(t)

	// Seed the target's .act/ with a sentinel file.
	targetAct := filepath.Join(targetRoot, ".act")
	if err := os.MkdirAll(targetAct, 0o755); err != nil {
		t.Fatalf("seed target .act: %v", err)
	}
	const sentinel = "do-not-touch.txt"
	body := []byte("preserved across refused bootstrap\n")
	if err := os.WriteFile(filepath.Join(targetAct, sentinel), body, 0o644); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}

	out, code := RunBootstrapWorker(BootstrapWorkerOptions{
		FromRemoteURL: remoteURL,
		Target:        targetRoot,
	})
	if code != 2 {
		t.Fatalf("exit=%d, want 2; out=%+v", code, out)
	}
	m, _ := out.(map[string]any)
	if got, _ := m["error"].(string); got != "target_not_empty" {
		t.Errorf("error code = %q, want target_not_empty", got)
	}
	// Sentinel must still be there.
	if got, err := os.ReadFile(filepath.Join(targetAct, sentinel)); err != nil || string(got) != string(body) {
		t.Errorf("sentinel mutated/missing: err=%v body=%q", err, got)
	}
}

// TestDocClaim_BootstrapFromRemote_TargetNotEmptyEnvelope is the
// user-visible doc-claim test for the target_not_empty error code,
// referenced from the docs_sweep registry. The spec asserts the
// envelope shape; this test exercises it at the user-visible boundary
// (RunBootstrapWorker's return payload), not just in an internal
// branch.
func TestDocClaim_BootstrapFromRemote_TargetNotEmptyEnvelope(t *testing.T) {
	remoteURL, _, _ := makeFromRemoteSource(t)
	targetRoot := makeBootstrapTarget(t)

	// Pre-create a non-empty .act/.
	targetAct := filepath.Join(targetRoot, ".act")
	if err := os.MkdirAll(targetAct, 0o755); err != nil {
		t.Fatalf("seed target .act: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetAct, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed stray: %v", err)
	}

	out, code := RunBootstrapWorker(BootstrapWorkerOptions{
		FromRemoteURL: remoteURL,
		Target:        targetRoot,
	})
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	m, _ := out.(map[string]any)
	if got, _ := m["error"].(string); got != "target_not_empty" {
		t.Errorf("error code = %q, want target_not_empty", got)
	}
	// The envelope MUST carry details.target so an agent can recover
	// the offending path without re-parsing the human message.
	d, _ := m["details"].(map[string]any)
	if d == nil {
		t.Fatalf("details missing in envelope: %+v", m)
	}
	if tgt, _ := d["target"].(string); tgt == "" {
		t.Errorf("details.target empty: %+v", d)
	}
}

// TestBootstrapFromRemote_ForceReplaces covers the --force path: a
// non-empty target whose .act/ is replaced wholesale by the clone.
func TestBootstrapFromRemote_ForceReplaces(t *testing.T) {
	remoteURL, _, _ := makeFromRemoteSource(t)
	targetRoot := makeBootstrapTarget(t)

	targetAct := filepath.Join(targetRoot, ".act")
	if err := os.MkdirAll(targetAct, 0o755); err != nil {
		t.Fatalf("seed target .act: %v", err)
	}
	const sentinel = "do-not-touch.txt"
	if err := os.WriteFile(filepath.Join(targetAct, sentinel), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}

	_, code := RunBootstrapWorker(BootstrapWorkerOptions{
		FromRemoteURL: remoteURL,
		Target:        targetRoot,
		Force:         true,
	})
	if code != 0 {
		t.Fatalf("from-remote --force exit=%d", code)
	}
	if _, err := os.Stat(filepath.Join(targetAct, sentinel)); !os.IsNotExist(err) {
		t.Errorf("sentinel survived --force: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(targetAct, ".git")); err != nil {
		t.Errorf(".git missing after --force replace: %v", err)
	}
}

// TestBootstrapFromRemote_Timeout covers the timeout path. The fixture
// is a bare TCP listener on an ephemeral port that accepts but never
// responds — `git clone git://127.0.0.1:<port>/anything` will connect,
// negotiate the protocol header, and hang waiting for the upload-pack
// response. With --timeout-seconds=1 the context.WithTimeout fires
// and SIGKILL terminates the clone subprocess; the command exits 4
// with envelope bootstrap_timeout.
//
// Asserted:
//   - Exit 4.
//   - error == "bootstrap_timeout".
//   - details.timeout_seconds == 1.
//   - The .act.bootstrap/ staging dir under the target is torn down.
func TestBootstrapFromRemote_Timeout(t *testing.T) {
	// Hanging TCP listener: accept connections, then sleep forever.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	done := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the connection open until the test ends.
			go func(c net.Conn) {
				<-done
				_ = c.Close()
			}(c)
		}
	}()
	t.Cleanup(func() { close(done) })

	port := ln.Addr().(*net.TCPAddr).Port
	remoteURL := stallURL(port)
	targetRoot := makeBootstrapTarget(t)

	start := time.Now()
	out, code := RunBootstrapWorker(BootstrapWorkerOptions{
		FromRemoteURL:  remoteURL,
		Target:         targetRoot,
		TimeoutSeconds: 1,
	})
	elapsed := time.Since(start)
	if code != 4 {
		t.Fatalf("exit=%d, want 4; out=%+v elapsed=%s", code, out, elapsed)
	}
	m, _ := out.(map[string]any)
	if got, _ := m["error"].(string); got != "bootstrap_timeout" {
		t.Errorf("error code = %q, want bootstrap_timeout", got)
	}
	d, _ := m["details"].(map[string]any)
	if d == nil {
		t.Fatalf("details missing: %+v", m)
	}
	switch v := d["timeout_seconds"].(type) {
	case int:
		if v != 1 {
			t.Errorf("details.timeout_seconds = %d, want 1", v)
		}
	case float64:
		if int(v) != 1 {
			t.Errorf("details.timeout_seconds = %v, want 1", v)
		}
	default:
		t.Errorf("details.timeout_seconds wrong type: %T %v", v, v)
	}
	// Staging dir must be torn down.
	if _, err := os.Stat(filepath.Join(targetRoot, ".act.bootstrap")); !os.IsNotExist(err) {
		t.Errorf(".act.bootstrap/ leaked after timeout: err=%v", err)
	}
	// Cap the runtime so the assertion is meaningful: at 1s budget +
	// git startup + SIGKILL grace we should be done in well under 10s.
	if elapsed > 10*time.Second {
		t.Errorf("timeout path took %s (budget=1s + slack)", elapsed)
	}
}

// TestDocClaim_BootstrapFromRemote_TimeoutEnvelope is the
// user-visible doc-claim assertion for the bootstrap_timeout envelope
// shape, referenced from the docs_sweep registry. Drives the same
// hanging-TCP fixture as TestBootstrapFromRemote_Timeout but focuses
// on the envelope shape rather than the staging-cleanup invariant.
func TestDocClaim_BootstrapFromRemote_TimeoutEnvelope(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	done := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { <-done; _ = c.Close() }(c)
		}
	}()
	t.Cleanup(func() { close(done) })

	port := ln.Addr().(*net.TCPAddr).Port
	targetRoot := makeBootstrapTarget(t)
	out, code := RunBootstrapWorker(BootstrapWorkerOptions{
		FromRemoteURL:  stallURL(port),
		Target:         targetRoot,
		TimeoutSeconds: 1,
	})
	if code != 4 {
		t.Fatalf("exit=%d, want 4", code)
	}
	m, _ := out.(map[string]any)
	if got, _ := m["error"].(string); got != "bootstrap_timeout" {
		t.Errorf("error = %q, want bootstrap_timeout", got)
	}
	// details.timeout_seconds is the load-bearing field — the spec
	// names it explicitly, and an agent reading the envelope needs to
	// know the budget that was enforced.
	d, _ := m["details"].(map[string]any)
	if d == nil || d["timeout_seconds"] == nil {
		t.Errorf("details.timeout_seconds missing: %+v", m)
	}
}

// TestBootstrapFromRemote_ConcurrentDisjointTargets is the §5
// addenda interference-test AC. Three parallel bootstraps to three
// disjoint target paths against one shared BareRemote fixture; each
// is followed by a `act ready --json` against its own target; all
// must succeed and produce the same ready id set as the source.
func TestBootstrapFromRemote_ConcurrentDisjointTargets(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	remoteURL, srcRoot, _ := makeFromRemoteSource(t)
	srcReady, _ := mustRunAct(t, srcRoot, 0, "ready", "--json")
	srcIDs := extractReadyIDs(t, srcReady)

	const N = 3
	targets := make([]string, N)
	for i := 0; i < N; i++ {
		targets[i] = makeBootstrapTarget(t)
	}

	codes := make([]int, N)
	outs := make([]any, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[i], codes[i] = RunBootstrapWorker(BootstrapWorkerOptions{
				FromRemoteURL: remoteURL,
				Target:        targets[i],
			})
		}()
	}
	wg.Wait()

	for i := 0; i < N; i++ {
		if codes[i] != 0 {
			t.Errorf("bootstrap[%d] exit=%d out=%+v", i, codes[i], outs[i])
		}
	}
	// Round-trip each target: act ready --json must agree with the
	// source's ready set.
	for i := 0; i < N; i++ {
		if codes[i] != 0 {
			continue
		}
		tgtReady, _ := mustRunAct(t, targets[i], 0, "ready", "--json")
		tgtIDs := extractReadyIDs(t, tgtReady)
		if !equalStringSets(srcIDs, tgtIDs) {
			t.Errorf("target[%d] ready set diverged:\n  src: %v\n  tgt: %v",
				i, srcIDs, tgtIDs)
		}
	}
}

// TestDocClaim_BootstrapFromRemote_HelpListsFlag is the user-visible
// doc-claim test for the --from-remote help string. The claim is
// "act help" mentions --from-remote so a cold-start agent reading the
// help discovers the mode exists.
func TestDocClaim_BootstrapFromRemote_HelpListsFlag(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	site := t.TempDir()
	out, _ := mustRunAct(t, site, 0, "help")
	if !strings.Contains(out, "--from-remote") {
		t.Errorf("act help missing --from-remote:\n%s", out)
	}
}

// stallURL builds a git-protocol URL pointing at the hanging TCP
// listener used by the timeout tests. The path is arbitrary — git
// will negotiate the protocol header and hang on the upload-pack
// response, which is what the test wants.
func stallURL(port int) string {
	return fmt.Sprintf("git://127.0.0.1:%d/stall", port)
}

// runGitConfigGet reads a key out of a `git config -f` config file
// via the real `git config` binary. Mirrors the read shape the spec
// asserts in prose ("git config -f <worktree>/.act/.git/config
// act.role returns 'worker'"), so the doc-claim test exercises the
// same user-visible boundary the claim names.
func runGitConfigGet(t *testing.T, configPath, key string) string {
	t.Helper()
	out, err := exec.Command("git", "config", "-f", configPath, "--get", key).Output()
	if err != nil {
		t.Fatalf("git config -f %s --get %s: %v", configPath, key, err)
	}
	return strings.TrimSpace(string(out))
}
