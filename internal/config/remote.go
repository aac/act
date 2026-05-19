// Package config — git-config keys for the act remote / coordination plane.
//
// Phase 2 ticket 1a (docs/coordination-plane-phase2-plan.md) introduces a
// small set of `act.*` config keys that live in `.act/.git/config` — the
// nested .act repo's own git config file. These keys carry per-repo
// orchestration knobs (timeouts, cache TTLs, drift thresholds) plus the
// load-bearing `act.role` decision that closes v1 open-question #4:
// whether this `.act/` is the canonical-history orchestrator or a
// dispatched worker.
//
// Why nested .git/config (not .act/config.json)?
//
//   - These keys are git-protocol settings (e.g. receive.denyCurrentBranch
//     for push-into-checked-out-branch behavior) plus orchestration knobs
//     that pair naturally with them. Keeping the whole bundle in one
//     place (`.act/.git/config`) means a `git config -f` invocation
//     reads/writes everything; no second file to keep in sync.
//   - The nested .git/config file is per-clone (it's inside the .git
//     directory) so it does not accidentally propagate via pull/push.
//     The orchestrator's role marker stays on the orchestrator's
//     machine; the worker's role marker stays on the worker's machine.
//   - `.act/config.json` is committed and folded; these keys are
//     decidedly NOT folded state — they're host-local knobs.
//
// `act.role` semantics (closes v1 OQ #4):
//
//   - Set to "orchestrator" by `act remote enable` on the canonical
//     history holder.
//   - Set to "worker" by `act bootstrap-worker --from-remote` (Phase 2
//     ticket 7) on every dispatched worker.
//   - Unset on legacy / hand-crafted repos: default is "worker"
//     (safe — workers don't trigger upstream sync). The post-receive
//     hook (ticket 6a) and the post-commit upstream-sync trigger
//     (ticket 6b) read this key to decide whether to fire.
//   - No filesystem-path heuristic. The config key is the only
//     mechanism for role decision.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Role is the orchestrator-vs-worker enum read from `act.role`.
type Role string

const (
	// RoleOrchestrator is the canonical-history holder. Set by
	// `act remote enable`.
	RoleOrchestrator Role = "orchestrator"

	// RoleWorker is a dispatched bootstrap. Set by
	// `act bootstrap-worker --from-remote` (Phase 2 ticket 7).
	RoleWorker Role = "worker"

	// RoleUnknown is the parsed value when the key is unset or contains
	// an unrecognized string. Callers that care about safe-by-default
	// should treat RoleUnknown the same as RoleWorker.
	RoleUnknown Role = ""
)

// Git-config key names. Centralised so `act remote enable`, `act remote
// disable`, and any future readers (post-receive hook, doctor) use the
// same strings.
const (
	// ActRoleKey selects orchestrator vs worker. See Role.
	ActRoleKey = "act.role"

	// ReadCacheTTLSecondsKey is the seconds-of-staleness budget for
	// read-side coordination caches (ticket 2 series).
	ReadCacheTTLSecondsKey = "act.readCacheTTLSeconds"

	// BootstrapTimeoutSecondsKey caps the wall-time budget for the
	// bootstrap protocol (Phase 2 ticket 7).
	BootstrapTimeoutSecondsKey = "act.bootstrapTimeoutSeconds"

	// FetchTimeoutSecondsKey caps the wall-time budget for an
	// upstream `git fetch` (ticket 4 / 5).
	FetchTimeoutSecondsKey = "act.fetchTimeoutSeconds"

	// SlowWriteThresholdMsKey is the per-write latency budget above
	// which a coordination warning fires (ticket 8).
	SlowWriteThresholdMsKey = "act.slowWriteThresholdMs"

	// UpstreamDriftThresholdCommitsKey is the commit-count threshold
	// at which the orchestrator's drift advisory fires.
	UpstreamDriftThresholdCommitsKey = "act.upstreamDriftThresholdCommits"

	// UpstreamDriftThresholdSecondsKey is the wall-time-since-last-sync
	// threshold at which the drift advisory fires.
	UpstreamDriftThresholdSecondsKey = "act.upstreamDriftThresholdSeconds"

	// ReceiveDenyCurrentBranchKey is the upstream git-receive policy.
	// Set to "updateInstead" so workers pushing back to the
	// orchestrator update the checked-out branch in place rather than
	// rejecting.
	ReceiveDenyCurrentBranchKey = "receive.denyCurrentBranch"
)

// PostReceiveHookSkeleton is the legacy/no-op body retained for callers
// (tests, external imports) that referenced it before Phase 2 ticket 6a
// landed. `act remote enable` itself now writes the rendered
// PostReceiveHookBodyTemplate — the real body filled in by 6a and
// rendered with the installing binary's absolute path by act-528547.
// The skeleton is kept exported so the 1a-era
// TestDocClaim_Remote_PostReceiveSkeletonNamesTicket assertion (which
// reads the installed body and checks for the "ticket 6a" marker)
// continues to match — that comment now lives in the rendered template.
const PostReceiveHookSkeleton = "#!/bin/bash\n" +
	"# act post-receive hook (legacy skeleton — superseded by PostReceiveHookBodyTemplate).\n" +
	"#\n" +
	"# Installed by: act remote enable\n" +
	"# Removed by:   act remote disable\n" +
	"exit 0\n"

// PostReceiveHookBodyTemplate is the body template installed at
// `.act/.git/hooks/post-receive` by `act remote enable` (Phase 2 ticket
// 6a). When a worker pushes new ops to the orchestrator's `.act/.git`,
// git invokes this hook on the orchestrator. The hook detaches a
// background `act remote sync` so the just-received ops are republished
// to `origin-upstream` (if configured) without blocking the worker's
// push completion.
//
// Why `nohup ... &` and not a foreground `act remote sync`:
//
//   - The worker is blocked on its push until the hook returns; a
//     foreground sync that hit a slow/unreachable upstream would make
//     worker writes feel slow even though the orchestrator's local
//     state is already durable.
//   - Sync is fail-soft (`act remote sync` exits 0 even when the upstream
//     is unreachable; failures land in `.act/.sync-log`), so backgrounding
//     never silently loses the failure signal.
//   - `nohup` plus `&` is the minimum-dependency detach: no systemd, no
//     launchd, no per-platform spawn helper.
//
// Why a template (not a literal `nohup act remote sync`):
//
//   - A bare `act` in the hook depends on whichever binary PATH resolves
//     to at hook-fire time. If the operator installed a fresh `act` into
//     a new location but PATH still points at a stale earlier binary,
//     the hook silently no-ops on `remote sync` (the verb doesn't exist
//     in the pre-Phase 2 binary). Discovered in act-abbf4b dogfood; see
//     act-528547.
//   - At `act remote enable` time we know the absolute path of the
//     binary running the enable (via os.Executable + EvalSymlinks). We
//     substitute that path into the template via RenderPostReceiveHookBody
//     so the installed hook calls the exact binary that installed it —
//     immune to subsequent PATH changes. Re-running `act remote enable`
//     after a binary move refreshes the hook.
//
// The body deliberately names ticket 6a in a comment so the
// TestDocClaim_Remote_PostReceiveSkeletonNamesTicket assertion (the
// 1a-era guarantee that the file documents who fills in the body) still
// matches after 6a fills the skeleton.
//
// ACT_BIN_PLACEHOLDER is the literal substring substituted at install
// time. The rendered hook line is `nohup <ACT_BIN> remote sync ...` —
// the rendered form replaces the historical `nohup act remote sync`
// invocation with the resolved absolute path. Documenting both forms
// in this comment block keeps the docs-sweep substring anchor stable.
const PostReceiveHookBodyTemplate = "#!/bin/bash\n" +
	"# act post-receive hook (Phase 2 ticket 6a; absolute-path form act-528547).\n" +
	"#\n" +
	"# When a worker pushes new ops into the orchestrator's .act/.git, fire\n" +
	"# `act remote sync` in the background so the just-received ops are\n" +
	"# republished to origin-upstream (if configured). Sync is fail-soft;\n" +
	"# unreachable upstreams land in .act/.sync-log, not in the hook's\n" +
	"# exit code (we don't want a worker push to fail because the\n" +
	"# orchestrator's upstream is down).\n" +
	"#\n" +
	"# The ACT_BIN marker on the invocation line below is replaced at\n" +
	"# `act remote enable` time with the absolute path of the binary\n" +
	"# running the enable so the hook is immune to later PATH staleness\n" +
	"# (act-528547). The historical bare form was an `act remote sync`\n" +
	"# invocation; the rendered form embeds the absolute path instead.\n" +
	"#\n" +
	"# Installed by: act remote enable\n" +
	"# Removed by:   act remote disable\n" +
	"nohup {{ACT_BIN}} remote sync >/dev/null 2>&1 &\n" +
	"exit 0\n"

// ActBinPlaceholder is the literal marker in PostReceiveHookBodyTemplate
// that RenderPostReceiveHookBody replaces with the absolute path of the
// `act` binary at install time.
const ActBinPlaceholder = "{{ACT_BIN}}"

// RenderPostReceiveHookBody substitutes the {{ACT_BIN}} placeholder in
// PostReceiveHookBodyTemplate with actBinPath and returns the resulting
// hook body. actBinPath should be the canonical absolute path of the
// `act` binary that ran `act remote enable` (typically obtained by
// `os.Executable` plus `filepath.EvalSymlinks`).
//
// If actBinPath is empty, the placeholder is replaced with the bare
// literal `act` — the pre-528547 behaviour — as a safe fallback. The
// caller is expected to surface a non-empty path; this fallback only
// guards against a programmer error.
func RenderPostReceiveHookBody(actBinPath string) string {
	bin := actBinPath
	if bin == "" {
		bin = "act"
	}
	return strings.ReplaceAll(PostReceiveHookBodyTemplate, ActBinPlaceholder, bin)
}

// EnableDefaults is the default values written by `act remote enable`
// for each scalar key. The keys themselves are constants above; values
// are kept here so the test suite and the implementation share one
// source of truth.
//
// The values are conservative defaults; per-repo overrides happen via
// plain `git config -f .act/.git/config <key> <value>`.
type EnableDefaults struct {
	ReadCacheTTLSeconds           int
	BootstrapTimeoutSeconds       int
	FetchTimeoutSeconds           int
	SlowWriteThresholdMs          int
	UpstreamDriftThresholdCommits int
	UpstreamDriftThresholdSeconds int
}

// DefaultEnableDefaults returns the canonical default scalar values.
// Edit only with a corresponding update to docs/spec-v2.md.
func DefaultEnableDefaults() EnableDefaults {
	return EnableDefaults{
		ReadCacheTTLSeconds:           5,
		BootstrapTimeoutSeconds:       30,
		FetchTimeoutSeconds:           10,
		SlowWriteThresholdMs:          1000,
		UpstreamDriftThresholdCommits: 50,
		UpstreamDriftThresholdSeconds: 3600,
	}
}

// AllActRoleKeys returns every key `act remote disable` unsets, in a
// stable order (the order matters only for human-readable diff output —
// `git config --unset` is per-key and order-independent on disk).
//
// The order pairs role-and-receive-policy first (the load-bearing
// decisions), followed by scalars from lowest-to-highest defaults so a
// reader can eyeball the list and tell which knob is which.
func AllActRoleKeys() []string {
	return []string{
		ActRoleKey,
		ReceiveDenyCurrentBranchKey,
		ReadCacheTTLSecondsKey,
		BootstrapTimeoutSecondsKey,
		FetchTimeoutSecondsKey,
		SlowWriteThresholdMsKey,
		UpstreamDriftThresholdCommitsKey,
		UpstreamDriftThresholdSecondsKey,
	}
}

// ActGitConfigPath returns the absolute path to the nested-repo's git
// config file under the .act/ tree at actStateRoot. actStateRoot is
// the `.act/` directory itself (Layout(repo).Root), not the host repo
// root.
func ActGitConfigPath(actStateRoot string) string {
	return filepath.Join(actStateRoot, ".git", "config")
}

// ActGitHooksDir returns the absolute path to the hooks directory of
// the nested .act/.git repo at actStateRoot.
func ActGitHooksDir(actStateRoot string) string {
	return filepath.Join(actStateRoot, ".git", "hooks")
}

// PostReceiveHookPath returns the absolute path to the post-receive
// hook file under the nested .act/.git repo at actStateRoot.
func PostReceiveHookPath(actStateRoot string) string {
	return filepath.Join(ActGitHooksDir(actStateRoot), "post-receive")
}

// SetGitConfig writes key=value to configPath using `git config -f
// <configPath> <key> <value>`. The file must already exist (it's
// `.act/.git/config`, created by `git init` of the nested repo).
func SetGitConfig(configPath, key, value string) error {
	if _, err := os.Stat(configPath); err != nil {
		return fmt.Errorf("config: stat %s: %w", configPath, err)
	}
	cmd := exec.Command("git", "config", "-f", configPath, key, value)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("config: git config -f %s %s %s: %w (%s)",
			configPath, key, value, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// UnsetGitConfig removes key from configPath via `git config -f
// <configPath> --unset <key>`. Idempotent: a key that is already absent
// is treated as success (git exits 5 for "not in file", which we
// translate to nil).
func UnsetGitConfig(configPath, key string) error {
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			// No config file → nothing to unset. Idempotent.
			return nil
		}
		return fmt.Errorf("config: stat %s: %w", configPath, err)
	}
	cmd := exec.Command("git", "config", "-f", configPath, "--unset", key)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// git config --unset exits 5 when the key is not set or the
		// section does not exist. That's the idempotent path.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ee.ExitCode() == 5 {
				return nil
			}
		}
		return fmt.Errorf("config: git config -f %s --unset %s: %w (%s)",
			configPath, key, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// GetGitConfig reads key from configPath via `git config -f
// <configPath> --get <key>`. Returns "", nil if the key is not set or
// the config file does not exist. Any other error is surfaced.
func GetGitConfig(configPath, key string) (string, error) {
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("config: stat %s: %w", configPath, err)
	}
	cmd := exec.Command("git", "config", "-f", configPath, "--get", key)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// Exit 1 = key not found in section that does exist.
			// Exit 5 = section/key absent. Both → empty value.
			if ee.ExitCode() == 1 || ee.ExitCode() == 5 {
				return "", nil
			}
		}
		return "", fmt.Errorf("config: git config -f %s --get %s: %w",
			configPath, key, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ReadRole returns the parsed `act.role` value from configPath. Unset
// or unrecognized values map to RoleUnknown; callers that need
// safe-by-default behavior should treat RoleUnknown as equivalent to
// RoleWorker (the post-commit upstream-sync trigger does this).
func ReadRole(configPath string) (Role, error) {
	val, err := GetGitConfig(configPath, ActRoleKey)
	if err != nil {
		return RoleUnknown, err
	}
	switch Role(val) {
	case RoleOrchestrator:
		return RoleOrchestrator, nil
	case RoleWorker:
		return RoleWorker, nil
	default:
		return RoleUnknown, nil
	}
}
