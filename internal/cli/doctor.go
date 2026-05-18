// Package cli — `act doctor` consistency checks.
//
// Implements act-40ae per spec-v2 §"act doctor". The doctor walks the on-disk
// op log and the SQLite index, surfaces drift, and offers safe auto-remediation
// for the two index-related findings (index-divergence, index-schema).
package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/index"
	"github.com/aac/act/internal/op"
)

// DoctorOptions captures the flag knobs for `act doctor`.
type DoctorOptions struct {
	// Check restricts the run to a single named check; empty runs all
	// eight in spec order.
	Check string
	// Fix enables auto-remediation for the two index checks.
	Fix bool
	// AsJSON toggles the JSON output envelope.
	AsJSON bool
	// Compact triggers manual compaction (delegated to compact package
	// when available; here a no-op warn-only finding).
	Compact bool
	// Strict promotes all `warn` findings to `error` severity (and
	// therefore exit 1). Use in CI to catch regressions that the
	// interactive review step tolerates. Per Phase 1 reconcile-lite
	// (docs/coordination-plane-design.md "Doctor reconciliation",
	// act-37f7).
	Strict bool
}

// Finding is a single doctor finding.
type Finding struct {
	Check    string `json:"check"`
	Severity string `json:"severity"` // "error" | "warn"
	IssueID  string `json:"issue_id,omitempty"`
	Message  string `json:"message"`
}

// DoctorResult is the JSON shape returned by RunDoctor on a successful walk.
type DoctorResult struct {
	Findings []Finding `json:"findings"`
	Count    int       `json:"count"`
}

// DoctorErrorOutput is the structured failure envelope (e.g. missing .act/).
type DoctorErrorOutput struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// allChecks is the fixed run order. Determinism matters for stable JSON output.
var allChecks = []string{
	"orphan-close",
	"orphan-ops",
	"dangling-deps",
	"time-travel",
	"cycle",
	"unknown-op-version",
	"index-divergence",
	"index-schema",
	"gitignore-effective",
}

// validChecks indexes allChecks for O(1) name validation.
var validChecks = func() map[string]bool {
	m := make(map[string]bool, len(allChecks))
	for _, c := range allChecks {
		m[c] = true
	}
	return m
}()

// RunDoctor implements `act doctor`. It returns either a DoctorResult or a
// DoctorErrorOutput plus an exit code.
//
// Exit codes:
//   - 0: no error-severity findings (warnings are acceptable)
//   - 1: at least one error-severity finding
//   - 3: missing .act/ (or repo root)
func RunDoctor(repoRoot string, opts DoctorOptions) (output any, exitCode int) {
	paths := config.Layout(repoRoot)
	if _, err := os.Stat(paths.Root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DoctorErrorOutput{
				Error:   "act_not_initialized",
				Message: fmt.Sprintf("act doctor: %s/.act not initialized; run `act init` first", repoRoot),
			}, 3
		}
		return DoctorErrorOutput{
			Error:   "act_not_initialized",
			Message: fmt.Sprintf("act doctor: stat %s: %v", paths.Root, err),
		}, 3
	}

	// Choose the check set.
	var run []string
	if opts.Check == "" {
		run = allChecks
	} else {
		if !validChecks[opts.Check] {
			return DoctorErrorOutput{
				Error:   "bad_flag",
				Message: fmt.Sprintf("act doctor: --check %q: unknown", opts.Check),
			}, 2
		}
		run = []string{opts.Check}
	}

	// Pre-fold the ops once; most checks consume the same view.
	var foldRes *fold.FoldResult
	if foldNeeded(run) {
		fr, err := fold.Fold(paths.Ops, fold.ApplyDispatch)
		if err != nil {
			// A fold error is itself a doctor finding (op-log corrupt).
			return DoctorResult{
				Findings: []Finding{{
					Check:    "orphan-ops",
					Severity: "error",
					Message:  fmt.Sprintf("fold error: %v", err),
				}},
				Count: 1,
			}, 1
		}
		foldRes = fr
	}

	var findings []Finding
	for _, name := range run {
		switch name {
		case "orphan-close":
			findings = append(findings, checkOrphanClose(repoRoot, foldRes)...)
		case "orphan-ops":
			findings = append(findings, checkOrphanOps(paths.Ops)...)
		case "dangling-deps":
			findings = append(findings, checkDanglingDeps(foldRes)...)
		case "time-travel":
			findings = append(findings, checkTimeTravel(paths.Ops)...)
		case "cycle":
			findings = append(findings, checkCycle(foldRes)...)
		case "unknown-op-version":
			findings = append(findings, checkUnknownOpVersion(paths.Ops)...)
		case "index-divergence":
			findings = append(findings, checkIndexDivergence(paths, opts.Fix)...)
		case "index-schema":
			findings = append(findings, checkIndexSchema(paths, opts.Fix)...)
		case "gitignore-effective":
			findings = append(findings, checkGitignoreEffective(repoRoot)...)
		}
	}

	// Strict mode: promote warn → error before computing exit code. We do
	// this in a second pass (rather than at emission time) so each check
	// can reason about its own severity in isolation; --strict is a
	// presentation policy, not a check-level decision.
	if opts.Strict {
		for i := range findings {
			if findings[i].Severity == "warn" {
				findings[i].Severity = "error"
			}
		}
	}

	exit := 0
	for _, f := range findings {
		if f.Severity == "error" {
			exit = 1
			break
		}
	}
	return DoctorResult{Findings: findings, Count: len(findings)}, exit
}

// foldNeeded reports whether any of the requested checks consumes a fold.
func foldNeeded(run []string) bool {
	for _, n := range run {
		switch n {
		case "orphan-close", "dangling-deps", "cycle":
			return true
		}
	}
	return false
}

// checkOrphanClose implements Phase 1's "doctor reconcile-lite" table
// (docs/coordination-plane-design.md "Doctor reconciliation", act-37f7).
// Despite the historic name, this check covers cases (a), (b), and (d)
// of the table — they share the same two indexes (markers + act state)
// and folding them into one walk avoids re-scanning git log per case.
// Cases (c) and (e) are inherently ignored — multiple markers on the
// same id and claims newer than the latest code commit are normal
// states, not anomalies, and don't need explicit handling.
//
// Case (a): marker in code with no matching issue in act state. Warn.
// Suppressed under the external-PR heuristic (commit author not in the
// host repo's recent contributor set) since fork PRs cite their own
// act ids and aren't expected to reconcile here.
//
// Case (b): closed issue in act state with no closing marker anywhere
// (host log + nested act log both empty). Warn. Suppressed when the
// issue is `type=tracking` (no-code-by-design) or the close op carried
// `no_code=true` (legitimate no-code close — wrong-claim retraction,
// doc correction, obsoleted issue).
//
// Case (d): marker references an id that doesn't exist in act state
// (typo, deleted issue, cross-repo reference). Warn. Suppressed under
// the same external-PR heuristic as (a) — fork PRs commonly cite ids
// from their own act state.
//
// Marker forms (post-act-c4c5):
//   - Trailer form `Act-Id: act-<hex>` in the commit body — the only
//     emission shape going forward.
//   - Historical subject-line form `(act-<hex>)` — still matched so
//     pre-migration history in existing repos resolves cleanly. The
//     nested .act/ repo's op-commits also carry this form.
//
// Implementation: HostGitOps.AllMarkers scans the host log once,
// returning (sha, subject, author_email, issue_id). We do the same
// against the nested act repo. From those two indexes plus the fold,
// each case becomes a set-difference check.
//
// --strict promotion is a global pass in RunDoctor; this function emits
// the design-spec severity unchanged.
func checkOrphanClose(repoRoot string, fr *fold.FoldResult) []Finding {
	if fr == nil {
		return nil
	}
	host := gitops.NewHostGitOps(repoRoot)
	nestedActPath := filepath.Join(repoRoot, ".act")
	var nestedHost *gitops.HostGitOps
	if _, err := os.Stat(filepath.Join(nestedActPath, ".git")); err == nil {
		nestedHost = gitops.NewHostGitOps(nestedActPath)
	}

	// Step 1: build the marker indexes for host log and nested log.
	hostMarkers, _ := host.AllMarkers()
	// Index host markers by the canonical short id (`act-<MinShortHex>`)
	// since act state ids are emitted at MinShortHexLen but historic
	// markers may carry shorter or longer hex tails. We index by both
	// the as-seen id and the truncated-to-MinShortHexLen short id so
	// either lookup direction works.
	hostMarkerByID := map[string][]gitops.MarkerCommit{}
	for _, m := range hostMarkers {
		hostMarkerByID[m.IssueID] = append(hostMarkerByID[m.IssueID], m)
	}
	nestedMarkerByID := map[string][]gitops.MarkerCommit{}
	if nestedHost != nil {
		nestedMarkers, _ := nestedHost.AllMarkers()
		for _, m := range nestedMarkers {
			nestedMarkerByID[m.IssueID] = append(nestedMarkerByID[m.IssueID], m)
		}
	}

	// Step 2: internal contributors for the external-PR suppression.
	// An empty set (fresh repo with no commits) collapses to "every
	// commit is external," which would silence all (a)/(d) warnings —
	// that's the right call for a brand-new repo with no history to
	// distinguish from. The doctor is liberal in what it accepts.
	internal, _ := host.InternalContributors(50)

	// Step 3: build the act-state id-space. We compare against this for
	// case (a) and (d) directly.
	known := map[string]*fold.IssueState{}
	for id, st := range fr.Issues {
		if st != nil && !st.Tombstoned {
			known[id] = st
		}
	}

	var findings []Finding

	// Case (b): closed issues in act state with no closing marker.
	for _, id := range sortedIssueIDs(fr.Issues) {
		st := known[id]
		if st == nil {
			continue
		}
		status, _ := st.Fields["status"].(string)
		if status != "closed" {
			continue
		}
		// Suppress: type=tracking is no-code-by-design.
		if t, _ := st.Fields["type"].(string); t == "tracking" {
			continue
		}
		// Suppress: explicit no_code close marker. The fold stores it
		// as the bool `closed_no_code` (see internal/fold/apply.go).
		if v, ok := st.Fields["closed_no_code"]; ok {
			if b, _ := v.(bool); b {
				continue
			}
		}
		short := ShortIssueID(id)
		if markerMatchesShort(hostMarkerByID, short) {
			continue
		}
		if markerMatchesShort(nestedMarkerByID, short) {
			continue
		}
		findings = append(findings, Finding{
			Check:    "orphan-close",
			Severity: "warn",
			IssueID:  id,
			Message:  fmt.Sprintf("closed issue %s has no commit referencing (%s); pass --no-code to act close for legitimate no-code closes", id, short),
		})
	}

	// Cases (a) and (d): markers in host code log that don't resolve.
	// (a) = marker present, issue exists in act state but we already
	// covered the inverse in (b). Concretely: if the marker resolves
	// to a known issue, the marker is fine; if it doesn't resolve to a
	// known id at all (case d), warn unless suppressed.
	//
	// The wording in the design table distinguishes (a) "marker in code,
	// no matching issue" from (d) "marker referencing unknown id" — in
	// practice they collapse to the same observation under Phase 1
	// (there's no second act state to cross-reference). We treat any
	// marker whose id doesn't resolve as a single (a)/(d) class.
	reported := map[string]bool{}
	for _, m := range hostMarkers {
		// Resolve the marker's id to a known act state id. We compare
		// by the short-id (MinShortHexLen) form so different-length
		// markers for the same id collapse.
		if _, ok := known[m.IssueID]; ok {
			continue
		}
		// Try resolving as a prefix: an issue with full id
		// `act-<longer-hex>` may carry a shorter marker. Build a
		// canonical short form for the marker and lookup.
		if resolved, found := resolveMarkerToKnown(m.IssueID, known); found {
			_ = resolved
			continue
		}
		// External-PR heuristic: suppress when the commit's author email
		// is not in the recent-contributors set. We log the maintainer-
		// audit info on the finding via the Message so a `--strict` CI
		// run keeps the suppression intact for fork PRs but a manual
		// review can still see what got filtered.
		if len(internal) > 0 {
			if _, isInternal := internal[m.AuthorEmail]; !isInternal {
				// Suppressed; we don't emit a finding. (The audit
				// trail is left to git log filtered on author — adding
				// a "suppressed for external PR" warn-info finding
				// would defeat the suppression.)
				continue
			}
		}
		key := m.SHA + "\x00" + m.IssueID
		if reported[key] {
			continue
		}
		reported[key] = true
		findings = append(findings, Finding{
			Check:    "orphan-close",
			Severity: "warn",
			IssueID:  m.IssueID,
			Message:  fmt.Sprintf("commit %s carries marker %s but no matching issue exists in act state", shortSHA(m.SHA), m.IssueID),
		})
	}

	return findings
}

// markerMatchesShort reports whether any marker in idx matches the issue
// short-id `short`. The short form is `act-<MinShortHexLen-hex>` (see
// ShortIssueID). Markers may carry longer or shorter hex tails so we
// compare by the marker's id starting with the short form's hex prefix.
// "Both have to start with `act-`" is implicit in AllMarkers's output.
func markerMatchesShort(idx map[string][]gitops.MarkerCommit, short string) bool {
	// Fast path: exact match on short id.
	if _, ok := idx[short]; ok {
		return true
	}
	// Fallback: a longer-id marker (e.g. full-id) for an id whose
	// short form is `short`.
	for id := range idx {
		if strings.HasPrefix(id, short) {
			return true
		}
	}
	return false
}

// resolveMarkerToKnown checks whether the marker's id is a prefix of, or
// has-as-prefix, any known full id. Returns the full id on match.
//
// Marker hex tails are 4..fullLen chars. The act-state id is the
// canonical full id (or a shorter id from before the floor widened).
// Either direction can be a prefix of the other, so check both.
func resolveMarkerToKnown(markerID string, known map[string]*fold.IssueState) (string, bool) {
	for fullID := range known {
		if strings.HasPrefix(fullID, markerID) || strings.HasPrefix(markerID, fullID) {
			return fullID, true
		}
	}
	return "", false
}

// checkGitignoreEffective probes the host repo to confirm `.act/` is
// actually gitignored. Doctor delta item 7 (Phase 1) requires this as a
// sanity probe: a missed .gitignore entry would leak nested act state
// into the host's tracked history, defeating the "outside contributors
// see exactly the code" property. We test by running
// `git check-ignore .act/` — exit 0 = ignored (good), exit 1 = NOT
// ignored (bad, surface as error with the remediation recipe).
//
// The error finding is severity=error directly (not warn): a tracked
// .act/ is a hard policy violation. The remedy recipe
// `git rm -r --cached .act/` matches docs/coordination-plane-design.md
// "Public-repo concerns" delta item 7. (act-37f7).
func checkGitignoreEffective(repoRoot string) []Finding {
	// Only meaningful when the host repo has a .act/ dir at all. A
	// repo without .act (CI bootstrap, doc-only fork) trivially
	// satisfies the check.
	actPath := filepath.Join(repoRoot, ".act")
	if _, err := os.Stat(actPath); err != nil {
		return nil
	}
	host := gitops.NewHostGitOps(repoRoot)
	ignored, err := host.CheckIgnored(".act/")
	if err != nil {
		return []Finding{{
			Check:    "gitignore-effective",
			Severity: "error",
			Message:  fmt.Sprintf("gitignore-effective probe failed: %v", err),
		}}
	}
	if ignored {
		return nil
	}
	return []Finding{{
		Check:    "gitignore-effective",
		Severity: "error",
		Message:  ".act/ is NOT ignored by the host repo's .gitignore; nested act state will leak into tracked history. Remedy: add '.act/' to .gitignore and run: git rm -r --cached .act/",
	}}
}

// checkOrphanOps: for each issue dir under .act/ops/, find no `create` op.
func checkOrphanOps(opsDir string) []Finding {
	var findings []Finding
	entries, err := os.ReadDir(opsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return []Finding{{
			Check: "orphan-ops", Severity: "error",
			Message: fmt.Sprintf("read %s: %v", opsDir, err),
		}}
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		issueID := e.Name()
		hasCreate := false
		root := filepath.Join(opsDir, issueID)
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".json") {
				return nil
			}
			_, _, opType, perr := op.Parse(d.Name())
			if perr == nil && opType == "create" {
				hasCreate = true
			}
			return nil
		})
		if !hasCreate {
			findings = append(findings, Finding{
				Check: "orphan-ops", Severity: "error",
				IssueID: issueID,
				Message: fmt.Sprintf("issue %s has no create op (orphan ops directory)", issueID),
			})
		}
	}
	return findings
}

// checkDanglingDeps: for each issue, for each blocks/relates/supersedes parent,
// finding if the parent issue does not exist in the fold.
func checkDanglingDeps(fr *fold.FoldResult) []Finding {
	if fr == nil {
		return nil
	}
	known := map[string]bool{}
	for id, st := range fr.Issues {
		if st != nil && !st.Tombstoned {
			known[id] = true
		}
	}
	var findings []Finding
	for _, id := range sortedIssueIDs(fr.Issues) {
		st := fr.Issues[id]
		if st == nil || st.Tombstoned {
			continue
		}
		deps := extractDeps(st)
		for _, d := range deps {
			if !known[d.parent] {
				findings = append(findings, Finding{
					Check: "dangling-deps", Severity: "error",
					IssueID: id,
					Message: fmt.Sprintf("issue %s has %s edge to unknown parent %s", id, d.edge, d.parent),
				})
			}
		}
	}
	return findings
}

// dep is a normalized dep edge.
type dep struct {
	parent string
	edge   string
}

// extractDeps reads deps from the rendered fold state. The fold engine
// stores deps in Fields["deps"] as []map[string]string (per fold/lww).
func extractDeps(st *fold.IssueState) []dep {
	if st == nil {
		return nil
	}
	raw, ok := st.Fields["deps"]
	if !ok {
		return nil
	}
	var out []dep
	switch v := raw.(type) {
	case []map[string]string:
		for _, m := range v {
			out = append(out, dep{parent: m["parent"], edge: m["edge_type"]})
		}
	case []any:
		for _, e := range v {
			if m, ok := e.(map[string]string); ok {
				out = append(out, dep{parent: m["parent"], edge: m["edge_type"]})
			} else if m, ok := e.(map[string]any); ok {
				p, _ := m["parent"].(string)
				et, _ := m["edge_type"].(string)
				out = append(out, dep{parent: p, edge: et})
			}
		}
	}
	return out
}

// checkTimeTravel: for each op file, parse the filename timestamp, compare to
// the file mtime; finding if drift > 5 minutes.
func checkTimeTravel(opsDir string) []Finding {
	var findings []Finding
	const budget = 5 * time.Minute
	_ = filepath.WalkDir(opsDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		ts, _, _, perr := op.Parse(d.Name())
		if perr != nil {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		drift := info.ModTime().Sub(ts)
		if drift < 0 {
			drift = -drift
		}
		if drift > budget {
			findings = append(findings, Finding{
				Check: "time-travel", Severity: "warn",
				Message: fmt.Sprintf("op %s drift %s exceeds %s", filepath.Base(path), drift, budget),
			})
		}
		return nil
	})
	return findings
}

// checkCycle: DFS over the blocks subgraph; finding per cycle detected.
func checkCycle(fr *fold.FoldResult) []Finding {
	if fr == nil {
		return nil
	}
	adj := make(map[string][]string)
	ids := sortedIssueIDs(fr.Issues)
	for _, id := range ids {
		st := fr.Issues[id]
		if st == nil || st.Tombstoned {
			continue
		}
		for _, d := range extractDeps(st) {
			if d.edge == "blocks" {
				adj[id] = append(adj[id], d.parent)
			}
		}
	}
	// Tarjan-style: detect any back-edge in DFS tree.
	color := map[string]int{} // 0=white,1=gray,2=black
	var findings []Finding
	var dfs func(node string, stack []string) bool
	dfs = func(node string, stack []string) bool {
		color[node] = 1
		stack = append(stack, node)
		for _, n := range adj[node] {
			if color[n] == 1 {
				// Cycle: from index of n in stack to end + n.
				idx := -1
				for i, s := range stack {
					if s == n {
						idx = i
						break
					}
				}
				path := append([]string{}, stack[idx:]...)
				path = append(path, n)
				findings = append(findings, Finding{
					Check: "cycle", Severity: "error",
					IssueID: node,
					Message: fmt.Sprintf("cycle in blocks subgraph: %s", strings.Join(path, " -> ")),
				})
				return true
			}
			if color[n] == 0 {
				if dfs(n, stack) {
					// Continue searching for more cycles from other roots.
				}
			}
		}
		color[node] = 2
		return false
	}
	for _, id := range ids {
		if color[id] == 0 {
			dfs(id, nil)
		}
	}
	return findings
}

// checkUnknownOpVersion: for each op file, finding if op_version > 1.
// --fix is not honoured for this check.
func checkUnknownOpVersion(opsDir string) []Finding {
	var findings []Finding
	_ = filepath.WalkDir(opsDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		// We can't use op.Unmarshal because Validate rejects op_version!=1.
		// Decode just the op_version field manually.
		ver := extractOpVersion(body)
		if ver > op.CurrentOpVersion {
			findings = append(findings, Finding{
				Check: "unknown-op-version", Severity: "error",
				Message: fmt.Sprintf("op %s has op_version=%d > %d (cannot fix)", filepath.Base(path), ver, op.CurrentOpVersion),
			})
		}
		return nil
	})
	return findings
}

// extractOpVersion reads "op_version" from raw envelope JSON without full
// validation. Returns 0 on parse failure.
func extractOpVersion(body []byte) int {
	// Minimal parser: look for "op_version":N.
	s := string(body)
	idx := strings.Index(s, `"op_version"`)
	if idx < 0 {
		return 0
	}
	rest := s[idx+len(`"op_version"`):]
	// Skip whitespace and colon.
	rest = strings.TrimLeft(rest, " \t\r\n")
	if !strings.HasPrefix(rest, ":") {
		return 0
	}
	rest = strings.TrimLeft(rest[1:], " \t\r\n")
	n := 0
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// checkIndexDivergence: open .act/index.db, rebuild into temp; row-by-row diff.
// With --fix, replace .act/index.db with the rebuilt copy.
func checkIndexDivergence(paths config.LayoutPaths, fix bool) []Finding {
	if _, err := os.Stat(paths.IndexDB); err != nil {
		// No on-disk index — nothing to diverge from. Treat as warn.
		return nil
	}
	current, err := index.Open(paths.IndexDB)
	if err != nil {
		return []Finding{{Check: "index-divergence", Severity: "error", Message: err.Error()}}
	}
	defer current.Close()

	tmpPath := filepath.Join(paths.Root, ".doctor-rebuild.db")
	_ = os.Remove(tmpPath)
	rebuilt, err := index.Open(tmpPath)
	if err != nil {
		return []Finding{{Check: "index-divergence", Severity: "error", Message: err.Error()}}
	}
	if err := rebuilt.Rebuild(paths.Ops); err != nil {
		_ = rebuilt.Close()
		_ = os.Remove(tmpPath)
		return []Finding{{Check: "index-divergence", Severity: "error", Message: err.Error()}}
	}

	diff := diffIssueRows(current.DB(), rebuilt.DB())
	_ = rebuilt.Close()

	var findings []Finding
	if diff != "" {
		sev := "error"
		if fix {
			// Apply the rebuilt db over the canonical path.
			_ = current.Close()
			if err := os.Rename(tmpPath, paths.IndexDB); err == nil {
				sev = "warn"
				findings = append(findings, Finding{
					Check: "index-divergence", Severity: sev,
					Message: "index diverged; replaced with rebuilt copy",
				})
				return findings
			}
		}
		findings = append(findings, Finding{
			Check: "index-divergence", Severity: sev,
			Message: fmt.Sprintf("index diverged: %s", diff),
		})
	}
	_ = os.Remove(tmpPath)
	return findings
}

// diffIssueRows pulls (id, status, title) rows from each db and returns a
// short summary if they differ. Empty means equivalent.
func diffIssueRows(a, b *sql.DB) string {
	rowsA := snapshotIssues(a)
	rowsB := snapshotIssues(b)
	if rowsA == rowsB {
		return ""
	}
	return fmt.Sprintf("current=%q rebuilt=%q", truncate(rowsA, 200), truncate(rowsB, 200))
}

func snapshotIssues(db *sql.DB) string {
	if db == nil {
		return ""
	}
	rows, err := db.Query(`SELECT id, COALESCE(status,''), COALESCE(title,'') FROM issues ORDER BY id`)
	if err != nil {
		return fmt.Sprintf("ERR:%v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, status, title string
		if err := rows.Scan(&id, &status, &title); err != nil {
			return fmt.Sprintf("ERR:%v", err)
		}
		fmt.Fprintf(&b, "%s|%s|%s;", id, status, title)
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// checkIndexSchema: open .act/index.db, list tables; drop+rebuild if missing.
func checkIndexSchema(paths config.LayoutPaths, fix bool) []Finding {
	if _, err := os.Stat(paths.IndexDB); err != nil {
		return nil
	}
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		return []Finding{{Check: "index-schema", Severity: "error", Message: err.Error()}}
	}
	defer idx.Close()

	expected := []string{"issues", "issue_accept", "issue_deps", "issue_meta", "fts"}
	have := map[string]bool{}
	rows, err := idx.DB().Query(`SELECT name FROM sqlite_master WHERE type IN ('table','virtual') OR type='table'`)
	if err != nil {
		return []Finding{{Check: "index-schema", Severity: "error", Message: err.Error()}}
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			have[n] = true
		}
	}
	rows.Close()

	var missing []string
	for _, t := range expected {
		if !have[t] {
			missing = append(missing, t)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	if fix {
		_ = idx.Close()
		_ = os.Remove(paths.IndexDB)
		fresh, ferr := index.Open(paths.IndexDB)
		if ferr != nil {
			return []Finding{{Check: "index-schema", Severity: "error", Message: ferr.Error()}}
		}
		if rerr := fresh.Rebuild(paths.Ops); rerr != nil {
			_ = fresh.Close()
			return []Finding{{Check: "index-schema", Severity: "error", Message: rerr.Error()}}
		}
		_ = fresh.Close()
		return []Finding{{
			Check: "index-schema", Severity: "warn",
			Message: fmt.Sprintf("index schema missing %v; rebuilt", missing),
		}}
	}
	return []Finding{{
		Check: "index-schema", Severity: "error",
		Message: fmt.Sprintf("index schema missing tables: %v", missing),
	}}
}

// sortedIssueIDs returns the keys of the issues map in lexicographic order.
func sortedIssueIDs(issues map[string]*fold.IssueState) []string {
	out := make([]string, 0, len(issues))
	for id := range issues {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// FormatDoctorHuman renders a DoctorResult as one line per finding.
// The trailing newline is included so callers can pipe directly to stdout.
func FormatDoctorHuman(res DoctorResult) string {
	if len(res.Findings) == 0 {
		return "act doctor: 0 findings\n"
	}
	var b strings.Builder
	for _, f := range res.Findings {
		issue := f.IssueID
		if issue == "" {
			issue = "-"
		}
		fmt.Fprintf(&b, "[%s] [%s] %s %s\n", f.Severity, f.Check, issue, f.Message)
	}
	fmt.Fprintf(&b, "%d findings\n", len(res.Findings))
	return b.String()
}
