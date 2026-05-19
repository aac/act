// Package cli — `act bootstrap-worker` subcommand.
//
// Phase 1.5 prerequisite for the upcoming coordination-plane Phase 2 work
// (docs/coordination-plane-phase2-plan.md). Copies the host repo's `.act/`
// state tree into a worker worktree's path so a dispatched sub-agent can run
// act commands against a state directory that already mirrors the orchestrator's
// view. This is the "import-from-local-path" mode that Phase 2 will pair with
// a future `--from-remote` mode (ticket 7 in the Phase 2 plan).
//
// Surface:
//
//	act bootstrap-worker <target-path> [--force] [--json]
//
// Behaviour:
//
//   - Resolve the source `.act/` from cwd via the host-repo resolver in
//     internal/gitops.
//   - Copy the entire `.act/` tree (ops/, snapshots/, hooks/, imports/,
//     index.db if present, config.json, .git) into `<target>/.act.bootstrap/`,
//     then atomic-rename to `<target>/.act/`. On any failure mid-copy, the
//     `.act.bootstrap/` tree is torn down so we never leave a partial `.act/`.
//   - Refuse to overwrite a non-empty existing `<target>/.act/` unless
//     --force is passed (empty or missing target .act/ is the normal case).
//   - Stamp a `.act/.bootstrap-meta.json` file in the new target containing
//     {source_root, dispatch_hlc, copied_at} — see the dispatch_hlc note
//     below.
//   - Round-trip validate: run a `ready`-equivalent fold against the target
//     after the rename, and tear down the target if validation fails.
//
// dispatch_hlc storage:
//
//	The HLC at copy time is persisted to a small `.bootstrap-meta.json`
//	file at the target `.act/` root. We chose the meta-file over the
//	"only-on-stdout" option because:
//
//	  - the orchestrator may drop stdout on a non-zero exit and we still
//	    want the dispatch HLC durable for later harvest comparison.
//	  - Phase 2's `act harvest` will lean on filename-diff as the primary
//	    path, but having a persisted dispatch_hlc gives harvest a fallback
//	    signal when the ops/ tree was rewritten.
//	  - the file name starts with a dot so it doesn't show up in normal
//	    `ls` output and is gitignored by act's own conventions (the nested
//	    .act/.git tree commits its own contents, but this file is created
//	    AFTER the rename and is not part of the source commit, so it lives
//	    outside the nested repo's tracked tree).
//
//	The dispatch_hlc value is derived from `time.Now()` UnixMilli with the
//	source's node_id, since this command is not itself a tracked op-write;
//	no fold semantics depend on it for Phase 1.5.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/hlc"
)

// BootstrapWorkerOptions controls `act bootstrap-worker`.
type BootstrapWorkerOptions struct {
	// SourceCWD is the directory the resolver should start its host-repo
	// walk from. Callers usually pass os.Getwd(); tests set it explicitly.
	SourceCWD string

	// Target is the path under which the new `.act/` will land. The
	// command creates `<target>/.act/`; the parent path must exist (we
	// don't try to mkdir arbitrary ancestors — that's the caller's job).
	Target string

	// Force makes the command replace a non-empty existing `<target>/.act/`.
	// Empty or missing target `.act/` is allowed without --force.
	Force bool

	// AsJSON selects the JSON output rendering branch (the caller's
	// concern; this struct is plumbed for parity with other commands).
	AsJSON bool

	// Now is an injectable clock for tests; default time.Now.
	Now func() time.Time
}

// BootstrapWorkerResult is the success payload.
type BootstrapWorkerResult struct {
	SourceRoot      string `json:"source_root"`
	Target          string `json:"target"`
	OpsCopied       int    `json:"ops_copied"`
	SnapshotsCopied int    `json:"snapshots_copied"`
	DispatchHLC     string `json:"dispatch_hlc"`
}

// bootstrapMeta is the small JSON document written to
// `<target>/.act/.bootstrap-meta.json`. It is diagnostic-only for Phase
// 1.5; Phase 2's harvest may read it as a fallback ordering signal.
type bootstrapMeta struct {
	SourceRoot  string `json:"source_root"`
	DispatchHLC string `json:"dispatch_hlc"`
	CopiedAt    string `json:"copied_at"`
}

// BootstrapMetaFileName is the basename of the meta file dropped at the
// target's `.act/` root. Exported so tests can assert on the same constant
// the implementation uses.
const BootstrapMetaFileName = ".bootstrap-meta.json"

// RunBootstrapWorker is the package-public entry point. Returns a
// JSON-encodable value (BootstrapWorkerResult on success, error-envelope
// map on failure) plus an exit code per the universal error table:
//
//	0 success
//	2 bad input (target empty, target .act/ non-empty without --force,
//	  invalid source state)
//	3 filesystem / git / validation failure that would leave the worker
//	  unusable
func RunBootstrapWorker(opts BootstrapWorkerOptions) (any, int) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Target == "" {
		return map[string]any{
			"error":   ErrBadFlag,
			"message": "act bootstrap-worker: <target-path> is required",
		}, 2
	}

	// Resolve the source host repo root and its .act/.
	srcStart := opts.SourceCWD
	if srcStart == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return map[string]any{
				"error":   ErrNoRepo,
				"message": fmt.Sprintf("act bootstrap-worker: getcwd: %v", err),
			}, 3
		}
		srcStart = cwd
	}
	srcRoot, err := gitops.FindHostRepoRoot(srcStart)
	if err != nil {
		if errors.Is(err, gitops.ErrNoHostRepo) {
			return map[string]any{
				"error":   ErrNotInGit,
				"message": fmt.Sprintf("act bootstrap-worker: %v", err),
			}, 3
		}
		return map[string]any{
			"error":   ErrNoRepo,
			"message": fmt.Sprintf("act bootstrap-worker: resolve host repo: %v", err),
		}, 3
	}
	srcAct, err := gitops.FindActStatePath(srcRoot)
	if err != nil {
		// No .act/ at all under the source host repo → source state not
		// initialised. Use the spec-canonical code.
		return map[string]any{
			"error":   ErrActNotInitialized,
			"message": fmt.Sprintf("act bootstrap-worker: source %s has no .act/ — run `act init` first", srcRoot),
		}, 3
	}

	// (Pre-flight A) source `.act/.git` exists and is a git repo. We do
	// not require a full git status — the .git directory's presence is the
	// invariant the nested-repo design asserts (doctor's `nested-layout`
	// check (a)). Without it the copy would silently lose the op history.
	srcGitPath := filepath.Join(srcAct, ".git")
	if _, err := os.Stat(srcGitPath); err != nil {
		return map[string]any{
			"error":   "act_state_not_initialized",
			"message": fmt.Sprintf("act bootstrap-worker: source %s missing nested .git — run `act migrate-to-nested` or `act init`", srcAct),
		}, 3
	}

	// (Pre-flight B) target parent must exist and be writable. We don't
	// mkdir ancestors silently; the caller is expected to create the
	// worktree path (typically `git worktree add` did).
	absTarget, err := filepath.Abs(opts.Target)
	if err != nil {
		return map[string]any{
			"error":   ErrBadFlag,
			"message": fmt.Sprintf("act bootstrap-worker: abs(%q): %v", opts.Target, err),
		}, 2
	}
	if _, err := os.Stat(absTarget); err != nil {
		return map[string]any{
			"error":   ErrStatFailed,
			"message": fmt.Sprintf("act bootstrap-worker: target %s: %v", absTarget, err),
		}, 3
	}

	// (Pre-flight C) target .act/ — refuse to overwrite a non-empty
	// existing tree unless --force.
	targetAct := filepath.Join(absTarget, ".act")
	if nonEmpty, err := dirNonEmpty(targetAct); err != nil {
		return map[string]any{
			"error":   ErrStatFailed,
			"message": fmt.Sprintf("act bootstrap-worker: stat target .act/: %v", err),
		}, 3
	} else if nonEmpty && !opts.Force {
		return map[string]any{
			"error":   "target_not_empty",
			"message": fmt.Sprintf("act bootstrap-worker: %s exists and is non-empty; pass --force to overwrite", targetAct),
		}, 2
	}

	// Stage path. We copy into `.act.bootstrap/` first; the final rename
	// is the atomic commit point.
	stagingAct := filepath.Join(absTarget, ".act.bootstrap")
	// Pre-clean any leftover staging dir from a prior aborted run.
	if err := os.RemoveAll(stagingAct); err != nil {
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf("act bootstrap-worker: clear staging %s: %v", stagingAct, err),
		}, 3
	}

	// Copy the source `.act/` tree → staging.
	stats, err := copyTreeWithStats(srcAct, stagingAct)
	if err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf("act bootstrap-worker: copy %s → %s: %v", srcAct, stagingAct, err),
		}, 3
	}

	// Stamp the dispatch_hlc / meta file into the staging tree BEFORE the
	// atomic rename so a successful rename means the meta is in place.
	nodeID := readSourceNodeID(srcAct)
	now := opts.Now()
	dispatchHLC := hlc.HLC{
		Wall:    now.UTC().UnixMilli(),
		Logical: 0,
		NodeID:  nodeID,
	}
	hlcJSON, err := dispatchHLC.MarshalJSON()
	if err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrMarshalFailed,
			"message": fmt.Sprintf("act bootstrap-worker: marshal dispatch_hlc: %v", err),
		}, 3
	}
	meta := bootstrapMeta{
		SourceRoot:  srcRoot,
		DispatchHLC: string(hlcJSON),
		CopiedAt:    now.UTC().Format(rfc3339Millis),
	}
	metaBody, err := json.Marshal(meta)
	if err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrMarshalFailed,
			"message": fmt.Sprintf("act bootstrap-worker: marshal meta: %v", err),
		}, 3
	}
	if err := os.WriteFile(filepath.Join(stagingAct, BootstrapMetaFileName), metaBody, 0o644); err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf("act bootstrap-worker: write meta: %v", err),
		}, 3
	}

	// Atomic-ish rename. If `<target>/.act/` exists (we're in --force
	// territory), we remove it first; doing it under a single roof
	// preserves the "atomic from the caller's perspective" intent — there
	// is a small window where neither old nor new exists, but the only
	// way to make rename truly atomic on Unix is to swap on the same
	// inode, which os.Rename will not do across a directory delete.
	if _, err := os.Stat(targetAct); err == nil {
		if err := os.RemoveAll(targetAct); err != nil {
			_ = os.RemoveAll(stagingAct)
			return map[string]any{
				"error":   ErrWriteFailed,
				"message": fmt.Sprintf("act bootstrap-worker: remove existing %s: %v", targetAct, err),
			}, 3
		}
	}
	if err := os.Rename(stagingAct, targetAct); err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf("act bootstrap-worker: rename %s → %s: %v", stagingAct, targetAct, err),
		}, 3
	}

	// Round-trip validation: run a ready-equivalent against the new
	// target to make sure the copy produced a usable state directory.
	// We call RunReady directly (rather than shelling out) for hermetic
	// testability and to avoid coupling the validation to which binary
	// is on PATH.
	if validErr := validateBootstrappedTarget(absTarget); validErr != nil {
		// Tear down the target so we don't leave a half-valid state.
		_ = os.RemoveAll(targetAct)
		return map[string]any{
			"error":   "bootstrap_validation_failed",
			"message": fmt.Sprintf("act bootstrap-worker: validation against %s failed: %v", targetAct, validErr),
		}, 3
	}

	return BootstrapWorkerResult{
		SourceRoot:      srcRoot,
		Target:          absTarget,
		OpsCopied:       stats.OpsCopied,
		SnapshotsCopied: stats.SnapshotsCopied,
		DispatchHLC:     string(hlcJSON),
	}, 0
}

// copyStats counts the load-bearing trees so the success envelope can
// surface "ops_copied: N" without an extra scan.
type copyStats struct {
	OpsCopied       int
	SnapshotsCopied int
}

// copyTreeWithStats recursively copies src → dst, creating dst if
// necessary, and returns counts of regular files under ops/ and
// snapshots/. Symlinks are recreated as symlinks; permissions on copied
// files are preserved.
func copyTreeWithStats(src, dst string) (copyStats, error) {
	var stats copyStats
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return stats, fmt.Errorf("mkdir %s: %w", dst, err)
	}

	walkErr := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return fmt.Errorf("rel %s: %w", p, rerr)
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		mode := info.Mode()
		switch {
		case mode&os.ModeSymlink != 0:
			linkTarget, lerr := os.Readlink(p)
			if lerr != nil {
				return fmt.Errorf("readlink %s: %w", p, lerr)
			}
			// Ensure parent dir exists for the symlink.
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir parent for symlink %s: %w", target, err)
			}
			if err := os.Symlink(linkTarget, target); err != nil {
				return fmt.Errorf("symlink %s: %w", target, err)
			}
		case info.IsDir():
			if err := os.MkdirAll(target, info.Mode().Perm()|0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		default:
			if err := copyFile(p, target, info.Mode().Perm()); err != nil {
				return err
			}
			// Count files under ops/ and snapshots/ for the stats.
			if strings.HasPrefix(rel, "ops"+string(filepath.Separator)) && filepath.Ext(rel) == ".json" {
				stats.OpsCopied++
			} else if strings.HasPrefix(rel, "snapshots"+string(filepath.Separator)) && filepath.Ext(rel) == ".json" {
				stats.SnapshotsCopied++
			}
		}
		return nil
	})
	if walkErr != nil {
		return stats, walkErr
	}
	return stats, nil
}

// copyFile copies the contents of src → dst with the given mode. dst's
// parent directory is created if missing.
func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", dst, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s → %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dst, err)
	}
	return nil
}

// dirNonEmpty reports whether path exists as a directory with at least
// one entry (subject to a single ReadDir). Missing path → (false, nil).
// A non-directory at that path also counts as "exists, can't overwrite
// without --force": we surface it as non-empty so the caller hits the
// target_not_empty branch.
func dirNonEmpty(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		return true, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if err != nil && err != io.EOF {
		return false, err
	}
	return len(names) > 0, nil
}

// readSourceNodeID pulls node_id from the source `.act/config.json`. Falls
// back to a zero-hex node_id only if the read fails — the dispatch HLC is
// diagnostic-only so a missing node_id is recoverable, but in practice
// every initialized source has a config.
func readSourceNodeID(srcAct string) string {
	body, err := os.ReadFile(filepath.Join(srcAct, "config.json"))
	if err != nil {
		return "00000000"
	}
	var cfg config.Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		return "00000000"
	}
	if cfg.NodeID == "" {
		return "00000000"
	}
	return cfg.NodeID
}

// validateBootstrappedTarget runs RunReady against the bootstrapped
// target. RunReady returns code 0 on success even with an empty ready
// set; any non-zero code indicates the target is not a usable .act/
// state directory. We treat that as a hard validation failure.
//
// Two important behavioral notes:
//
//   - RunReady walks the index DB; a freshly-copied DB from the source
//     may have stale absolute-path references in pragma-recorded
//     metadata. Empirically index.db is path-agnostic in act's usage
//     (it stores op-derived state, not paths), so a copy works; if
//     this turns out to be brittle, the validation can switch to a
//     fold-only check that bypasses the index.
//   - We deliberately do not call out to ./bin/act — the validation
//     uses the same Go entry points the binary itself dispatches to,
//     so a bin/act on PATH (or absent) doesn't matter.
func validateBootstrappedTarget(targetRoot string) error {
	out, code := RunReady(targetRoot, ReadyOptions{Limit: 1, AsJSON: true})
	if code != 0 {
		return fmt.Errorf("ready exited %d: %v", code, out)
	}
	return nil
}
