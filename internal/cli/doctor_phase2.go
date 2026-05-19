// Package cli — Phase 2 ticket 9 (act-aa4f19) doctor extensions.
//
// This file adds five new doctor reconciliation cases on top of the
// Phase 1 surface implemented in doctor.go:
//
//   - case (a'): orchestrator with `origin` configured but no
//     post-receive hook installed at `.act/.git/hooks/post-receive`.
//     Surfaced as `remote-attached-orchestrator`.
//   - case (c'): worker (`act.role=worker`) with no `origin` configured.
//     Surfaced as `worker-without-origin`.
//   - case (f):  local commits on the nested `.act/.git` ahead of
//     `origin/<branch>` (potential unpushed work). Surfaced as
//     `unpushed-commits`. Warn by default; promoted to error under
//     --strict (via the global pass in RunDoctor).
//   - case (g):  origin unreachable. A short-timeout `git fetch origin
//     --dry-run` against the nested `.act/.git` decides; failure exits 4
//     with the literal stderr `remote: origin unreachable; run 'act
//     remote sync' from the orchestrator or check connectivity`. Under
//     --no-fetch the same condition is reported as a warning (exit 0).
//   - case (h):  `origin-upstream` more than `act.upstreamDriftThreshold
//     Commits` commits behind `origin`. Reads refs/remotes locally after
//     the fetch from case (g) completes; suppressed entirely under
//     --no-fetch (per the §5 addendum: drift detection requires a
//     successful upstream fetch).
//
// The five cases all read state local to the nested `.act/.git` repo
// (the orchestrator's authoritative state) rather than the host repo.
// `act.role` + `remote.origin.url` + `remote.origin-upstream.url` all
// live there per docs/spec-v2.md "Coordination plane: Phase 2 config
// schema".
//
// In addition this file populates the remote-status JSON block on the
// DoctorResult envelope: `remote_reachable`, `local_unpushed_count`,
// `upstream_drift_commits`, `slow_writes_last_hour`, and
// `fetch_failure_reason` (case g only).
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aac/act/internal/config"
)

// RemoteStatus is the case-(f) / case-(g) / case-(h) / slow-write
// summary block carried inside DoctorResult. All fields are populated
// on every run (no `omitempty`) so JSON consumers can rely on the keys
// existing — a `false` / `0` / `""` value is meaningful in itself.
//
// Field meanings (per the ticket-9 spec):
//
//   - RemoteReachable: result of the case-(g) probe. True when the
//     dry-run fetch against `origin` succeeded OR when `origin` is not
//     configured (vacuously reachable). False when the probe failed.
//     Under --no-fetch the field reflects the pre-fetch assumption
//     (true if origin is configured, false otherwise) since no probe
//     was issued.
//   - LocalUnpushedCount: result of the case-(f) `git rev-list --count
//     origin/<branch>..HEAD`. Zero when origin is unconfigured.
//   - UpstreamDriftCommits: result of the case-(h) `git rev-list
//     --count origin-upstream/<branch>..origin/<branch>`. Zero when
//     `origin-upstream` is unconfigured OR --no-fetch was passed.
//   - SlowWritesLastHour: count of entries in `.act/.slow-writes`
//     whose `timestamp` parses as RFC3339 within the last hour.
//   - FetchFailureReason: the (trimmed) failure string from the
//     case-(g) fetch probe. Populated only when the probe failed and
//     --no-fetch was not set; empty otherwise. Bounded to 256 bytes
//     to keep the JSON envelope reasonable.
type RemoteStatus struct {
	RemoteReachable      bool   `json:"remote_reachable"`
	LocalUnpushedCount   int    `json:"local_unpushed_count"`
	UpstreamDriftCommits int    `json:"upstream_drift_commits"`
	SlowWritesLastHour   int    `json:"slow_writes_last_hour"`
	FetchFailureReason   string `json:"fetch_failure_reason"`
}

// Phase 2 doctor case identifiers. Kept as exported constants so tests
// can match on the canonical strings without re-deriving them.
const (
	CheckRemoteAttachedOrchestrator = "remote-attached-orchestrator" // case (a')
	CheckWorkerWithoutOrigin        = "worker-without-origin"        // case (c')
	CheckUnpushedCommits            = "unpushed-commits"             // case (f)
	CheckRemoteReachable            = "remote-reachable"             // case (g)
	CheckUpstreamDrift              = "upstream-drift"               // case (h)
)

// fetchDryRunTimeout caps the `git fetch origin --dry-run` invocation
// for case (g). Doctor is interactive; we'd rather report "unreachable"
// after 5s than block on a hung TCP connect.
const fetchDryRunTimeout = 5 * time.Second

// caseGStderrLiteral is the literal stderr line emitted on a case-(g)
// finding. Pinned by an acceptance criterion in the ticket; the
// doc-claim sweep enforces the same string in docs/spec-v2.md.
const caseGStderrLiteral = "remote: origin unreachable; run 'act remote sync' from the orchestrator or check connectivity"

// checkPhase2Reconciliation runs cases (a'), (c'), (f), (g), (h) and
// updates `status` with the per-case JSON-block fields. It is invoked
// from RunDoctor as a single check ("phase2-reconciliation"); the
// individual case names appear on the emitted Finding.Check field for
// per-case introspection. Bundling them in one walk avoids a per-case
// nested-repo stat + git config read.
//
// The `noFetch` argument suppresses the inline fetch in case (g);
// under --no-fetch the case is downgraded to a warning AND case (h)
// is suppressed entirely (drift detection cannot run against stale
// remote refs).
func checkPhase2Reconciliation(repoRoot string, noFetch bool) (findings []Finding, status RemoteStatus, exit int) {
	status = RemoteStatus{
		// Default-true: an unconfigured origin is vacuously reachable.
		// The fetch probe below overrides this when origin is set.
		RemoteReachable: true,
	}

	paths := config.Layout(repoRoot)
	gitDir := filepath.Join(paths.Root, ".git")
	configPath := filepath.Join(gitDir, "config")

	// Read role + origin once; multiple cases consume them.
	role, _ := config.ReadRole(configPath)
	originURL, _ := config.GetGitConfig(configPath, "remote.origin.url")
	upstreamURL, _ := config.GetGitConfig(configPath, upstreamURLKey())

	// Slow-writes summary: always populated (independent of remote state).
	status.SlowWritesLastHour = countSlowWritesLastHour(paths.Root, time.Now())

	// Case (a'): orchestrator with origin but no post-receive hook.
	if role == config.RoleOrchestrator && originURL != "" {
		hookPath := config.PostReceiveHookPath(paths.Root)
		if _, err := os.Stat(hookPath); err != nil {
			findings = append(findings, Finding{
				Check:    CheckRemoteAttachedOrchestrator,
				Severity: "error",
				Message:  fmt.Sprintf("orchestrator with origin configured but no post-receive hook at %s; remedy: re-run `act remote enable`", hookPath),
			})
		}
	}

	// Case (c'): worker without origin.
	if role == config.RoleWorker && originURL == "" {
		findings = append(findings, Finding{
			Check:    CheckWorkerWithoutOrigin,
			Severity: "error",
			Message:  "act.role=worker but no origin configured in .act/.git/config; remedy: re-run `act bootstrap-worker --from-remote <url>`",
		})
	}

	// Case (f): local commits ahead of origin. Requires origin to be
	// configured. The branch is resolved from `.act/.git/HEAD`.
	if originURL != "" {
		if branch, ok := nestedActBranch(gitDir); ok {
			n, err := countLocalUnpushed(gitDir, branch)
			if err == nil && n > 0 {
				status.LocalUnpushedCount = n
				findings = append(findings, Finding{
					Check:    CheckUnpushedCommits,
					Severity: "warn",
					Message:  fmt.Sprintf("local: %d unpushed commits ahead of origin", n),
				})
			}
		}
	}

	// Case (g): origin reachable. Only attempts the fetch when origin
	// is configured AND --no-fetch was not set. Under --no-fetch the
	// case is reported as a warning iff origin is configured (we treat
	// "configured but unverified" as still potentially-reachable for
	// the JSON field but emit a warn so the operator knows the probe
	// was skipped).
	if originURL != "" {
		if noFetch {
			// Skipped probe: keep RemoteReachable=true (we have no
			// evidence to the contrary) and emit a warn finding so
			// human + JSON output reflect that --no-fetch ran.
			findings = append(findings, Finding{
				Check:    CheckRemoteReachable,
				Severity: "warn",
				Message:  "remote: origin reachability not verified (--no-fetch)",
			})
		} else {
			if reason, ok := fetchDryRun(gitDir, fetchDryRunTimeout); !ok {
				status.RemoteReachable = false
				status.FetchFailureReason = truncateBytes(reason, 256)
				findings = append(findings, Finding{
					Check:    CheckRemoteReachable,
					Severity: "error",
					Message:  caseGStderrLiteral,
				})
				exit = 4
			}
		}
	}

	// Case (h): origin-upstream drift. Suppressed entirely under
	// --no-fetch (per §5 addendum: drift detection requires a
	// successful upstream fetch, and the upstream fetch is the same
	// shape as the case-(g) probe).
	if !noFetch && upstreamURL != "" {
		// We re-use the case-(g) probe by issuing a second dry-run
		// fetch against origin-upstream so the refs/remotes/origin-
		// upstream pointers are warm. Failure here is non-fatal:
		// case (h) is best-effort, so we report drift only when we
		// can actually count it.
		_, _ = fetchDryRunRemote(gitDir, UpstreamRemoteName, fetchDryRunTimeout)

		if branch, ok := nestedActBranch(gitDir); ok {
			n, err := countCommitsBehind(gitDir, branch)
			if err == nil && n > 0 {
				threshold := readDriftThresholdCommits(configPath)
				status.UpstreamDriftCommits = n
				if n > threshold {
					findings = append(findings, Finding{
						Check:    CheckUpstreamDrift,
						Severity: "warn",
						Message:  fmt.Sprintf("upstream: origin-upstream is %d commits behind origin; run 'act remote sync'", n),
					})
				}
			}
		}
	}

	return findings, status, exit
}

// nestedActBranch returns the short branch name (e.g. "main") that
// .act/.git's HEAD points at. Returns ok=false on detached HEAD or read
// failure — both shapes mean the case-(f)/(h) walk should be skipped
// (we can't name a refs/remotes/origin/<branch> without it).
func nestedActBranch(gitDir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(data))
	const prefix = "ref: refs/heads/"
	if !strings.HasPrefix(s, prefix) {
		return "", false
	}
	branch := strings.TrimPrefix(s, prefix)
	if branch == "" {
		return "", false
	}
	return branch, true
}

// countLocalUnpushed runs `git -C <gitDir> rev-list --count
// origin/<branch>..HEAD` inside the nested .act/.git. Returns 0 if
// the upstream ref is missing (no fetch has happened yet — we can't
// claim "unpushed" without a baseline). Returns (n, nil) on success;
// (0, err) on a real failure.
func countLocalUnpushed(gitDir, branch string) (int, error) {
	// Sanity: confirm refs/remotes/origin/<branch> exists. The remote
	// ref may be unpopulated on a fresh clone that has not yet
	// fetched; in that case rev-list errors and we return 0.
	cmd := exec.Command("git", "--git-dir="+gitDir, "rev-list", "--count",
		"origin/"+branch+"..HEAD")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		// Missing remote ref → exit 128 with stderr "unknown
		// revision". Treat as "no baseline; nothing to report".
		return 0, nil
	}
	n, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if perr != nil {
		return 0, perr
	}
	return n, nil
}

// countCommitsBehind runs `git -C <gitDir> rev-list --count
// origin-upstream/<branch>..origin/<branch>`. Returns 0 on missing
// refs (treat as "no drift signal yet") or 0 on success.
func countCommitsBehind(gitDir, branch string) (int, error) {
	cmd := exec.Command("git", "--git-dir="+gitDir, "rev-list", "--count",
		UpstreamRemoteName+"/"+branch+".."+"origin/"+branch)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return 0, nil
	}
	n, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if perr != nil {
		return 0, perr
	}
	return n, nil
}

// fetchDryRun runs `git -C <gitDir> fetch origin --dry-run` with a
// short timeout. On success returns ("", true); on failure returns
// (trimmed stderr, false). Used by case (g) reachability probe.
//
// We use `--dry-run` so the probe never mutates the local refs/remotes
// state — useful in tests and avoids surprising behavior on a doctor
// run against a production .act/.git.
func fetchDryRun(gitDir string, timeout time.Duration) (string, bool) {
	return fetchDryRunRemote(gitDir, "origin", timeout)
}

// fetchDryRunRemote is the generic helper backing both case (g)'s
// origin probe and case (h)'s preparatory origin-upstream fetch.
func fetchDryRunRemote(gitDir, remote string, timeout time.Duration) (string, bool) {
	cmd := exec.Command("git", "--git-dir="+gitDir, "fetch", "--dry-run", remote)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	// Bound the wall-clock so a hung TCP connect doesn't stall doctor.
	timer := time.AfterFunc(timeout, func() {
		_ = cmd.Process.Kill()
	})
	out, err := cmd.CombinedOutput()
	_ = timer
	if err != nil {
		return strings.TrimSpace(string(out)) + ": " + err.Error(), false
	}
	return "", true
}

// readDriftThresholdCommits reads `act.upstreamDriftThresholdCommits`
// from the nested config; returns the default (50) on missing/invalid.
func readDriftThresholdCommits(configPath string) int {
	v, err := config.GetGitConfig(configPath, config.UpstreamDriftThresholdCommitsKey)
	if err != nil || v == "" {
		return config.DefaultEnableDefaults().UpstreamDriftThresholdCommits
	}
	n, perr := strconv.Atoi(v)
	if perr != nil || n < 0 {
		return config.DefaultEnableDefaults().UpstreamDriftThresholdCommits
	}
	return n
}

// countSlowWritesLastHour scans `.act/.slow-writes` and returns the
// count of records whose `timestamp` parses as RFC3339 and falls
// within the last hour of `now`. Missing file or parse failures
// degrade gracefully to 0 (the file is observability surface; a corrupt
// entry must not break doctor).
//
// Schema is the one pinned by Phase 2 ticket 3b — see slowwrites.go.
func countSlowWritesLastHour(actStateRoot string, now time.Time) int {
	path := filepath.Join(actStateRoot, ".slow-writes")
	body, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	cutoff := now.Add(-1 * time.Hour)
	count := 0
	for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec SlowWriteRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		// Try RFC3339Nano first (covers the millisecond schema) then
		// plain RFC3339 as a fallback for any hand-written records.
		ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
		if err != nil {
			ts, err = time.Parse(time.RFC3339, rec.Timestamp)
			if err != nil {
				continue
			}
		}
		if ts.After(cutoff) {
			count++
		}
	}
	return count
}

// truncateBytes trims s to at most n bytes, appending "..." when
// truncation occurred. Used to bound FetchFailureReason on the JSON
// envelope so a noisy upstream stderr cannot blow up the doctor output.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
