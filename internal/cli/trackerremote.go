package cli

// Tracker-remote detection for a checkout with no local act state (act-a025ab).
//
// `.act/` is gitignored, so a fresh clone of a project carries no tracker —
// and act's no-state guard says so ("this is normal in CI / fresh clones").
// That wording is right for a project with no tracker anywhere, and wrong
// for an operator who keeps each project's tracker in a remote git repo and
// syncs it between machines: there, a checkout without `.act/` (or with an
// `.act/` that has no config.json) is a broken checkout, and "this is normal"
// sends sessions off working against an empty queue.
//
// act cannot discover such a remote on its own — the only place act records
// a remote is inside `.act/.git/config`, which is exactly what is missing.
// So the operator tells act where trackers live, once per machine:
//
//  1. $ACT_TRACKER_REMOTE, if set and non-empty.
//  2. $XDG_CONFIG_HOME/act/tracker-remote (default
//     ~/.config/act/tracker-remote), first non-empty line.
//
// The value is a git URL or path template; `{repo}` is replaced with the
// folder name of the main repository — inside a linked git worktree, the
// repo the worktree belongs to, not the worktree's own folder — and a leading `~/` expands to the home
// directory. Unset (the default) means no probe runs and the no-state
// guard behaves exactly as before.

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// TrackerRemoteEnvVar names the environment override for the tracker-remote
// template.
const TrackerRemoteEnvVar = "ACT_TRACKER_REMOTE"

// TrackerRemoteRepoPlaceholder is replaced with the host repo's basename.
const TrackerRemoteRepoPlaceholder = "{repo}"

// trackerRemoteProbeTimeout bounds the `git ls-remote` probe for non-local
// remotes, so an unreachable host cannot hang a read-only command.
const trackerRemoteProbeTimeout = 10 * time.Second

// TrackerRemote is the result of resolving and probing the configured
// tracker remote for one host repo.
type TrackerRemote struct {
	// Configured is true when a template was found at all.
	Configured bool
	// URL is the template with {repo} and ~/ expanded.
	URL string
	// Source says where the template came from, for messages:
	// "$ACT_TRACKER_REMOTE" or the config file path.
	Source string
	// Found is true when the remote was confirmed to exist.
	Found bool
	// Unconfirmed is set when a non-local remote could not be probed
	// (unreachable, auth failure, or absent — git does not distinguish).
	// It carries the first line of git's error.
	Unconfirmed string
}

// TrackerRemoteConfigPath returns the per-machine tracker-remote file,
// honouring XDG_CONFIG_HOME and falling back to ~/.config/act/tracker-remote.
func TrackerRemoteConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "act", "tracker-remote")
}

// resolveTrackerRemoteTemplate returns the raw template and its source, or
// ("", "") when nothing is configured.
func resolveTrackerRemoteTemplate() (string, string) {
	if v := strings.TrimSpace(os.Getenv(TrackerRemoteEnvVar)); v != "" {
		return v, "$" + TrackerRemoteEnvVar
	}
	path := TrackerRemoteConfigPath()
	if path == "" {
		return "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			return line, path
		}
	}
	return "", ""
}

// isLocalGitPath reports whether url names a filesystem path rather than a
// network remote. file:// URLs count as local.
func isLocalGitPath(url string) (string, bool) {
	if strings.HasPrefix(url, "file://") {
		return strings.TrimPrefix(url, "file://"), true
	}
	if strings.Contains(url, "://") {
		return "", false
	}
	// scp-like "host:path" — a colon before any slash.
	if i := strings.Index(url, ":"); i >= 0 {
		if j := strings.Index(url, "/"); j < 0 || i < j {
			return "", false
		}
	}
	return url, true
}

// trackerRemoteRepoName returns the name `{repo}` expands to for the host
// repo at hostRoot: the folder name of the main repository (act-15ca2b).
//
// In a linked worktree (e.g. <repo>/.claude/worktrees/<name>) the host root
// is the worktree folder, whose name has nothing to do with where the
// tracker lives; git's common dir points back at the main repository's
// .git, whose parent folder is the name the operator's remotes are keyed
// by. Any failure asking git — or a common dir that is not a `.git`
// folder — falls back to hostRoot's basename, which is also the answer for
// an ordinary checkout.
func trackerRemoteRepoName(hostRoot string) string {
	fallback := filepath.Base(hostRoot)
	out, err := exec.Command("git", "-C", hostRoot, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return fallback
	}
	common := strings.TrimSpace(string(out))
	if common == "" {
		return fallback
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(hostRoot, common)
	}
	common = filepath.Clean(common)
	if filepath.Base(common) != ".git" {
		return fallback
	}
	return filepath.Base(filepath.Dir(common))
}

// DetectTrackerRemote resolves the configured tracker remote for the host
// repo at hostRoot and probes whether it exists. It never fails: any problem
// resolving or probing degrades to "not found" (plus Unconfirmed for a
// network remote that could not be reached).
func DetectTrackerRemote(hostRoot string) TrackerRemote {
	tmpl, source := resolveTrackerRemoteTemplate()
	if tmpl == "" {
		return TrackerRemote{}
	}
	url := strings.ReplaceAll(tmpl, TrackerRemoteRepoPlaceholder, trackerRemoteRepoName(hostRoot))
	if strings.HasPrefix(url, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			url = filepath.Join(home, url[2:])
		}
	}
	tr := TrackerRemote{Configured: true, URL: url, Source: source}
	if p, local := isLocalGitPath(url); local {
		// A bare repo has HEAD at its root; a non-bare one under .git/.
		for _, head := range []string{filepath.Join(p, "HEAD"), filepath.Join(p, ".git", "HEAD")} {
			if st, err := os.Stat(head); err == nil && !st.IsDir() {
				tr.Found = true
				break
			}
		}
		return tr
	}
	ctx, cancel := context.WithTimeout(context.Background(), trackerRemoteProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", url)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=5")
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		tr.Found = true
		return tr
	}
	msg := strings.TrimSpace(string(out))
	if i := strings.Index(msg, "\n"); i >= 0 {
		msg = msg[:i]
	}
	if msg == "" {
		msg = err.Error()
	}
	tr.Unconfirmed = msg
	return tr
}
