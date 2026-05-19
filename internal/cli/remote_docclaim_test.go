package cli

// Doc-claim regression tests for `act remote` (Phase 2 ticket 1a).
//
// Each test pins a user-visible behavior claim made in a doc surface
// (cmd/act/help.go's helpOverview, docs/spec-v2.md) at the boundary an
// agent would actually hit — `act help` stdout for help-text claims,
// the actual git-config file for spec-claimed key values. Internal
// behaviour is covered by remote_test.go; this file is for the
// drift-vs-doc shape that the docs-sweep enforces.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
)

// TestDocClaim_Remote_HelpListsSubcommand asserts that `act help`'s
// subcommands listing names `remote`. The sweep enforces that
// cmd/act/help.go's helpOverview contains the literal token `remote`;
// this test drives the actual binary and checks stdout, so a refactor
// that splits helpOverview into multiple constants still has to keep
// `remote` in the rendered list.
func TestDocClaim_Remote_HelpListsSubcommand(t *testing.T) {
	site := t.TempDir()
	out, _ := mustRunAct(t, site, 0, "help")
	// Pull the Subcommands section and check `remote` appears in it,
	// not just somewhere later in the page.
	const start = "Subcommands:"
	const end = "'act mine'"
	startIdx := strings.Index(out, start)
	endIdx := strings.Index(out, end)
	if startIdx < 0 || endIdx < 0 || endIdx <= startIdx {
		t.Fatalf("could not isolate Subcommands section: start=%d end=%d", startIdx, endIdx)
	}
	section := out[startIdx:endIdx]
	if !strings.Contains(section, "remote") {
		t.Errorf("Subcommands section missing `remote`:\n%s", section)
	}
}

// remoteFixtureForDocClaim is a thin alias-style helper. We don't share
// newRemoteFixture across files (Go test package-scope is fine, but
// keeping a per-purpose helper makes the docclaim file independently
// readable).
func remoteFixtureForDocClaim(t *testing.T) string {
	t.Helper()
	return newRemoteFixture(t)
}

// TestDocClaim_Config_ActRoleOrchestrator asserts the spec claim
// "act.role=orchestrator" at the boundary the spec names: the actual
// `.act/.git/config` file after `act remote enable`.
func TestDocClaim_Config_ActRoleOrchestrator(t *testing.T) {
	host := remoteFixtureForDocClaim(t)
	_, code := mustRunAct(t, host, 0, "remote", "enable", "--json")
	_ = code
	configPath := filepath.Join(host, ".act", ".git", "config")
	cmd := exec.Command("git", "config", "-f", configPath, "--get", config.ActRoleKey)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --get %s: %v", config.ActRoleKey, err)
	}
	got := strings.TrimSpace(string(out))
	if got != "orchestrator" {
		t.Errorf("%s = %q, want %q", config.ActRoleKey, got, "orchestrator")
	}
}

// TestDocClaim_Config_ActRoleDefaultsToWorker asserts the spec's
// "default is `worker`" claim for an unset `act.role` key. The spec
// claim is about parser behaviour for the unset case; the boundary is
// config.ReadRole returning RoleUnknown (which callers treat as worker
// safe-by-default) — pinned here so a refactor of ReadRole that
// silently flipped the default would fail this test.
//
// We register the claim as "default is `worker`" matching the spec
// prose; the assertion verifies the parser-side guarantee that the
// unset case does not parse as orchestrator.
func TestDocClaim_Config_ActRoleDefaultsToWorker(t *testing.T) {
	host := remoteFixtureForDocClaim(t)
	configPath := filepath.Join(host, ".act", ".git", "config")
	role, err := config.ReadRole(configPath)
	if err != nil {
		t.Fatalf("ReadRole: %v", err)
	}
	if role == config.RoleOrchestrator {
		t.Errorf("ReadRole on unset key = orchestrator; spec says default is worker")
	}
}

// TestDocClaim_Remote_EnableSetsReceiveDenyCurrentBranch pins the
// helpOverview claim that `act remote enable` writes
// receive.denyCurrentBranch=updateInstead.
func TestDocClaim_Remote_EnableSetsReceiveDenyCurrentBranch(t *testing.T) {
	host := remoteFixtureForDocClaim(t)
	_, _ = mustRunAct(t, host, 0, "remote", "enable", "--json")
	configPath := filepath.Join(host, ".act", ".git", "config")
	cmd := exec.Command("git", "config", "-f", configPath, "--get", "receive.denyCurrentBranch")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --get receive.denyCurrentBranch: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "updateInstead" {
		t.Errorf("receive.denyCurrentBranch = %q, want %q", got, "updateInstead")
	}
}

// TestDocClaim_Remote_DisableIsIdempotent pins the spec claim
// "act remote disable run twice in succession MUST exit zero both
// times". Drive at the subprocess boundary so a refactor that broke
// idempotency would surface here.
func TestDocClaim_Remote_DisableIsIdempotent(t *testing.T) {
	host := remoteFixtureForDocClaim(t)
	mustRunAct(t, host, 0, "remote", "enable")
	mustRunAct(t, host, 0, "remote", "disable")
	mustRunAct(t, host, 0, "remote", "disable") // second call must also be zero
}

// TestDocClaim_Remote_DisableRemovesHookFile pins the spec claim that
// disable removes the post-receive file (not merely truncates / not
// merely unsets config). The boundary is os.Stat returning IsNotExist.
func TestDocClaim_Remote_DisableRemovesHookFile(t *testing.T) {
	host := remoteFixtureForDocClaim(t)
	mustRunAct(t, host, 0, "remote", "enable")
	hookPath := filepath.Join(host, ".act", ".git", "hooks", "post-receive")
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("hook not present after enable: %v", err)
	}
	mustRunAct(t, host, 0, "remote", "disable")
	_, err := os.Stat(hookPath)
	if err == nil {
		t.Errorf("post-receive hook still present after disable")
	} else if !os.IsNotExist(err) {
		t.Errorf("unexpected stat error: %v", err)
	}
}

// TestDocClaim_Remote_PostReceiveSkeletonNamesTicket pins the §5
// addendum: "The post-receive hook body is intentionally empty until
// ticket 6a lands; do not back-fill in 1a's scope." The skeleton names
// ticket 6a in a comment so an agent reading the file sees who owns
// the body.
func TestDocClaim_Remote_PostReceiveSkeletonNamesTicket(t *testing.T) {
	host := remoteFixtureForDocClaim(t)
	mustRunAct(t, host, 0, "remote", "enable")
	hookPath := filepath.Join(host, ".act", ".git", "hooks", "post-receive")
	body, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	if !strings.Contains(string(body), "ticket 6a") {
		t.Errorf("hook skeleton missing ticket 6a reference:\n%s", body)
	}
}

// TestDocClaim_Remote_EnableOnlyBlocksOnErrorSeverity pins the
// docs/spec-v2.md §"Verification" claim: "MUST run `act doctor` after
// the writes complete and return non-zero if doctor reports any
// error-severity finding". The boundary an orchestrator hits is the
// `act remote enable` exit code when a repo carries warn-only doctor
// state (e.g. historical orphan-close from re-sliced .git history).
// The drift shape: a refactor of runRemoteEnable that goes back to
// blocking on any finding (warns + errors) would reintroduce the
// act-06ef97 bug. Drive at the subprocess boundary so the assertion
// stays anchored where a cold-start agent hits it.
func TestDocClaim_Remote_EnableOnlyBlocksOnErrorSeverity(t *testing.T) {
	host := remoteFixtureForDocClaim(t)
	// Seed a synthetic warn (case-(d) orphan-close): a host commit
	// carrying an `Act-Id:` trailer for an id that doesn't exist in
	// act state. checkOrphanClose surfaces this as Severity=warn.
	// We use the case-(d) shape rather than case-(b) so the seed
	// doesn't touch .act/ops/ — direct op-file writes would trip
	// index-divergence (error-severity) alongside the warn and the
	// assertion would conflate the two paths.
	seedOrphanCloseWarn(t, host, "act-dccafe")
	// The enable must succeed (exit 0) despite the warn. mustRunAct
	// fatals on any other exit code, which IS the assertion we want.
	mustRunAct(t, host, 0, "remote", "enable", "--json")
}
