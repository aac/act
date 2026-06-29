// Package cli — `act remote add-upstream <url>` subcommand (Phase 2
// ticket 1b).
//
// Adds the well-known `origin-upstream` remote to the orchestrator's
// `.act/.git/config` and does an initial `git push origin-upstream
// <branch>` so the upstream mirror is seeded immediately. By default the
// command refuses URLs that match one of the curated "obviously public"
// host patterns in `internal/config/upstream_patterns.go` — agents
// rarely want to publish op-log churn to a public repo. `--force-public`
// overrides the refusal.
//
// Exit-code semantics:
//
//	0 — remote configured and initial push succeeded.
//	2 — public-URL refusal (envelope `upstream_public`), or bad input
//	    (missing URL, --force-public on private URL is fine and ignored).
//	3 — filesystem / git failure (no .act/, config write failed, push
//	    failed for non-public reasons).
//
// The initial push failure is fatal (exit 3) rather than fail-soft: the
// user is configuring an upstream and the verb's whole purpose is to
// publish to it. If the URL is unreachable they want to know
// immediately, not discover it later via a silent `.sync-log` entry.
// `act remote sync` retains the fail-soft semantics; `add-upstream`
// does not.
package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
)

// RemoteAddUpstreamOptions controls `act remote add-upstream`.
type RemoteAddUpstreamOptions struct {
	// URL is the upstream URL (required). Both `https://...` and SSH
	// (`git@host:org/repo.git`) forms are accepted.
	URL string

	// ForcePublic bypasses the curated public-host refusal.
	ForcePublic bool

	// SourceCWD is the directory the host-repo walk starts from. Tests
	// set it explicitly; defaults to os.Getwd().
	SourceCWD string

	// AsJSON is plumbed for parity with other commands.
	AsJSON bool
}

// RemoteAddUpstreamResult is the success payload.
type RemoteAddUpstreamResult struct {
	// ActStateRoot is the absolute `.act/` directory the command
	// operated on.
	ActStateRoot string `json:"act_state_root"`

	// ConfigPath is the absolute `.act/.git/config` file.
	ConfigPath string `json:"config_path"`

	// URL is the URL written to remote.origin-upstream.url.
	URL string `json:"url"`

	// Branch is the branch name that the initial push targeted (usually
	// "main"). Empty if the push step was skipped.
	Branch string `json:"branch"`

	// ForcePublic echoes the option for audit visibility.
	ForcePublic bool `json:"force_public"`
}

// RunRemoteAddUpstream is the package-public entry point. Returns a
// JSON-encodable value (RemoteAddUpstreamResult on success, error-
// envelope map on failure) plus an exit code per the universal table.
func RunRemoteAddUpstream(opts RemoteAddUpstreamOptions) (any, int) {
	if strings.TrimSpace(opts.URL) == "" {
		return map[string]any{
			"error":   ErrBadFlag,
			"message": "act remote add-upstream: missing URL; usage: act remote add-upstream <url> [--force-public]",
		}, 2
	}

	// Public-URL refusal. Happens BEFORE we touch any state so a
	// refused invocation has no side effects.
	if config.IsPublicURL(opts.URL) && !opts.ForcePublic {
		return map[string]any{
			"error":   ErrUpstreamPublic,
			"message": "refusing public upstream; pass --force-public to override",
			"details": map[string]any{
				"url": opts.URL,
			},
		}, 2
	}

	// Resolve the .act/ via the standard host-repo walk.
	srcStart := opts.SourceCWD
	if srcStart == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return map[string]any{
				"error":   ErrNoRepo,
				"message": fmt.Sprintf("act remote add-upstream: getcwd: %v", err),
			}, 3
		}
		srcStart = cwd
	}
	hostRoot, err := gitops.FindHostRepoRoot(srcStart)
	if err != nil {
		if errors.Is(err, gitops.ErrNoHostRepo) {
			return map[string]any{
				"error":   ErrNotInGit,
				"message": fmt.Sprintf("act remote add-upstream: %v", err),
			}, 3
		}
		return map[string]any{
			"error":   ErrNoRepo,
			"message": fmt.Sprintf("act remote add-upstream: resolve host repo: %v", err),
		}, 3
	}
	actRoot, err := gitops.FindActStatePath(hostRoot)
	if err != nil {
		return map[string]any{
			"error":   ErrActNotInitialized,
			"message": fmt.Sprintf("act remote add-upstream: %s has no .act/ — run `act init` first", hostRoot),
		}, 3
	}
	configPath := config.ActGitConfigPath(actRoot)
	if _, err := os.Stat(configPath); err != nil {
		return map[string]any{
			"error": ErrActNotInitialized,
			"message": fmt.Sprintf(
				"act remote add-upstream: %s missing — nested .act/.git not initialized (run `act init` or `act remote enable`)",
				configPath),
		}, 3
	}

	// Write the URL + the canonical refspec. We mirror what
	// `git remote add` would have produced if we'd shelled out (we
	// don't, because `git remote add` requires a working tree and we
	// only have the bare config file).
	if err := config.SetGitConfig(configPath, "remote."+UpstreamRemoteName+".url", opts.URL); err != nil {
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf("act remote add-upstream: %v", err),
		}, 3
	}
	if err := config.SetGitConfig(configPath, "remote."+UpstreamRemoteName+".fetch",
		"+refs/heads/*:refs/remotes/"+UpstreamRemoteName+"/*"); err != nil {
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf("act remote add-upstream: %v", err),
		}, 3
	}

	// Initial push. Resolve the branch via HEAD on the nested .act/.git;
	// fall back to "main" if HEAD is unset or detached.
	gitDir := filepath.Join(actRoot, ".git")
	branch := resolveAddUpstreamBranch(gitDir)
	if err := gitPushInitialUpstream(gitDir, branch); err != nil {
		// Don't roll back the config write. The user can re-run
		// add-upstream (it's idempotent at the config level) or run
		// `act remote sync` once the URL is reachable. The exit-3
		// surfaces the failure so the agent knows to act on it.
		return map[string]any{
			"error":   ErrPushFailed,
			"message": fmt.Sprintf("act remote add-upstream: initial push failed: %v", err),
			"details": map[string]any{
				"url":    opts.URL,
				"branch": branch,
			},
		}, 3
	}

	return RemoteAddUpstreamResult{
		ActStateRoot: actRoot,
		ConfigPath:   configPath,
		URL:          opts.URL,
		Branch:       branch,
		ForcePublic:  opts.ForcePublic,
	}, 0
}

// resolveAddUpstreamBranch reads HEAD on the nested .act/.git and
// returns the branch portion of the symbolic ref. Falls back to "main"
// when HEAD is unset or detached.
func resolveAddUpstreamBranch(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "main"
	}
	s := strings.TrimSpace(string(data))
	const prefix = "ref: "
	if !strings.HasPrefix(s, prefix) {
		return "main"
	}
	ref := strings.TrimSpace(strings.TrimPrefix(s, prefix))
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if branch == "" {
		return "main"
	}
	return branch
}

// gitPushInitialUpstream runs `git --git-dir=<gitDir> push origin-upstream
// <branch>`. The combined output is folded into the returned error
// (capped to a reasonable tail) so callers can surface a meaningful
// diagnostic without re-running.
func gitPushInitialUpstream(gitDir, branch string) error {
	cmd := exec.Command("git", "--git-dir="+gitDir, "push", UpstreamRemoteName, branch)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		out := strings.TrimSpace(buf.String())
		if out == "" {
			return err
		}
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}
