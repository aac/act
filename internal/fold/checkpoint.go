package fold

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aac/act/internal/canonicaljson"
)

// checkpointSchemaVersion is the on-disk schema version for
// `.act/fold-checkpoint.json`. A mismatch causes ReadCheckpoint to return nil
// (cold-start). See spec §3.5.
const checkpointSchemaVersion = 1

// IssueCheckpoint records the per-issue cache entry written into the
// fold checkpoint.
//
//   - SubtreeHash is a deterministic tree hash of the issue's ops subtree
//     (`.act/ops/<id>/`).
//   - FoldHash is sha256(canonicaljson.Marshal(RenderState(state))) for the
//     issue's terminal folded state.
type IssueCheckpoint struct {
	SubtreeHash string `json:"subtree_hash"`
	FoldHash    string `json:"fold_hash"`
}

// Checkpoint is the in-memory representation of `.act/fold-checkpoint.json`.
//
// TreeHash is a deterministic tree hash of the entire `.act/ops/` directory.
// When TreeHash matches the current on-disk hash, the cached fold result is
// known to be authoritative.
type Checkpoint struct {
	TreeHash string                     `json:"tree_hash"`
	Issues   map[string]IssueCheckpoint `json:"issues"`
}

// checkpointFile is the JSON wrapper struct that adds schema_version on disk.
type checkpointFile struct {
	SchemaVersion int                        `json:"schema_version"`
	TreeHash      string                     `json:"tree_hash"`
	Issues        map[string]IssueCheckpoint `json:"issues"`
}

// InvalidateCheckpoint removes the fold-checkpoint at path. A missing file
// is not an error — invalidation is idempotent so callers can invoke it
// blindly after any operation that may have added new ops (e.g. the
// read-path cache layer's post-rebase invariant per Phase 2 ticket 5).
//
// A non-NotExist error (permission denied, busy file on Windows, etc.) is
// returned so the caller can decide whether to surface it. Production
// callers in v0.1 treat a stray .act/fold-checkpoint.json as a cold-start
// trigger anyway (the schema-version mismatch path), so even a transient
// failure here doesn't risk serving stale fold output.
func InvalidateCheckpoint(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("fold: invalidate checkpoint %s: %w", path, err)
	}
	return nil
}

// ReadCheckpoint loads a checkpoint from path. If the file is missing or its
// schema version does not match this binary's, ReadCheckpoint returns
// (nil, nil) — the caller treats that as cold-start.
func ReadCheckpoint(path string) (*Checkpoint, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("fold: read checkpoint %s: %w", path, err)
	}
	var f checkpointFile
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("fold: parse checkpoint %s: %w", path, err)
	}
	if f.SchemaVersion != checkpointSchemaVersion {
		return nil, nil
	}
	cp := &Checkpoint{
		TreeHash: f.TreeHash,
		Issues:   f.Issues,
	}
	if cp.Issues == nil {
		cp.Issues = map[string]IssueCheckpoint{}
	}
	return cp, nil
}

// WriteCheckpoint atomically writes cp to path using canonical JSON.
//
// The write goes to `<path>.tmp`, fsyncs, then renames over path; a crash
// mid-write therefore leaves either the prior file intact or a stray .tmp
// that readers ignore.
func WriteCheckpoint(path string, cp *Checkpoint) error {
	if cp == nil {
		return errors.New("fold: WriteCheckpoint: nil checkpoint")
	}
	issues := cp.Issues
	if issues == nil {
		issues = map[string]IssueCheckpoint{}
	}
	// Build a map for canonical-JSON marshaling; canonicaljson sorts keys
	// lexicographically, so the on-disk bytes are stable.
	issuesAny := map[string]any{}
	for id, ic := range issues {
		issuesAny[id] = map[string]any{
			"subtree_hash": ic.SubtreeHash,
			"fold_hash":    ic.FoldHash,
		}
	}
	body, err := canonicaljson.Marshal(map[string]any{
		"schema_version": checkpointSchemaVersion,
		"tree_hash":      cp.TreeHash,
		"issues":         issuesAny,
	})
	if err != nil {
		return fmt.Errorf("fold: marshal checkpoint: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("fold: mkdir for checkpoint: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("fold: open temp checkpoint: %w", err)
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fold: write temp checkpoint: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fold: fsync temp checkpoint: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("fold: close temp checkpoint: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("fold: rename temp checkpoint: %w", err)
	}
	return nil
}

// ComputeTreeHash returns a deterministic content hash of the directory at
// opsRoot.
//
// For v0.1 this is sha256 over a sorted list of ("<rel-path>\0<file-sha256>\n")
// entries for every regular file in the tree. The hash is stable across
// runs, machines, and filesystems.
//
// Future iterations (act-9b55 / act-2e8d) may switch to the literal git tree
// SHA-1 via `git ls-tree` or `git mktree` so subtree hashes are invariant
// under squash/rebase. This implementation already shares the v0.1 stability
// property the rest of the cache logic relies on; only the cross-tool
// equivalence is missing.
//
// A missing opsRoot returns a stable "empty" hash so cold-start is
// well-defined.
func ComputeTreeHash(opsRoot string) (string, error) {
	return computeHash(opsRoot, "")
}

// ComputeIssueSubtreeHash is ComputeTreeHash scoped to one issue dir.
func ComputeIssueSubtreeHash(opsRoot, issueID string) (string, error) {
	return computeHash(filepath.Join(opsRoot, issueID), "")
}

func computeHash(root, _ string) (string, error) {
	type entry struct {
		rel  string
		hash string
	}
	var entries []entry

	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Empty tree: stable, distinct from any non-empty content.
			return hashOf("EMPTY"), nil
		}
		return "", fmt.Errorf("fold: stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fold: %s: not a directory", root)
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(body)
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		// Normalise to forward slashes so the hash is platform independent.
		rel = filepath.ToSlash(rel)
		entries = append(entries, entry{rel: rel, hash: hex.EncodeToString(sum[:])})
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("fold: walk %s: %w", root, walkErr)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	h := sha256.New()
	for _, e := range entries {
		h.Write([]byte(e.rel))
		h.Write([]byte{0})
		h.Write([]byte(e.hash))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// listIssueDirs returns the list of immediate subdirectory names under
// opsRoot — i.e. the per-issue directories.
func listIssueDirs(opsRoot string) ([]string, error) {
	entries, err := os.ReadDir(opsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		ids = append(ids, name)
	}
	sort.Strings(ids)
	return ids, nil
}

// computeIssueFoldHash returns sha256(canonicaljson(RenderState(state))).
func computeIssueFoldHash(state *IssueState) (string, error) {
	rendered := RenderState(state)
	// canonicaljson handles map[string]any and []string; nil renders as "null"
	body, err := canonicaljson.Marshal(rendered)
	if err != nil {
		return "", fmt.Errorf("fold: marshal render state: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// FoldWithCheckpoint folds opsRoot, consulting an on-disk checkpoint at
// checkpointPath to skip work.
//
// Algorithm (v0.1):
//
//  1. Read the existing Checkpoint (if any).
//  2. Compute the current tree hash of opsRoot.
//  3. Full hit: cp != nil && cp.TreeHash == current — refold is skipped, the
//     cached Checkpoint is returned and FoldResult is nil. Callers that need
//     a populated FoldResult should refold; v0.1 uses checkpoint hits as a
//     pure staleness check rather than a result cache.
//  4. Miss: refold every issue and rebuild the checkpoint from scratch.
//
// v0.1 simplification: per-issue cache reuse (skipping refold for issues
// whose SubtreeHash is unchanged) is not yet wired in. The checkpoint
// already records per-issue SubtreeHash + FoldHash so a future change
// (act-9b55 / act-2e8d) can flip on partial reuse without touching
// callers. See spec §5.B.5 for the partial-reuse rule.
func FoldWithCheckpoint(opsRoot, checkpointPath string, applyDispatch func(string) ApplyFunc) (*FoldResult, *Checkpoint, error) {
	cp, err := ReadCheckpoint(checkpointPath)
	if err != nil {
		return nil, nil, err
	}
	current, err := ComputeTreeHash(opsRoot)
	if err != nil {
		return nil, nil, err
	}

	// Full hit: the on-disk tree is byte-identical to what produced the
	// checkpoint. The cached fold is authoritative.
	if cp != nil && cp.TreeHash == current {
		return nil, cp, nil
	}

	// Cold or stale: full refold.
	res, err := Fold(opsRoot, applyDispatch)
	if err != nil {
		return nil, nil, err
	}

	// Build the new per-issue map. Enumerate dirs on disk (so we drop
	// issues whose subtree disappeared and pick up new ones), and fall
	// back to the FoldResult when an issue dir exists but yielded no
	// folded state (only possible for empty dirs).
	ids, err := listIssueDirs(opsRoot)
	if err != nil {
		return nil, nil, err
	}
	issues := map[string]IssueCheckpoint{}
	for _, id := range ids {
		sub, err := ComputeIssueSubtreeHash(opsRoot, id)
		if err != nil {
			return nil, nil, err
		}
		st, ok := res.Issues[id]
		if !ok {
			st = newIssueState(id)
		}
		fh, err := computeIssueFoldHash(st)
		if err != nil {
			return nil, nil, err
		}
		issues[id] = IssueCheckpoint{SubtreeHash: sub, FoldHash: fh}
	}
	// Some folded issues may have ids that don't correspond to a
	// `.act/ops/<id>/` directory (e.g. ops cross-referencing other
	// issues — not a current shape, but defensive). Don't drop them
	// silently if they show up in the fold result.
	for id, st := range res.Issues {
		if _, ok := issues[id]; ok {
			continue
		}
		sub, err := ComputeIssueSubtreeHash(opsRoot, id)
		if err != nil {
			return nil, nil, err
		}
		fh, err := computeIssueFoldHash(st)
		if err != nil {
			return nil, nil, err
		}
		issues[id] = IssueCheckpoint{SubtreeHash: sub, FoldHash: fh}
	}

	newCP := &Checkpoint{TreeHash: current, Issues: issues}
	if err := WriteCheckpoint(checkpointPath, newCP); err != nil {
		return nil, nil, err
	}
	return res, newCP, nil
}
