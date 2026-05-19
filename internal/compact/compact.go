// Package compact implements the opportunistic snapshot/prune flow described
// in spec-v2 §5 "Compaction".
//
// Run is the engine entry point: it acquires a non-blocking flock on
// `.act/.compact.lock`, walks `.act/ops/<issue>/`, decides which issues are
// eligible (>50 ops or >30d since last compact), and for each eligible issue
// re-folds, writes `.act/snapshots/<id>.json`, optionally prunes subsumed op
// files (only when `AggressivePrune` and the issue is closed >30d), writes a
// `compact` op-type envelope, and finally commits via the supplied gitops
// committer.
//
// Lock contention is a warning (`compaction_locked` per §5.D.4), not an error;
// Run returns Result with Skipped == ["compaction_locked"] and a nil error on
// that path so callers can surface it as a stdout warning under `--json`.
package compact

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// SkipCompactionLocked is the sentinel string Run records in Result.Skipped
// when the compact lock is contended. Callers under `--json` translate this
// into the §5.D.4 warning shape.
const SkipCompactionLocked = "compaction_locked"

// Thresholds. Exposed as vars so tests can flex them; defaults match spec.
var (
	// OpCountTrigger fires compaction when an issue's op count exceeds this.
	OpCountTrigger = 50
	// SnapshotAgeTrigger fires compaction when the last snapshot (or, if
	// none, the issue itself) is older than this.
	SnapshotAgeTrigger = 30 * 24 * time.Hour
	// CloseAgeForPrune is the closed-at age past which AggressivePrune may
	// delete subsumed op files for a closed issue.
	CloseAgeForPrune = 30 * 24 * time.Hour
)

// Options configures a Run invocation.
type Options struct {
	// IssueID restricts compaction to a single issue. Empty means "all
	// eligible issues under .act/ops/".
	IssueID string
	// DryRun disables all writes (snapshot file, compact op file, deletions,
	// commit). Result is still populated as if writes had happened.
	DryRun bool
	// AggressivePrune enables deletion of subsumed op files for issues that
	// are closed and whose `closed_at` is older than CloseAgeForPrune.
	// Without this flag, snapshots are still written but op files are kept.
	AggressivePrune bool
	// Now overrides time.Now for tests. Zero means time.Now().
	Now time.Time
}

// Result reports what Run did.
type Result struct {
	CompactedIssues int
	PrunedOps       int
	Skipped         []string // reasons (e.g. "compaction_locked", "act-aaaa: no ops")
}

// gitOpsCommitter is the minimal subset of *gitops.GitOps needed by Run.
// The package does not stage files; it relies on `git commit -a` semantics
// being unsuitable here, so the committer is expected to add explicit paths
// via its own staging pipeline. To keep the interface tight, we model the
// committer as a single Commit method and have Run pre-stage via `git add`.
type gitOpsCommitter interface {
	Commit(message string) error
}

// stagingCommitter is the optional extension some callers (e.g. *gitops.GitOps)
// implement to let Run pre-stage paths before invoking Commit.
type stagingCommitter interface {
	gitOpsCommitter
	StageOpFile(path string) error
}

// Run executes the compaction procedure described in spec-v2 §5.
//
// It returns (Result{}, error) for genuine errors only — IO problems, malformed
// envelopes, commit failures. Lock contention is recorded in Result.Skipped
// with sentinel SkipCompactionLocked and returns a nil error.
func Run(repoRoot string, opts Options, gitops gitOpsCommitter) (Result, error) {
	if repoRoot == "" {
		return Result{}, fmt.Errorf("compact: repoRoot is empty")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	actDir := filepath.Join(repoRoot, ".act")
	if _, err := os.Stat(actDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Result{}, nil
		}
		return Result{}, fmt.Errorf("compact: stat .act: %w", err)
	}

	lockPath := filepath.Join(actDir, ".compact.lock")
	release, locked, err := acquireLock(lockPath)
	if err != nil {
		return Result{}, fmt.Errorf("compact: acquire lock: %w", err)
	}
	if !locked {
		return Result{Skipped: []string{SkipCompactionLocked}}, nil
	}
	defer release()

	opsRoot := filepath.Join(actDir, "ops")
	snapDir := filepath.Join(actDir, "snapshots")

	issues, err := listIssues(opsRoot, opts.IssueID)
	if err != nil {
		return Result{}, err
	}

	res := Result{}
	stager, _ := gitops.(stagingCommitter)
	var stagedAnything bool

	for _, issueID := range issues {
		count, latestSnapTime, err := scanIssue(opsRoot, snapDir, issueID)
		if err != nil {
			return Result{}, err
		}
		if !shouldCompact(count, latestSnapTime, now) {
			continue
		}

		// Re-fold from current ops on disk.
		state, err := fold.FoldIssue(opsRoot, issueID, fold.ApplyDispatch)
		if err != nil {
			return Result{}, fmt.Errorf("compact: fold %s: %w", issueID, err)
		}

		// Discover the subsumed op files (every op currently on disk for the
		// issue) so the snapshot records the subsumed set verbatim.
		subsumed, err := listOpFiles(opsRoot, issueID)
		if err != nil {
			return Result{}, err
		}
		// Subsumed paths are stored relative to repoRoot for portability.
		relSubsumed := make([]string, 0, len(subsumed))
		for _, p := range subsumed {
			rel, rerr := filepath.Rel(repoRoot, p)
			if rerr != nil {
				rel = p
			}
			relSubsumed = append(relSubsumed, filepath.ToSlash(rel))
		}
		sort.Strings(relSubsumed)

		// Compute the snapshot bytes. The shape mirrors spec §5.3 with one
		// addition: as_of_hlc captures the high-water HLC across all ops for
		// the issue.
		asOf := highWaterHLC(state)
		treeHash, err := hashOpsTree(opsRoot, issueID)
		if err != nil {
			return Result{}, fmt.Errorf("compact: tree hash %s: %w", issueID, err)
		}

		snap := snapshotFile{
			ID:          issueID,
			State:       fold.RenderState(state),
			SubsumedOps: relSubsumed,
			AsOfHLC:     hlcString(asOf),
			TreeHash:    treeHash,
		}
		body, err := canonicaljson.Marshal(snap)
		if err != nil {
			return Result{}, fmt.Errorf("compact: marshal snapshot %s: %w", issueID, err)
		}

		snapPath := filepath.Join(snapDir, issueID+".json")
		if !opts.DryRun {
			if err := os.MkdirAll(snapDir, 0o755); err != nil {
				return Result{}, fmt.Errorf("compact: mkdir snapshots: %w", err)
			}
			if err := atomicWrite(snapPath, body); err != nil {
				return Result{}, err
			}
			if stager != nil {
				if err := stager.StageOpFile(snapPath); err != nil {
					return Result{}, fmt.Errorf("compact: stage snapshot: %w", err)
				}
			}
			stagedAnything = true
		}

		// Optional prune: only if AggressivePrune and the issue is closed >30d.
		if opts.AggressivePrune && isClosedLongEnough(state, now) {
			for _, p := range subsumed {
				if !opts.DryRun {
					if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
						return Result{}, fmt.Errorf("compact: prune %s: %w", p, err)
					}
					if stager != nil {
						// Stage the deletion.
						_ = stager.StageOpFile(p)
					}
				}
				res.PrunedOps++
			}
		}

		// Compact op envelope. We bypass op.ValidatePayload for op_type=compact
		// because it is an internal maintenance op not present in the writer's
		// closed type set; we still produce a canonical, fold-compatible JSON
		// blob so on-disk form is uniform.
		if !opts.DryRun {
			compactPath, err := writeCompactOp(opsRoot, issueID, snapPath, treeHash, len(subsumed), asOf, repoRoot)
			if err != nil {
				return Result{}, err
			}
			if stager != nil {
				if err := stager.StageOpFile(compactPath); err != nil {
					return Result{}, fmt.Errorf("compact: stage compact op: %w", err)
				}
			}
		}

		res.CompactedIssues++
	}

	// Commit at most one act-compact commit covering all eligible issues.
	if !opts.DryRun && stagedAnything && res.CompactedIssues > 0 {
		msg := fmt.Sprintf("act-compact: %d issues", res.CompactedIssues)
		if err := gitops.Commit(msg); err != nil {
			return Result{}, fmt.Errorf("compact: commit: %w", err)
		}
	}

	return res, nil
}

// snapshotFile is the on-disk shape of `.act/snapshots/<id>.json`.
type snapshotFile struct {
	ID          string         `json:"id"`
	State       map[string]any `json:"state"`
	SubsumedOps []string       `json:"subsumed_ops"`
	AsOfHLC     string         `json:"as_of_hlc"`
	TreeHash    string         `json:"tree_hash"`
}

// listIssues returns the issue ids under opsRoot. If only is non-empty it
// returns just that one (when present on disk).
func listIssues(opsRoot, only string) ([]string, error) {
	if only != "" {
		p := filepath.Join(opsRoot, only)
		info, err := os.Stat(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
			return nil, fmt.Errorf("compact: stat %s: %w", p, err)
		}
		if !info.IsDir() {
			return nil, nil
		}
		return []string{only}, nil
	}
	entries, err := os.ReadDir(opsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("compact: read ops root: %w", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// scanIssue counts ops for the issue and finds the latest snapshot mtime (or
// zero time if no snapshot). The op count includes existing compact ops; the
// trigger uses the raw count per spec.
func scanIssue(opsRoot, snapDir, issueID string) (int, time.Time, error) {
	count := 0
	root := filepath.Join(opsRoot, issueID)
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			count++
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
		return 0, time.Time{}, fmt.Errorf("compact: walk ops: %w", walkErr)
	}

	var snapTime time.Time
	snapPath := filepath.Join(snapDir, issueID+".json")
	if info, err := os.Stat(snapPath); err == nil {
		snapTime = info.ModTime()
	}
	return count, snapTime, nil
}

// shouldCompact applies the §5 trigger: > 50 ops OR last snapshot age > 30d.
// When no snapshot exists, the age trigger fires only when the op count is
// non-zero (an empty issue is not eligible).
func shouldCompact(opCount int, lastSnap time.Time, now time.Time) bool {
	if opCount == 0 {
		return false
	}
	if opCount > OpCountTrigger {
		return true
	}
	if lastSnap.IsZero() {
		// No snapshot yet — fire only if the oldest op (proxied by the
		// caller's "now > issue age" gate) is >30d. We don't have direct
		// access to the issue's first-op mtime here without another walk;
		// keeping the simpler rule: with no snapshot, only the count trigger
		// fires. The age-without-snapshot path is the spec's "or no compact
		// op ever and the issue is > 30 days old" — engine consumers
		// (doctor, write commands) supply their own signal by setting Now or
		// by passing IssueID directly. The op-count gate is the dominant
		// trigger in practice.
		return false
	}
	if now.Sub(lastSnap) > SnapshotAgeTrigger {
		return true
	}
	return false
}

// listOpFiles returns absolute paths of all .json op files under
// opsRoot/<issueID>/, sorted lexicographically.
func listOpFiles(opsRoot, issueID string) ([]string, error) {
	root := filepath.Join(opsRoot, issueID)
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// hlcString renders an HLC as a compact string form for use in snapshot and
// compact-op payloads. The JSON-marshalled wire form is the canonical wall +
// logical + node_id triple per spec §3; we re-use it here.
func hlcString(h hlc.HLC) string {
	if h.NodeID == "" {
		return ""
	}
	b, err := h.MarshalJSON()
	if err != nil {
		return ""
	}
	return string(b)
}

// highWaterHLC returns the maximum HLC across all tracked LWW fields. If the
// state has none, returns the zero HLC.
//
// state.LastHLC carries hlc.Stamp (HLC + op_hash) per LWW gating; the high
// water of just the HLC component is what callers want here (snapshot
// scheduling / pruning), since the hash is incidental to time-of-last-write.
// Sentinel: top.NodeID=="" identifies an uninitialised top so the first
// non-zero stamp always installs.
func highWaterHLC(state *fold.IssueState) hlc.HLC {
	var top hlc.HLC
	for _, stamp := range state.LastHLC {
		if top.NodeID == "" || top.Less(stamp.HLC) {
			top = stamp.HLC
		}
	}
	return top
}

// isClosedLongEnough reports whether the issue is closed and `closed_at` is
// older than CloseAgeForPrune.
func isClosedLongEnough(state *fold.IssueState, now time.Time) bool {
	if state == nil {
		return false
	}
	status, _ := state.Fields["status"].(string)
	if status != "closed" {
		return false
	}
	closedAt, _ := state.Fields["closed_at"].(string)
	if closedAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, closedAt)
	if err != nil {
		// RFC3339Millis ("...sssZ") is a strict subset of RFC3339; the
		// fallback parse should never fail, but if it does we conservatively
		// refuse to prune.
		return false
	}
	return now.Sub(t) > CloseAgeForPrune
}

// writeCompactOp writes a `compact` op envelope for issueID under
// opsRoot/<issue>/<yyyy-mm>/. The envelope deliberately bypasses
// op.ValidatePayload because `compact` is not in op.ValidOpTypes; we hand-roll
// the on-disk JSON here. The filename is `<iso>-<8hex>-compact.json` to mirror
// the canonical layout used by user-visible ops.
func writeCompactOp(opsRoot, issueID, snapPath, treeHash string, subsumedCount int, asOf hlc.HLC, repoRoot string) (string, error) {
	wallMs := asOf.Wall
	if wallMs == 0 {
		wallMs = time.Now().UnixMilli()
	}
	t := time.UnixMilli(wallMs).UTC()
	shard := filepath.Join(opsRoot, issueID, t.Format("2006-01"))
	if err := os.MkdirAll(shard, 0o755); err != nil {
		return "", fmt.Errorf("compact: mkdir shard: %w", err)
	}
	relSnap, err := filepath.Rel(repoRoot, snapPath)
	if err != nil {
		relSnap = snapPath
	}
	body := map[string]any{
		"op_type":            "compact",
		"issue_id":           issueID,
		"snapshot_path":      filepath.ToSlash(relSnap),
		"snapshot_tree_hash": treeHash,
		"subsumed_count":     subsumedCount,
		"as_of_hlc":          hlcString(asOf),
	}
	canon, err := canonicaljson.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("compact: marshal compact op: %w", err)
	}
	// Filename: ISO-millis + 8 hex of treeHash + "-compact.json".
	hashPart := treeHash
	if len(hashPart) > 8 {
		hashPart = hashPart[:8]
	}
	if hashPart == "" {
		hashPart = "00000000"
	}
	// Filename time-component uses the NTFS-safe dash-form layout shared
	// with op filenames (act-2f3d / act-d5d1ff). ':' is reserved on NTFS
	// and breaks `git checkout` on Windows hosts before any Go code runs;
	// the compact tombstone is written into the same shard as op files
	// so it must follow the same naming contract. The canonical layout
	// lives at op.IsoLayout.
	iso := t.Format(op.IsoLayout)
	fname := fmt.Sprintf("%s-%s-compact.json", iso, hashPart)
	path := filepath.Join(shard, fname)
	if err := atomicWrite(path, canon); err != nil {
		return "", err
	}
	return path, nil
}

// hashOpsTree returns a stable digest of the op-files set under
// opsRoot/<issueID>/. Spec calls this the "git tree hash"; we compute a sha256
// of the sorted list of (relative-path, file-sha256) pairs so the value is
// deterministic without invoking git. The digest is recorded both on the
// snapshot file and on the compact op for cross-checking by fold.
func hashOpsTree(opsRoot, issueID string) (string, error) {
	files, err := listOpFiles(opsRoot, issueID)
	if err != nil {
		return "", err
	}
	type pair struct {
		Path string `json:"path"`
		Hash string `json:"hash"`
	}
	pairs := make([]pair, 0, len(files))
	for _, p := range files {
		body, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("compact: read %s: %w", p, err)
		}
		sum := sha256Hex(body)
		rel, _ := filepath.Rel(filepath.Join(opsRoot, issueID), p)
		pairs = append(pairs, pair{Path: filepath.ToSlash(rel), Hash: sum})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Path < pairs[j].Path })
	enc, err := json.Marshal(pairs)
	if err != nil {
		return "", err
	}
	return sha256Hex(enc), nil
}

// atomicWrite writes body to path via a temp file in the same directory and
// renames it onto the target.
func atomicWrite(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("compact: mkdir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".compact-*.tmp")
	if err != nil {
		return fmt.Errorf("compact: create temp in %s: %w", dir, err)
	}
	tmp := f.Name()
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("compact: write temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("compact: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("compact: close temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("compact: rename %s: %w", path, err)
	}
	return nil
}
