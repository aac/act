// Package cli — `act remote enable` / `act remote disable` subcommands.
//
// Phase 2 ticket 1a (docs/coordination-plane-phase2-plan.md). These
// subcommands toggle the small set of git-config keys that wire the
// nested `.act/.git` repo into the coordination plane: receive
// behaviour, scalar timeouts/thresholds, and the load-bearing
// `act.role` key that closes v1 open-question #4.
//
// Surface:
//
//	act remote enable [--json]
//	act remote disable [--json]
//
// Behavior is deliberately minimal: enable writes a fixed set of keys
// (see config.AllActRoleKeys), installs a post-receive hook skeleton
// (filled in by ticket 6a), and runs `act doctor` post-config so a
// finding never sneaks in past the enable. Disable unsets the keys and
// removes the hook file. Both are idempotent on the never-enabled-yet
// repo.
//
// What's NOT in scope here: anything beyond local config writes. The
// post-commit upstream-sync trigger (6b), the worker bootstrap from a
// remote URL (7), and the actual receive-side fold (6a body) are
// separate tickets; this is just the schema + skeleton.
package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
)

// actBinEnvOverride is the env-var name that, when set non-empty,
// short-circuits resolveActBinPath and is used verbatim as the absolute
// act binary path embedded into the post-receive hook. This is the test
// seam used by TestRemoteSync_PostReceiveHookFiresBackgroundSync and
// friends: under `go test`, os.Executable returns the test binary, not
// a real `act` binary, so the end-to-end "hook fires → sync runs" path
// has to point the hook at a real prebuilt act via this env var.
//
// Production callers do not need to set this; bare `act remote enable`
// uses os.Executable as usual. The name is namespaced under `ACT_` to
// avoid collision with anything in the operator's shell.
const actBinEnvOverride = "ACT_BIN_OVERRIDE"

// resolveActBinPath returns the canonical absolute path of the running
// `act` binary, used to embed an absolute invocation in the post-receive
// hook body so the hook is immune to later PATH staleness (act-528547).
//
// We call os.Executable to get the path of the running process and then
// filepath.EvalSymlinks to canonicalize any symlinks (a common shape
// when the binary lives under e.g. $GOBIN/act-vX symlinked from
// $GOBIN/act, or when the operator manages multiple versions via a
// wrapper). If EvalSymlinks fails we fall back to the raw executable
// path — better an unresolved-but-absolute path than no path at all.
//
// The ACT_BIN_OVERRIDE env var, when set, short-circuits both lookups
// and is used verbatim. This is the test-seam (see actBinEnvOverride)
// used by hook-fires-end-to-end tests; production callers do not set
// it.
//
// Under `go test` without the override, os.Executable returns the test
// binary's absolute path (e.g. /tmp/go-build.../cli.test). Unit tests
// that only assert on hook-body content (not end-to-end hook execution)
// run fine in that mode; they compare against whatever
// resolveActBinPath returns at call time rather than hardcoding any
// specific path shape.
func resolveActBinPath() (string, error) {
	if override := os.Getenv(actBinEnvOverride); override != "" {
		return override, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve act binary path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// Fall back to the unresolved path. The hook still gets an
		// absolute path; canonicalisation through symlinks is a nice-
		// to-have, not a correctness requirement.
		return exe, nil
	}
	return resolved, nil
}

// RemoteOptions controls `act remote <verb>`.
type RemoteOptions struct {
	// Verb is "enable" or "disable". The CLI front-end parses
	// the positional and sets this; RunRemote rejects anything else.
	Verb string

	// SourceCWD is the directory the host-repo walk starts from.
	// Tests set it explicitly; defaults to os.Getwd().
	SourceCWD string

	// AsJSON is plumbed for parity with other commands.
	AsJSON bool
}

// RemoteResult is the success payload.
type RemoteResult struct {
	// Verb echoes RemoteOptions.Verb.
	Verb string `json:"verb"`

	// ActStateRoot is the absolute `.act/` directory the command
	// operated on.
	ActStateRoot string `json:"act_state_root"`

	// ConfigPath is the absolute `.act/.git/config` file.
	ConfigPath string `json:"config_path"`

	// HookPath is the absolute `.act/.git/hooks/post-receive` path.
	// For `enable` this file now exists; for `disable` it has been
	// removed.
	HookPath string `json:"hook_path"`

	// Changed is true when this invocation actually wrote/removed
	// state. `enable` always sets it true; `disable` sets it true
	// when at least one key was unset or the hook file was removed.
	Changed bool `json:"changed"`

	// DoctorFindings is the count of findings reported by doctor on
	// the post-enable verification pass. Always zero for the disable
	// path. Surfaces in the JSON for parity but the acceptance
	// criterion is that this equals zero on enable.
	DoctorFindings int `json:"doctor_findings"`
}

// RunRemote is the package-public entry point. Returns a
// JSON-encodable value (RemoteResult on success, error-envelope map
// on failure) plus an exit code per the universal table:
//
//	0 success
//	2 bad input (unknown verb, no .act/ found)
//	3 filesystem or git-config failure mid-write
func RunRemote(opts RemoteOptions) (any, int) {
	switch opts.Verb {
	case "enable", "disable":
		// OK
	case "":
		return map[string]any{
			"error":   ErrBadFlag,
			"message": "act remote: usage: act remote <enable|disable> [--json]",
		}, 2
	default:
		return map[string]any{
			"error":   ErrBadFlag,
			"message": fmt.Sprintf("act remote: unknown verb %q; want enable|disable", opts.Verb),
		}, 2
	}

	// Resolve the .act/ via the standard host-repo walk.
	srcStart := opts.SourceCWD
	if srcStart == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return map[string]any{
				"error":   ErrNoRepo,
				"message": fmt.Sprintf("act remote: getcwd: %v", err),
			}, 3
		}
		srcStart = cwd
	}
	hostRoot, err := gitops.FindHostRepoRoot(srcStart)
	if err != nil {
		if errors.Is(err, gitops.ErrNoHostRepo) {
			return map[string]any{
				"error":   ErrNotInGit,
				"message": fmt.Sprintf("act remote: %v", err),
			}, 3
		}
		return map[string]any{
			"error":   ErrNoRepo,
			"message": fmt.Sprintf("act remote: resolve host repo: %v", err),
		}, 3
	}
	actRoot, err := gitops.FindActStatePath(hostRoot)
	if err != nil {
		return map[string]any{
			"error":   ErrActNotInitialized,
			"message": fmt.Sprintf("act remote: %s has no .act/ — run `act init` first", hostRoot),
		}, 3
	}
	configPath := config.ActGitConfigPath(actRoot)
	hookPath := config.PostReceiveHookPath(actRoot)

	switch opts.Verb {
	case "enable":
		return runRemoteEnable(actRoot, hostRoot, configPath, hookPath)
	case "disable":
		return runRemoteDisable(actRoot, configPath, hookPath)
	}
	// unreachable
	return nil, 1
}

// runRemoteEnable writes the canonical key set, installs the hook
// skeleton, and runs doctor as a verification pass.
func runRemoteEnable(actRoot, hostRoot, configPath, hookPath string) (any, int) {
	// The nested .git/config file must already exist — `act init`
	// creates the nested repo and therefore the config file. If it's
	// missing, the repo is in an inconsistent state; surface as
	// act_not_initialized.
	if _, err := os.Stat(configPath); err != nil {
		return map[string]any{
			"error": ErrActNotInitialized,
			"message": fmt.Sprintf(
				"act remote enable: %s missing — nested .act/.git not initialized (run `act init` or `act migrate-to-nested`)",
				configPath),
		}, 3
	}

	defaults := config.DefaultEnableDefaults()

	// Each (key, value) pair we write. Order matches AllActRoleKeys
	// for human-readable diff parity.
	writes := []struct {
		key, val string
	}{
		{config.ActRoleKey, string(config.RoleOrchestrator)},
		{config.ReceiveDenyCurrentBranchKey, "updateInstead"},
		{config.ReadCacheTTLSecondsKey, remoteItoa(defaults.ReadCacheTTLSeconds)},
		{config.BootstrapTimeoutSecondsKey, remoteItoa(defaults.BootstrapTimeoutSeconds)},
		{config.FetchTimeoutSecondsKey, remoteItoa(defaults.FetchTimeoutSeconds)},
		{config.SlowWriteThresholdMsKey, remoteItoa(defaults.SlowWriteThresholdMs)},
		{config.UpstreamDriftThresholdCommitsKey, remoteItoa(defaults.UpstreamDriftThresholdCommits)},
		{config.UpstreamDriftThresholdSecondsKey, remoteItoa(defaults.UpstreamDriftThresholdSeconds)},
	}
	for _, w := range writes {
		if err := config.SetGitConfig(configPath, w.key, w.val); err != nil {
			return map[string]any{
				"error":   ErrWriteFailed,
				"message": fmt.Sprintf("act remote enable: %v", err),
			}, 3
		}
	}

	// Install the post-receive hook skeleton. The body is a no-op
	// until ticket 6a; we still write the file so the orchestrator's
	// `git receive-pack` invocations don't error on a missing hook.
	if err := os.MkdirAll(config.ActGitHooksDir(actRoot), 0o755); err != nil {
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf("act remote enable: mkdir hooks dir: %v", err),
		}, 3
	}
	// Resolve the absolute path of the running `act` binary so the
	// hook body embeds it directly. Without this, the hook calls bare
	// `act` and silently no-ops on `remote sync` if PATH later points
	// at a stale or pre-Phase 2 binary (the original act-528547 dogfood
	// discovery from act-abbf4b). Re-running `act remote enable` after
	// a binary move refreshes the hook.
	actBinPath, err := resolveActBinPath()
	if err != nil {
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf("act remote enable: %v", err),
		}, 3
	}
	hookBody := config.RenderPostReceiveHookBody(actBinPath)
	if err := os.WriteFile(hookPath, []byte(hookBody), 0o755); err != nil {
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf("act remote enable: write %s: %v", hookPath, err),
		}, 3
	}
	// WriteFile honors umask; force the exec bits explicitly so the
	// hook is invokable regardless of the caller's umask.
	if err := os.Chmod(hookPath, 0o755); err != nil {
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf("act remote enable: chmod %s: %v", hookPath, err),
		}, 3
	}

	// Doctor verification. We treat error-severity findings as
	// blocking but treat warn-severity findings (e.g. orphan-close
	// case-(d) from historical commits that reference ids not in the
	// current act state) as informational — they don't block the role
	// transition that just landed. This closes act-06ef97: any repo
	// with historical orphan-close warns (i.e. virtually every real
	// act repo) previously saw a confusing `doctor_failed` envelope
	// here even though the role key + hook were already written.
	//
	// We inspect the findings list ourselves rather than trusting the
	// doctor exit code so the block decision is anchored at the
	// user-visible severity, decoupled from doctor's exit-code policy
	// (e.g. the phase2 case-(g) exit-4 path that promotes beyond 1).
	doctorOut, _ := RunDoctor(hostRoot, DoctorOptions{})
	dr, _ := doctorOut.(DoctorResult)
	errorFindings := 0
	for _, f := range dr.Findings {
		if f.Severity == "error" {
			errorFindings++
		}
	}
	if errorFindings > 0 {
		return map[string]any{
			"error":   "doctor_failed",
			"message": fmt.Sprintf("act remote enable: doctor reported %d error-severity finding(s)", errorFindings),
			"details": map[string]any{
				"doctor_findings":       dr.Count,
				"doctor_error_findings": errorFindings,
			},
		}, 1
	}

	return RemoteResult{
		Verb:           "enable",
		ActStateRoot:   actRoot,
		ConfigPath:     configPath,
		HookPath:       hookPath,
		Changed:        true,
		DoctorFindings: dr.Count,
	}, 0
}

// runRemoteDisable unsets every key in AllActRoleKeys and removes the
// post-receive hook file. Idempotent: keys/files that aren't there are
// not errors.
func runRemoteDisable(actRoot, configPath, hookPath string) (any, int) {
	changed := false
	for _, key := range config.AllActRoleKeys() {
		// Pre-check: if the key is set, we'll count this as a change.
		// Cheap; reads are local file I/O.
		val, err := config.GetGitConfig(configPath, key)
		if err != nil {
			return map[string]any{
				"error":   ErrWriteFailed,
				"message": fmt.Sprintf("act remote disable: read %s: %v", key, err),
			}, 3
		}
		if err := config.UnsetGitConfig(configPath, key); err != nil {
			return map[string]any{
				"error":   ErrWriteFailed,
				"message": fmt.Sprintf("act remote disable: %v", err),
			}, 3
		}
		if val != "" {
			changed = true
		}
	}

	// Remove the post-receive hook file. The acceptance criterion is
	// the FILE is gone, not just the config keys. os.Remove returns
	// fs.ErrNotExist when the file is absent — idempotent.
	if _, err := os.Stat(hookPath); err == nil {
		if err := os.Remove(hookPath); err != nil {
			return map[string]any{
				"error":   ErrWriteFailed,
				"message": fmt.Sprintf("act remote disable: remove %s: %v", hookPath, err),
			}, 3
		}
		changed = true
	} else if !os.IsNotExist(err) {
		return map[string]any{
			"error":   ErrStatFailed,
			"message": fmt.Sprintf("act remote disable: stat %s: %v", hookPath, err),
		}, 3
	}

	return RemoteResult{
		Verb:           "disable",
		ActStateRoot:   actRoot,
		ConfigPath:     configPath,
		HookPath:       hookPath,
		Changed:        changed,
		DoctorFindings: 0,
	}, 0
}

// remoteItoa is a tiny base-10 int formatter local to this file. The
// cli package's concurrent_test.go ships its own `itoa` so we
// disambiguate by prefix.
func remoteItoa(n int) string {
	return fmt.Sprintf("%d", n)
}
