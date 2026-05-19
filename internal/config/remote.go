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

// PostReceiveHookSkeleton is the body installed at
// `.act/.git/hooks/post-receive` by `act remote enable`. Phase 2 ticket
// 6a fills in the real body; until then this is a documented no-op so
// the file exists, is executable, and the orchestrator's `git receive`
// invocations don't fail on a missing hook.
//
// The skeleton names ticket 6a explicitly so a future agent reading the
// file knows what fills in the gap.
const PostReceiveHookSkeleton = "#!/bin/bash\n" +
	"# act post-receive hook (Phase 2 skeleton, filled in by ticket 6a).\n" +
	"#\n" +
	"# This is the orchestrator's receive-side trigger for the coordination\n" +
	"# plane: when a worker pushes new ops to .act/.git, ticket 6a's body\n" +
	"# folds the new ops into the index and re-publishes the orchestrator's\n" +
	"# state. Until 6a lands, this hook is a documented no-op.\n" +
	"#\n" +
	"# Installed by: act remote enable\n" +
	"# Removed by:   act remote disable\n" +
	"exit 0\n"

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
