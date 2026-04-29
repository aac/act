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
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
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

// checkOrphanClose: for each closed issue, search `git log --all --grep`
// for `(act-XXXX)`; finding if no matching commit is found.
func checkOrphanClose(repoRoot string, fr *fold.FoldResult) []Finding {
	if fr == nil {
		return nil
	}
	var findings []Finding
	ids := sortedIssueIDs(fr.Issues)
	for _, id := range ids {
		st := fr.Issues[id]
		if st == nil || st.Tombstoned {
			continue
		}
		status, _ := st.Fields["status"].(string)
		if status != "closed" {
			continue
		}
		short := id
		if strings.HasPrefix(id, "act-") && len(id) >= 8 {
			short = id[:8] // act-XXXX
		}
		// `git log --all --grep '(act-XXXX)' --pretty=%H`
		cmd := exec.Command("git", "-C", repoRoot, "log", "--all", "--grep", "("+short+")", "--pretty=%H")
		out, err := cmd.Output()
		if err != nil || len(strings.TrimSpace(string(out))) == 0 {
			findings = append(findings, Finding{
				Check:    "orphan-close",
				Severity: "warn",
				IssueID:  id,
				Message:  fmt.Sprintf("closed issue %s has no commit referencing (%s)", id, short),
			})
		}
	}
	return findings
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
