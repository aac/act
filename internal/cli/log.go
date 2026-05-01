package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/op"
)

// LogResult is the success-shape returned by RunLog. It is JSON-serialisable
// and is the same shape that the human renderer consumes.
type LogResult struct {
	ID  string        `json:"id"`
	Ops []op.Envelope `json:"ops"`
}

// LogErrorOutput is the structured shape returned to the caller when log
// refuses. Candidates is non-nil only on the id_ambiguous path; it is also
// mirrored under Details["candidates"] so the on-the-wire JSON envelope
// matches spec §"Errors" (`details.candidates[]`).
type LogErrorOutput struct {
	Error      string         `json:"error"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	Candidates []string       `json:"-"`
}

// RunLog implements `act log <id>`. It walks `.act/ops/<id>/<yyyy-mm>/*.json`
// for the resolved id, parses each op envelope, sorts globally by HLC then op
// hash, and returns the chronological op stream.
//
// Returns:
//   - output: LogResult on success, LogErrorOutput on failure.
//   - exitCode: 0 success; 3 missing .act/, unknown id, or ambiguous prefix.
func RunLog(repoRoot, idOrPrefix string, asJSON bool) (output any, exitCode int) {
	_ = asJSON // reserved: asJSON shapes the human renderer in main.go, not here.

	actDir := filepath.Join(repoRoot, ".act")
	if _, err := os.Stat(actDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return LogErrorOutput{
				Error:   "not_in_git",
				Message: fmt.Sprintf("act log: %s/.act not found; run `act init` first", repoRoot),
			}, 3
		}
		return LogErrorOutput{
			Error:   "not_in_git",
			Message: fmt.Sprintf("act log: stat %s: %v", actDir, err),
		}, 3
	}

	opsDir := filepath.Join(actDir, "ops")
	allIDs, err := listIssueIDs(opsDir)
	if err != nil {
		return LogErrorOutput{
			Error:   "ops_walk_failed",
			Message: err.Error(),
		}, 3
	}

	full, ambiguous, found := ids.ResolvePrefix(allIDs, idOrPrefix)
	if ambiguous {
		candidates := ambiguousCandidates(allIDs, idOrPrefix)
		return LogErrorOutput{
			Error:   "id_ambiguous",
			Message: fmt.Sprintf("act log: prefix %q matches %d issues", idOrPrefix, len(candidates)),
			Details: map[string]any{
				"prefix":     idOrPrefix,
				"candidates": candidates,
			},
			Candidates: candidates,
		}, 3
	}
	if !found {
		return LogErrorOutput{
			Error:   "issue_not_found",
			Message: fmt.Sprintf("act log: no issue matches %q", idOrPrefix),
			Details: map[string]any{"query": idOrPrefix},
		}, 3
	}

	envs, err := readIssueOps(opsDir, full)
	if err != nil {
		return LogErrorOutput{
			Error:   "ops_read_failed",
			Message: err.Error(),
		}, 3
	}

	sortLogOps(envs)
	return LogResult{ID: full, Ops: envelopesOnly(envs)}, 0
}

// FormatLogHuman renders a LogResult as the human-friendly text form: one line
// per op with `<RFC3339Millis wall> <op-type> <8hex-hash> [issue=<short>]`,
// followed by a count line. Returns the multi-line string with a trailing
// newline. Callers print directly to stdout.
func FormatLogHuman(res LogResult) string {
	shortByID := ids.ShortestUniquePrefixes([]string{res.ID})
	short := shortByID[res.ID]
	if short == "" {
		short = res.ID
	}
	var b strings.Builder
	for _, env := range res.Ops {
		hash, err := env.Hash()
		if err != nil {
			hash = "????????"
		}
		wall := time.UnixMilli(env.HLC.Wall).UTC().Format(rfc3339Millis)
		fmt.Fprintf(&b, "%s %s %s [issue=%s]\n", wall, env.OpType, hash, short)
	}
	fmt.Fprintf(&b, "%d ops\n", len(res.Ops))
	return b.String()
}

// loggedOp is the in-memory representation of a parsed op for sorting:
// envelope plus its full canonical hash for the secondary tiebreak.
type loggedOp struct {
	env      op.Envelope
	fullHash string
}

// ListIssueIDs is the exported alias for listIssueIDs, used by sibling
// packages (e.g. internal/mcp) that compose multi-op writes and need to
// resolve ids against the on-disk universe.
func ListIssueIDs(opsDir string) ([]string, error) {
	return listIssueIDs(opsDir)
}

// listIssueIDs returns the full ids known to the repo by enumerating the
// per-issue subdirectories under `.act/ops/`. A missing opsDir is reported as
// an empty list, not an error: the caller distinguishes this from a corrupt
// `.act/` via the upstream stat on `.act/`.
func listIssueIDs(opsDir string) ([]string, error) {
	entries, err := os.ReadDir(opsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("act log: read %s: %w", opsDir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !ids.IsValidID(name) {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// readIssueOps walks `<opsDir>/<issueID>/<yyyy-mm>/*.json` and returns the
// parsed envelopes plus their canonical hashes. Files outside the
// month-shard layout (or with non-.json suffix) are skipped.
func readIssueOps(opsDir, issueID string) ([]loggedOp, error) {
	root := filepath.Join(opsDir, issueID)
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("act log: stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("act log: %s: not a directory", root)
	}

	var out []loggedOp
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return fmt.Errorf("act log: walk %s: %w", path, werr)
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("act log: read %s: %w", path, rerr)
		}
		env, uerr := op.Unmarshal(body)
		if uerr != nil {
			return fmt.Errorf("act log: parse %s: %w", path, uerr)
		}
		full, herr := env.FullHash()
		if herr != nil {
			return fmt.Errorf("act log: hash %s: %w", path, herr)
		}
		out = append(out, loggedOp{env: env, fullHash: full})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

// sortLogOps sorts ops by (HLC.Wall, HLC.Logical, fullHash) ascending. This is
// the same key fold uses; logging the audit stream must agree with the order
// fold consumes.
func sortLogOps(ops []loggedOp) {
	sort.SliceStable(ops, func(i, j int) bool {
		a, b := ops[i].env.HLC, ops[j].env.HLC
		if a.Wall != b.Wall {
			return a.Wall < b.Wall
		}
		if a.Logical != b.Logical {
			return a.Logical < b.Logical
		}
		return ops[i].fullHash < ops[j].fullHash
	})
}

// envelopesOnly extracts the envelope slice from a sorted []loggedOp. The
// fullHash side-data is not part of the JSON output (it's an in-memory sort
// key only).
func envelopesOnly(ops []loggedOp) []op.Envelope {
	out := make([]op.Envelope, len(ops))
	for i, o := range ops {
		out[i] = o.env
	}
	return out
}

// normalizePrefix is the local mirror of ids.normalizeHex (unexported in the
// ids package). We re-derive the hex tail to recompute the candidate list on
// the ambiguous path; the resolver itself doesn't return candidates.
func normalizePrefix(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "act-")
	return s
}

// stripActPrefix is the local mirror of ids.hexTail.
func stripActPrefix(id string) string {
	return strings.TrimPrefix(strings.ToLower(id), "act-")
}
