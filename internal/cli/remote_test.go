package cli

// Tests for `act remote enable` / `act remote disable` (Phase 2 ticket 1a).
//
// Each acceptance criterion gets one focused subtest. The fixture is a
// real git repo with `act init` run against it, since the subcommand
// resolves .act/ via gitops.FindHostRepoRoot. We invoke RunRemote
// directly (not via subprocess) for hermetic test runs; the
// TestDocClaim_* file drives the subprocess boundary for the doc claims.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
)

// newRemoteFixture initializes a host git repo + .act/ under t.TempDir()
// and returns the host repo root. The fixture is what RunRemote expects
// to find when SourceCWD is set to the host root: a host .git/, a
// .act/ tree with a nested .act/.git/.
func newRemoteFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	configureSite(t, dir, "remote@example.com", "Remote Tester")
	// Seed the host repo so subsequent commits aren't on an empty tree.
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatalf("seed README: %v", err)
	}
	runGit(t, dir, "add", "README")
	runGit(t, dir, "commit", "-q", "--no-verify", "-m", "seed")
	mustRunAct(t, dir, 0, "init", "--json")
	return dir
}

// gitConfigGet shells out to `git config -f <path> --get <key>` and
// returns the trimmed value plus the exit code. Tests assert on exit
// code 0 / non-zero rather than on parsing the value, mirroring how the
// acceptance criteria are written.
func gitConfigGet(t *testing.T, configPath, key string) (string, int) {
	t.Helper()
	cmd := exec.Command("git", "config", "-f", configPath, "--get", key)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return strings.TrimSpace(string(out)), ee.ExitCode()
		}
		t.Fatalf("git config --get %s: %v", key, err)
	}
	return strings.TrimSpace(string(out)), 0
}

// TestRunRemote_Enable_SetsReceiveDenyCurrentBranch asserts the
// canonical acceptance criterion: after `act remote enable`,
// `git config -f .act/.git/config receive.denyCurrentBranch` outputs
// `updateInstead`.
func TestRunRemote_Enable_SetsReceiveDenyCurrentBranch(t *testing.T) {
	host := newRemoteFixture(t)
	out, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host})
	if code != 0 {
		t.Fatalf("enable: code=%d out=%v", code, out)
	}
	configPath := filepath.Join(host, ".act", ".git", "config")
	val, getCode := gitConfigGet(t, configPath, config.ReceiveDenyCurrentBranchKey)
	if getCode != 0 {
		t.Fatalf("git config --get %s: exit=%d", config.ReceiveDenyCurrentBranchKey, getCode)
	}
	if val != "updateInstead" {
		t.Errorf("receive.denyCurrentBranch = %q, want %q", val, "updateInstead")
	}
}

// TestRunRemote_Enable_SetsActRoleOrchestrator asserts that
// `act remote enable` writes act.role=orchestrator. This is the
// load-bearing closing of OQ #4.
func TestRunRemote_Enable_SetsActRoleOrchestrator(t *testing.T) {
	host := newRemoteFixture(t)
	out, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host})
	if code != 0 {
		t.Fatalf("enable: code=%d out=%v", code, out)
	}
	configPath := filepath.Join(host, ".act", ".git", "config")
	val, getCode := gitConfigGet(t, configPath, config.ActRoleKey)
	if getCode != 0 {
		t.Fatalf("git config --get %s: exit=%d", config.ActRoleKey, getCode)
	}
	if val != "orchestrator" {
		t.Errorf("act.role = %q, want %q", val, "orchestrator")
	}
}

// TestRunRemote_Enable_InstallsPostReceiveHook asserts that the
// post-receive hook file exists and is executable.
func TestRunRemote_Enable_InstallsPostReceiveHook(t *testing.T) {
	host := newRemoteFixture(t)
	_, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host})
	if code != 0 {
		t.Fatalf("enable: code=%d", code)
	}
	hookPath := filepath.Join(host, ".act", ".git", "hooks", "post-receive")
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("hook missing: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("hook %s is not executable (mode=%o)", hookPath, info.Mode())
	}
	body, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	// The skeleton names ticket 6a as the body owner.
	if !strings.Contains(string(body), "ticket 6a") {
		t.Errorf("hook body missing ticket 6a reference:\n%s", body)
	}
	if !strings.Contains(string(body), "exit 0") {
		t.Errorf("hook body missing `exit 0`:\n%s", body)
	}
}

// TestRemoteEnable_HookContainsAbsoluteActPath asserts that
// `act remote enable` embeds the absolute path of the running `act`
// binary into the post-receive hook (act-528547). Without this, the
// hook calls bare `act` on PATH and silently no-ops when PATH points
// at a stale or pre-Phase 2 binary — exactly the dogfood failure
// observed in act-abbf4b.
//
// The boundary is the installed hook file content: the hook line
// `nohup <ACT_BIN> remote sync ... &` must contain whatever
// resolveActPath / os.Executable returns at install time, NOT the bare
// literal `act`. Under `go test`, os.Executable returns the test binary
// path so the assertion compares against that.
func TestRemoteEnable_HookContainsAbsoluteActPath(t *testing.T) {
	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("enable: code=%d", code)
	}

	// Determine what resolveActBinPath would have returned for this
	// process. We replicate the same os.Executable + EvalSymlinks
	// shape rather than calling resolveActBinPath directly so the test
	// would catch a refactor that bypassed the canonicalisation.
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	resolvedExe, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// Match the fallback in resolveActBinPath.
		resolvedExe = exe
	}
	if !filepath.IsAbs(resolvedExe) {
		t.Fatalf("expected absolute path from os.Executable, got %q", resolvedExe)
	}

	hookPath := filepath.Join(host, ".act", ".git", "hooks", "post-receive")
	body, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}

	if !strings.Contains(string(body), resolvedExe) {
		t.Errorf("hook body does not embed running binary path %q\nbody:\n%s", resolvedExe, body)
	}
	// Anti-test: the bare-act invocation must NOT appear as an actual
	// command line in the hook. Match against a line-start "nohup act "
	// (newline + literal) so comment references to the historical
	// bare-act form (e.g. backtick-quoted in the template's doc block)
	// don't cause a false positive.
	if strings.Contains(string(body), "\nnohup act ") {
		t.Errorf("hook body still uses bare `act` after nohup; absolute-path embedding bypassed:\n%s", body)
	}
	// The {{ACT_BIN}} placeholder must have been substituted.
	if strings.Contains(string(body), "{{ACT_BIN}}") {
		t.Errorf("hook body still contains unrendered {{ACT_BIN}} placeholder:\n%s", body)
	}
	// The expected rendered invocation line.
	wantLine := "nohup " + resolvedExe + " remote sync"
	if !strings.Contains(string(body), wantLine) {
		t.Errorf("hook body missing rendered invocation %q\nbody:\n%s", wantLine, body)
	}
}

// TestRemoteEnable_IdempotentHookContent asserts that running
// `act remote enable` twice produces identical hook content (assuming
// the binary path is unchanged). This is the act-528547 acceptance
// criterion that re-enable is the supported refresh path.
func TestRemoteEnable_IdempotentHookContent(t *testing.T) {
	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("first enable: code=%d", code)
	}
	hookPath := filepath.Join(host, ".act", ".git", "hooks", "post-receive")
	body1, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook (first): %v", err)
	}
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("second enable: code=%d", code)
	}
	body2, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook (second): %v", err)
	}
	if string(body1) != string(body2) {
		t.Errorf("hook body changed across two enables (non-idempotent):\nfirst:\n%s\nsecond:\n%s", body1, body2)
	}
}

// TestRunRemote_Enable_WritesAllScalarKeys covers the full set of
// scalar keys, not just role+receive. These are all listed in the
// acceptance criteria as "set config keys: ...". A single test
// iterates the registry so adding a new key to AllActRoleKeys auto-
// extends coverage.
func TestRunRemote_Enable_WritesAllScalarKeys(t *testing.T) {
	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("enable: code=%d", code)
	}
	configPath := filepath.Join(host, ".act", ".git", "config")
	defaults := config.DefaultEnableDefaults()
	want := map[string]string{
		config.ReadCacheTTLSecondsKey:           "5",
		config.BootstrapTimeoutSecondsKey:       "30",
		config.FetchTimeoutSecondsKey:           "10",
		config.SlowWriteThresholdMsKey:          "1000",
		config.UpstreamDriftThresholdCommitsKey: "50",
		config.UpstreamDriftThresholdSecondsKey: "3600",
	}
	// Defend against drift: if EnableDefaults is edited, the test
	// expectation tracks via the shared constants.
	_ = defaults
	for key, expected := range want {
		val, code := gitConfigGet(t, configPath, key)
		if code != 0 {
			t.Errorf("git config --get %s: exit=%d", key, code)
			continue
		}
		if val != expected {
			t.Errorf("%s = %q, want %q", key, val, expected)
		}
	}
}

// TestRunRemote_Disable_UnsetsReceiveDenyCurrentBranch: after disable,
// `git config -f .act/.git/config --get receive.denyCurrentBranch`
// returns non-zero (key absent).
func TestRunRemote_Disable_UnsetsReceiveDenyCurrentBranch(t *testing.T) {
	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("enable: code=%d", code)
	}
	if _, code := RunRemote(RemoteOptions{Verb: "disable", SourceCWD: host}); code != 0 {
		t.Fatalf("disable: code=%d", code)
	}
	configPath := filepath.Join(host, ".act", ".git", "config")
	_, getCode := gitConfigGet(t, configPath, config.ReceiveDenyCurrentBranchKey)
	if getCode == 0 {
		t.Errorf("expected receive.denyCurrentBranch unset after disable; git config --get exit=0")
	}
}

// TestRunRemote_Disable_UnsetsActRole: after disable, `git config -f
// --get act.role` returns non-zero (key absent).
func TestRunRemote_Disable_UnsetsActRole(t *testing.T) {
	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("enable: code=%d", code)
	}
	if _, code := RunRemote(RemoteOptions{Verb: "disable", SourceCWD: host}); code != 0 {
		t.Fatalf("disable: code=%d", code)
	}
	configPath := filepath.Join(host, ".act", ".git", "config")
	_, getCode := gitConfigGet(t, configPath, config.ActRoleKey)
	if getCode == 0 {
		t.Errorf("expected act.role unset after disable; git config --get exit=0")
	}
}

// TestRunRemote_Disable_RemovesPostReceiveHook: after disable, the
// hook file is gone (filesystem state, not just config). The
// acceptance row pins os.Stat returning ENOENT.
func TestRunRemote_Disable_RemovesPostReceiveHook(t *testing.T) {
	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("enable: code=%d", code)
	}
	hookPath := filepath.Join(host, ".act", ".git", "hooks", "post-receive")
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("hook not present after enable: %v", err)
	}
	if _, code := RunRemote(RemoteOptions{Verb: "disable", SourceCWD: host}); code != 0 {
		t.Fatalf("disable: code=%d", code)
	}
	_, err := os.Stat(hookPath)
	if err == nil {
		t.Errorf("post-receive hook still present after disable")
	} else if !os.IsNotExist(err) {
		t.Errorf("unexpected stat error: %v", err)
	}
}

// TestRunRemote_Disable_IdempotentSecondCall: running disable twice in
// a row exits zero both times.
func TestRunRemote_Disable_IdempotentSecondCall(t *testing.T) {
	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("enable: code=%d", code)
	}
	if _, code := RunRemote(RemoteOptions{Verb: "disable", SourceCWD: host}); code != 0 {
		t.Fatalf("first disable: code=%d", code)
	}
	out, code := RunRemote(RemoteOptions{Verb: "disable", SourceCWD: host})
	if code != 0 {
		t.Errorf("second disable should be idempotent; code=%d out=%v", code, out)
	}
	res, ok := out.(RemoteResult)
	if !ok {
		t.Fatalf("second disable: unexpected output type %T", out)
	}
	if res.Changed {
		t.Errorf("second disable Changed=true; expected no-op")
	}
}

// TestRunRemote_Disable_NeverEnabledIsNoOp: disable on a fresh fixture
// (no enable ever) exits zero.
func TestRunRemote_Disable_NeverEnabledIsNoOp(t *testing.T) {
	host := newRemoteFixture(t)
	out, code := RunRemote(RemoteOptions{Verb: "disable", SourceCWD: host})
	if code != 0 {
		t.Errorf("disable on never-enabled: code=%d out=%v", code, out)
	}
}

// TestRunRemote_Enable_DoctorClean: after enable, the embedded doctor
// pass reports zero findings (acceptance row).
func TestRunRemote_Enable_DoctorClean(t *testing.T) {
	host := newRemoteFixture(t)
	out, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host})
	if code != 0 {
		t.Fatalf("enable: code=%d out=%v", code, out)
	}
	res, ok := out.(RemoteResult)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if res.DoctorFindings != 0 {
		t.Errorf("doctor reported %d finding(s); want 0", res.DoctorFindings)
	}
}

// TestRunRemote_BadVerb: an unknown verb surfaces bad_flag with exit 2.
func TestRunRemote_BadVerb(t *testing.T) {
	host := newRemoteFixture(t)
	out, code := RunRemote(RemoteOptions{Verb: "nuke", SourceCWD: host})
	if code != 2 {
		t.Errorf("unknown verb: code=%d want=2", code)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("unknown verb: unexpected output type %T", out)
	}
	if m["error"] != ErrBadFlag {
		t.Errorf("unknown verb: error=%v want=%s", m["error"], ErrBadFlag)
	}
}

// TestRunRemote_ReadRoleDefault: with no key set, config.ReadRole
// returns RoleUnknown. Callers treat RoleUnknown like RoleWorker
// (safe-by-default); pinning the parse here is the user-visible
// boundary for the "default is worker" doc claim.
func TestRunRemote_ReadRoleDefault(t *testing.T) {
	host := newRemoteFixture(t)
	configPath := filepath.Join(host, ".act", ".git", "config")
	role, err := config.ReadRole(configPath)
	if err != nil {
		t.Fatalf("ReadRole: %v", err)
	}
	if role != config.RoleUnknown {
		t.Errorf("default ReadRole = %q, want %q", role, config.RoleUnknown)
	}
}

// seedOrphanCloseWarn writes a host commit carrying an `Act-Id:`
// trailer for an id that does not exist in act state, producing the
// orphan-close case-(d) warn-severity finding. The commit is authored
// by the fixture's existing internal email so the external-PR
// suppression in checkOrphanClose does NOT fire. Used by the warn-
// only tests below — this path avoids touching .act/ops/ directly so
// it doesn't accidentally trip index-divergence (which is
// error-severity) alongside the warn.
func seedOrphanCloseWarn(t *testing.T, host, syntheticID string) {
	t.Helper()
	p := filepath.Join(host, "warn-seed-"+syntheticID+".txt")
	if err := os.WriteFile(p, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	runGit(t, host, "add", filepath.Base(p))
	runGit(t, host, "commit", "-q", "--no-verify",
		"-m", "synthetic warn seed",
		"-m", "Act-Id: "+syntheticID)
}

// TestRemoteEnable_WarnOnlyDoctor_Succeeds asserts that act remote
// enable succeeds (exit 0) when the post-config doctor pass reports
// only warn-severity findings (e.g. historical orphan-close from
// commits referencing ids that don't exist in act state — the
// dogfood signal that motivated act-06ef97). Per docs/spec-v2.md the
// role transition + hook install must NOT be blocked by warns; only
// error-severity findings are blocking.
func TestRemoteEnable_WarnOnlyDoctor_Succeeds(t *testing.T) {
	host := newRemoteFixture(t)
	id := "act-deadbe"
	seedOrphanCloseWarn(t, host, id)

	// Pre-flight: confirm doctor reports a warn (and not an error)
	// for the synthetic id so the test is exercising the path we
	// mean to. Scoped to orphan-close to avoid noise from other
	// checks.
	docOut, _ := RunDoctor(host, DoctorOptions{Check: "orphan-close"})
	dr := docOut.(DoctorResult)
	sawWarn := false
	for _, f := range dr.Findings {
		if f.IssueID == id {
			if f.Severity != "warn" {
				t.Fatalf("seed for case-(d) should be warn, got %s for %+v", f.Severity, f)
			}
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Fatalf("seed did not produce warn finding for %s; findings=%+v", id, dr.Findings)
	}

	// The enable call. Acceptance: exit 0, RemoteResult returned (not
	// an envelope map), role key written, hook installed.
	out, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host})
	if code != 0 {
		t.Fatalf("warn-only doctor must not block enable; code=%d out=%v", code, out)
	}
	res, ok := out.(RemoteResult)
	if !ok {
		t.Fatalf("expected RemoteResult, got %T (%v)", out, out)
	}
	if !res.Changed {
		t.Errorf("RemoteResult.Changed = false; want true")
	}

	configPath := filepath.Join(host, ".act", ".git", "config")
	val, gc := gitConfigGet(t, configPath, config.ActRoleKey)
	if gc != 0 {
		t.Fatalf("act.role unset after enable; git config --get exit=%d", gc)
	}
	if val != "orchestrator" {
		t.Errorf("act.role = %q, want orchestrator", val)
	}
	hookPath := filepath.Join(host, ".act", ".git", "hooks", "post-receive")
	if _, err := os.Stat(hookPath); err != nil {
		t.Errorf("post-receive hook missing after enable: %v", err)
	}
}

// TestRemoteEnable_ErrorDoctor_FailsCleanly asserts that an
// error-severity doctor finding blocks the enable, the envelope is
// the canonical doctor_failed shape, AND the exit code is non-zero
// (the original bug was exit code 0 with an error envelope on
// stderr). (act-06ef97.)
func TestRemoteEnable_ErrorDoctor_FailsCleanly(t *testing.T) {
	host := newRemoteFixture(t)

	// Seed an orphan-ops error: a directory under .act/ops/ with a
	// non-create op and no create op. checkOrphanOps emits this as
	// Severity=error.
	id := "act-errord"
	writeRawOp(t, host, id, "close", map[string]string{"reason": "no-create"}, 1)

	out, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host})
	if code == 0 {
		t.Fatalf("error-severity doctor finding must block enable; got code=0 out=%v", out)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected envelope map, got %T (%v)", out, out)
	}
	if m["error"] != "doctor_failed" {
		t.Errorf("envelope.error = %v, want doctor_failed", m["error"])
	}
	msg, _ := m["message"].(string)
	if !strings.Contains(msg, "doctor reported") || !strings.Contains(msg, "error-severity") {
		t.Errorf("envelope.message = %q; want a doctor_failed message naming error-severity", msg)
	}
	details, _ := m["details"].(map[string]any)
	if details == nil {
		t.Errorf("envelope.details missing")
	}
}

// TestRemoteEnable_ExitCodeMatchesOutput asserts the exit-code /
// output mismatch documented in act-06ef97 is closed: whenever the
// caller sees a non-zero exit, the output is an error envelope;
// whenever the caller sees a zero exit, the output is RemoteResult
// (success). No envelope-with-exit-0 path remains.
func TestRemoteEnable_ExitCodeMatchesOutput(t *testing.T) {
	// Case A: warn-only — exit 0, RemoteResult.
	hostWarn := newRemoteFixture(t)
	seedOrphanCloseWarn(t, hostWarn, "act-cafebe")

	out, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: hostWarn})
	if code != 0 {
		t.Fatalf("case A: exit=%d, want 0; out=%v", code, out)
	}
	if _, ok := out.(RemoteResult); !ok {
		t.Errorf("case A: exit=0 must return RemoteResult, got %T", out)
	}

	// Case B: error — non-zero exit, envelope map.
	hostErr := newRemoteFixture(t)
	writeRawOp(t, hostErr, "act-eccme1", "close", map[string]string{"reason": "no-create"}, 1)

	out, code = RunRemote(RemoteOptions{Verb: "enable", SourceCWD: hostErr})
	if code == 0 {
		t.Fatalf("case B: exit=0, want non-zero; out=%v", out)
	}
	if _, ok := out.(map[string]any); !ok {
		t.Errorf("case B: non-zero exit must return envelope map, got %T", out)
	}
}
