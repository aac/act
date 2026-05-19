// Package cli — `act remote sync` subcommand (Phase 2 ticket 6a).
//
// Pushes the orchestrator's `.act/.git` to its `origin-upstream` remote
// if one is configured. Fail-soft on push failure: the verb still exits
// zero, but appends one JSON-line entry to `.act/.sync-log` so the
// failure is auditable.
//
// Exit-code semantics:
//
//	0 — push succeeded, OR push failed and a line was appended to
//	    `.act/.sync-log` (the "silently-handled" path).
//	2 — configuration error: no `origin-upstream` remote configured.
//
// The single non-zero exit is the user-actionable one — the agent is
// expected to run `act remote add-upstream <url>` first. Everything
// else (DNS down, auth missing, upstream unreachable) is treated as
// "log it and move on" so the post-receive hook chain (which invokes
// sync in the background) never propagates noise to the worker that
// pushed.
//
// `.act/.sync-log` shape: append-only JSON-lines, capped at 100 entries
// with the same pruning shape `.slow-writes` will use when ticket 8
// lands. Each record carries at minimum `timestamp`, `reason`, and
// `error` keys; the file is JSON-lines so a future reader can
// `head -n 100` over it without parsing the file as a single document.
package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
)

// SyncLogFilename is the path component (under `.act/`) where sync
// failures are appended. Exported for tests that want to assert on
// the file directly without re-deriving the path.
const SyncLogFilename = ".sync-log"

// SyncLogMaxEntries caps the JSON-lines file length. Older entries are
// dropped (LRU-by-position) when a write would push the count past the
// cap. Matches the shape `.slow-writes` will use when ticket 8 lands.
const SyncLogMaxEntries = 100

// UpstreamRemoteName is the well-known second-remote name on the
// orchestrator's `.act/.git`. Set by `act remote add-upstream <url>`
// (Phase 2 ticket 6b series); read by `act remote sync` here.
const UpstreamRemoteName = "origin-upstream"

// RemoteSyncOptions controls `act remote sync`.
type RemoteSyncOptions struct {
	// SourceCWD is the directory the host-repo walk starts from. Tests
	// set it explicitly; defaults to os.Getwd().
	SourceCWD string

	// AsJSON is plumbed for parity with other commands.
	AsJSON bool
}

// RemoteSyncResult is the success payload.
type RemoteSyncResult struct {
	// ActStateRoot is the absolute `.act/` directory the command
	// operated on.
	ActStateRoot string `json:"act_state_root"`

	// Pushed is true when this invocation produced a successful upstream
	// push (origin-upstream advanced). False on the no-op idempotent
	// path and on the fail-soft logged path.
	Pushed bool `json:"pushed"`

	// Logged is true when this invocation appended an entry to the
	// sync log. Mutually exclusive with Pushed in the same invocation:
	// a successful push does not log; a failed push always logs.
	Logged bool `json:"logged"`

	// SyncLogPath is the absolute `.act/.sync-log` file.
	SyncLogPath string `json:"sync_log_path"`

	// Reason carries the human-readable reason on Logged=true. Empty
	// otherwise.
	Reason string `json:"reason,omitempty"`
}

// SyncLogEntry is one JSON-lines record in `.act/.sync-log`.
//
// Field order in the encoded JSON is fixed by struct declaration order
// so an agent reading the file can rely on `reason` being the first
// key (the acceptance criterion in ticket 6a names "first JSON field
// is `reason`").
type SyncLogEntry struct {
	// Reason is a short slug. Currently the only emitted value is
	// "unreachable" (the upstream-failed-to-push path). Future values
	// (e.g. "auth_required", "diverged") may be added without breaking
	// existing readers.
	Reason string `json:"reason"`

	// Timestamp is the RFC3339Nano time the failure was recorded.
	Timestamp string `json:"timestamp"`

	// Error is the trimmed combined-output of the failing git push
	// (capped at 4096 bytes so a hostile upstream can't blow up the
	// file). Empty if the failure was non-IO.
	Error string `json:"error"`
}

// RunRemoteSync is the package-public entry point. Returns a
// JSON-encodable value (RemoteSyncResult on success, error-envelope map
// on failure) plus an exit code per the universal table:
//
//	0 success (or fail-soft handled)
//	2 upstream not configured
//	3 filesystem / git-resolution failure (e.g. `.act/` missing)
func RunRemoteSync(opts RemoteSyncOptions) (any, int) {
	// Resolve the .act/ via the standard host-repo walk.
	srcStart := opts.SourceCWD
	if srcStart == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return map[string]any{
				"error":   ErrNoRepo,
				"message": fmt.Sprintf("act remote sync: getcwd: %v", err),
			}, 3
		}
		srcStart = cwd
	}
	hostRoot, err := gitops.FindHostRepoRoot(srcStart)
	if err != nil {
		if errors.Is(err, gitops.ErrNoHostRepo) {
			return map[string]any{
				"error":   ErrNotInGit,
				"message": fmt.Sprintf("act remote sync: %v", err),
			}, 3
		}
		return map[string]any{
			"error":   ErrNoRepo,
			"message": fmt.Sprintf("act remote sync: resolve host repo: %v", err),
		}, 3
	}
	actRoot, err := gitops.FindActStatePath(hostRoot)
	if err != nil {
		return map[string]any{
			"error":   ErrActNotInitialized,
			"message": fmt.Sprintf("act remote sync: %s has no .act/ — run `act init` first", hostRoot),
		}, 3
	}
	configPath := config.ActGitConfigPath(actRoot)
	syncLogPath := filepath.Join(actRoot, SyncLogFilename)

	// Configuration check: origin-upstream MUST be configured. The
	// literal stderr is pinned by the doc-claim sweep.
	url, err := config.GetGitConfig(configPath, upstreamURLKey())
	if err != nil {
		return map[string]any{
			"error":   ErrConfigReadFailed,
			"message": fmt.Sprintf("act remote sync: read upstream url: %v", err),
		}, 3
	}
	if url == "" {
		return map[string]any{
			"error":   ErrUpstreamNotConfigured,
			"message": "no origin-upstream configured; run 'act remote add-upstream <url>'",
		}, 2
	}

	// Resolve `origin`'s current ref so we can short-circuit when
	// upstream is already at that ref. The acceptance criterion is
	// "idempotent: no-op if origin-upstream ref matches origin". We
	// read the orchestrator's `.act/.git` HEAD itself — `.act/.git`
	// IS the origin from the worker's perspective.
	gitDir := filepath.Join(actRoot, ".git")
	headRef, headRefErr := readGitHEAD(gitDir)
	if headRefErr == nil && headRef != "" {
		upstreamRef, _ := readRemoteRef(gitDir, UpstreamRemoteName, headRef)
		localRef, _ := readLocalBranchRef(gitDir, headRef)
		if upstreamRef != "" && upstreamRef == localRef {
			// Idempotent no-op: upstream is at our local ref.
			return RemoteSyncResult{
				ActStateRoot: actRoot,
				Pushed:       false,
				Logged:       false,
				SyncLogPath:  syncLogPath,
			}, 0
		}
	}

	// Perform the push. We push the current branch (resolved from
	// HEAD if available, else "main") to `origin-upstream`. Failure
	// is the fail-soft path: log and exit 0.
	branch := strings.TrimPrefix(headRef, "refs/heads/")
	if branch == "" {
		branch = "main"
	}
	pushErr := gitPushUpstream(gitDir, branch)
	if pushErr != nil {
		// Fail-soft: append to .sync-log, exit 0.
		entry := SyncLogEntry{
			Reason:    "unreachable",
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Error:     truncateForLog(pushErr.Error(), 4096),
		}
		if appendErr := appendSyncLog(syncLogPath, entry); appendErr != nil {
			// We failed to LOG the failure. This IS user-actionable:
			// surface it as a filesystem error so the agent can
			// diagnose. Exit 3 since the sync itself didn't succeed
			// AND we couldn't even record the failure.
			return map[string]any{
				"error":   ErrWriteFailed,
				"message": fmt.Sprintf("act remote sync: push failed (%v) AND log append failed: %v", pushErr, appendErr),
			}, 3
		}
		return RemoteSyncResult{
			ActStateRoot: actRoot,
			Pushed:       false,
			Logged:       true,
			SyncLogPath:  syncLogPath,
			Reason:       "unreachable",
		}, 0
	}

	return RemoteSyncResult{
		ActStateRoot: actRoot,
		Pushed:       true,
		Logged:       false,
		SyncLogPath:  syncLogPath,
	}, 0
}

// upstreamURLKey returns the git-config key that holds
// `origin-upstream`'s URL. Centralised so callers (sync, doctor) share
// the same string.
func upstreamURLKey() string {
	return "remote." + UpstreamRemoteName + ".url"
}

// readGitHEAD returns the symbolic ref HEAD points at (e.g.
// "refs/heads/main"). Returns "" with no error when HEAD is detached
// — sync still proceeds in that case, defaulting to the "main" branch.
func readGitHEAD(gitDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(data))
	const prefix = "ref: "
	if !strings.HasPrefix(s, prefix) {
		// Detached HEAD; no symbolic ref to return.
		return "", nil
	}
	return strings.TrimSpace(strings.TrimPrefix(s, prefix)), nil
}

// readRemoteRef returns the SHA stored at `refs/remotes/<remote>/<branch>`
// inside gitDir. Returns "" with no error if the ref doesn't exist.
// Reads packed-refs as well as loose refs.
func readRemoteRef(gitDir, remote, ref string) (string, error) {
	// ref looks like "refs/heads/main"; we want "refs/remotes/<remote>/main".
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if branch == "" {
		branch = "main"
	}
	target := "refs/remotes/" + remote + "/" + branch

	// Loose ref first.
	if data, err := os.ReadFile(filepath.Join(gitDir, target)); err == nil {
		return strings.TrimSpace(string(data)), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	// Fall back to packed-refs.
	return readPackedRef(gitDir, target)
}

// readLocalBranchRef returns the SHA at `refs/heads/<branch>` (the
// orchestrator's own local branch — what would be pushed to upstream).
func readLocalBranchRef(gitDir, ref string) (string, error) {
	if data, err := os.ReadFile(filepath.Join(gitDir, ref)); err == nil {
		return strings.TrimSpace(string(data)), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return readPackedRef(gitDir, ref)
}

// readPackedRef parses `packed-refs` and returns the SHA recorded for
// target (e.g. "refs/heads/main"). Returns "" with no error if the ref
// is not packed.
func readPackedRef(gitDir, target string) (string, error) {
	data, err := os.ReadFile(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == target {
			return fields[0], nil
		}
	}
	return "", nil
}

// gitPushUpstream runs `git --git-dir=<gitDir> push origin-upstream
// <branch>`. The combined output is folded into the returned error
// so the fail-soft log captures the diagnostic.
func gitPushUpstream(gitDir, branch string) error {
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

// appendSyncLog appends a single JSON-lines record to syncLogPath and
// prunes the file to SyncLogMaxEntries newest entries. The prune is
// a simple read-truncate-rewrite — fine for a 100-entry cap.
func appendSyncLog(syncLogPath string, entry SyncLogEntry) error {
	// Marshal first; if we can't marshal we don't touch the file.
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal sync-log entry: %w", err)
	}

	// Ensure parent dir exists (always does in practice — caller has
	// already resolved actRoot — but guard anyway).
	if err := os.MkdirAll(filepath.Dir(syncLogPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(syncLogPath), err)
	}

	// Read existing lines (if any) to drive the prune.
	var existing []string
	if data, rerr := os.ReadFile(syncLogPath); rerr == nil {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		// Allow long lines — 4096-byte error tails plus envelope chrome
		// can exceed the default 64KB scanner buffer in pathological
		// cases. Cap the scanner at 1MB so an unbounded file (someone
		// manually appended garbage) doesn't OOM the binary.
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			s := scanner.Text()
			if strings.TrimSpace(s) == "" {
				continue
			}
			existing = append(existing, s)
		}
	} else if !os.IsNotExist(rerr) {
		return fmt.Errorf("read sync-log: %w", rerr)
	}

	existing = append(existing, string(line))
	if len(existing) > SyncLogMaxEntries {
		existing = existing[len(existing)-SyncLogMaxEntries:]
	}

	// Atomic-ish rewrite: write to a temp file then rename. The
	// pruning shape matches what `.slow-writes` will use when ticket 8
	// lands; sharing the helper later (when ticket 8 introduces its
	// canonical version) is a fine refactor.
	tmp := syncLogPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	w := bufio.NewWriter(f)
	for _, s := range existing {
		if _, err := w.WriteString(s); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("write sync-log: %w", err)
		}
		if _, err := w.WriteString("\n"); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("write sync-log: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("flush sync-log: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close sync-log: %w", err)
	}
	if err := os.Rename(tmp, syncLogPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename sync-log: %w", err)
	}
	return nil
}

// truncateForLog returns s capped at n bytes, dropping the leading
// excess (keeps the most-recent tail of long error messages).
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
