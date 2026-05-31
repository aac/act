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
//   - Copy the entire `.act/` tree (ops/, snapshots/, imports/,
//     index.db if present, config.json, .git) into `<target>/.act.bootstrap/`,
//     then atomic-rename to `<target>/.act/`. On any failure mid-copy, the
//     `.act.bootstrap/` tree is torn down so we never leave a partial `.act/`.
//   - The host's `.act/hooks/` directory is deliberately NOT copied (act-43cf99).
//     Host hooks (e.g. the act repo's close hook that runs `go vet ./...`)
//     assume the host project's working-tree context and break on a worker
//     dispatched into a non-host repo. Per-worker hook installation is a
//     separate concern; the worker still gets the close-op trailer + push
//     semantics from its nested .act/.git, only the optional gating hook is
//     omitted.
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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/index"
)

// BootstrapWorkerOptions controls `act bootstrap-worker`.
type BootstrapWorkerOptions struct {
	// SourceCWD is the directory the resolver should start its host-repo
	// walk from in the cwd-source mode. Ignored when FromRemoteURL is
	// set. Callers usually pass os.Getwd(); tests set it explicitly.
	SourceCWD string

	// FromRemoteURL, when non-empty, switches the command into the Phase
	// 2 ticket 7 "from-remote" mode: instead of copying the source
	// `.act/` from cwd, the command runs `git clone --depth 1
	// <FromRemoteURL>` into a staging dir and atomic-renames it to
	// <Target>/.act/. Mutually exclusive with the cwd-source path.
	FromRemoteURL string

	// FromCWDSourcePath, when non-empty, switches the command into the
	// worker-cwd bootstrap mode (act-40fce0). This is the source/target
	// inversion of the default cwd-source mode: the WORKER runs the
	// command from inside its freshly-created worktree, names the
	// ORCHESTRATOR's repo (or `.act/`) path as the source, and the target
	// is the worker's own cwd (or an explicit Target). It exists because a
	// worktree created DURING an Agent dispatch does not exist yet when the
	// orchestrator would otherwise run `act bootstrap-worker <target>` — so
	// the orchestrator cannot bootstrap a not-yet-existing target, and the
	// only previous workaround was a raw `cp -r <orchestrator>/.act .` from
	// inside the worktree.
	//
	// Critically, this mode does NOT copy a live `index.db` (or its
	// `-wal` / `-shm` / `-journal` sidecars). The orchestrator may still
	// have the index open; copying a live SQLite file in-flight is the
	// fragile interaction behind the orchestrate-worker silent-data-loss
	// bug. Instead it copies only the op log + config + snapshots +
	// imports + the nested `.act/.git`, then REBUILDS `index.db` locally
	// from the copied op log. The index is a pure derived cache, so a
	// local rebuild is always correct and never depends on the source's
	// in-memory SQLite state.
	//
	// Mutually exclusive with FromRemoteURL and with the default
	// cwd-source mode (which is selected when both FromRemoteURL and
	// FromCWDSourcePath are empty).
	FromCWDSourcePath string

	// Target is the path under which the new `.act/` will land. The
	// command creates `<target>/.act/`; the parent path must exist (we
	// don't try to mkdir arbitrary ancestors — that's the caller's job).
	//
	// In the FromCWDSourcePath (worker-cwd) mode, an empty Target means
	// "use the process cwd" — the worker is bootstrapping itself in place.
	Target string

	// Force makes the command replace a non-empty existing `<target>/.act/`.
	// Empty or missing target `.act/` is allowed without --force.
	Force bool

	// AsJSON selects the JSON output rendering branch (the caller's
	// concern; this struct is plumbed for parity with other commands).
	AsJSON bool

	// TimeoutSeconds, when > 0, overrides act.bootstrapTimeoutSeconds for
	// the from-remote clone. Tests inject a small value (e.g. 1) to
	// drive the timeout path against a stalled-clone fixture. When 0,
	// the from-remote path reads act.bootstrapTimeoutSeconds from
	// SourceCWD's nested .act/.git/config; if unset, falls back to
	// config.DefaultEnableDefaults().BootstrapTimeoutSeconds.
	TimeoutSeconds int

	// Now is an injectable clock for tests; default time.Now.
	Now func() time.Time

	// CmdName is the command name used as the prefix in error-envelope
	// `message` strings (e.g. "act state import"). Empty defaults to the
	// historical "act bootstrap-worker" so direct-API callers and the
	// deprecation alias keep their existing message prefixes. The
	// directory-scoped `act state import` dispatcher sets this so the
	// user-visible error prose carries no worktree vocabulary (MF-D).
	CmdName string
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
	cmd := opts.CmdName
	if cmd == "" {
		cmd = "act bootstrap-worker"
	}

	if opts.FromRemoteURL != "" && opts.FromCWDSourcePath != "" {
		return map[string]any{
			"error":   ErrBadFlag,
			"message": cmd + ": --from-remote and --from-cwd are mutually exclusive",
		}, 2
	}

	// Worker-cwd mode (act-40fce0): the worker runs from inside its own
	// worktree and names the orchestrator path as the source; target
	// defaults to cwd. This branch must precede the generic Target=="" guard
	// because the empty-Target default (use cwd) is legitimate here.
	if opts.FromCWDSourcePath != "" {
		return runBootstrapFromCWD(opts)
	}

	if opts.Target == "" {
		return map[string]any{
			"error":   ErrBadFlag,
			"message": cmd + ": <target-path> is required",
		}, 2
	}

	// From-remote mode (Phase 2 ticket 7) short-circuits the cwd-source
	// path: we don't resolve a local .act/, we clone the URL instead.
	if opts.FromRemoteURL != "" {
		return runBootstrapFromRemote(opts)
	}

	// Resolve the source host repo root and its .act/.
	srcStart := opts.SourceCWD
	if srcStart == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return map[string]any{
				"error":   ErrNoRepo,
				"message": fmt.Sprintf(cmd+": getcwd: %v", err),
			}, 3
		}
		srcStart = cwd
	}
	srcRoot, err := gitops.FindHostRepoRoot(srcStart)
	if err != nil {
		if errors.Is(err, gitops.ErrNoHostRepo) {
			return map[string]any{
				"error":   ErrNotInGit,
				"message": fmt.Sprintf(cmd+": %v", err),
			}, 3
		}
		return map[string]any{
			"error":   ErrNoRepo,
			"message": fmt.Sprintf(cmd+": resolve host repo: %v", err),
		}, 3
	}
	srcAct, err := gitops.FindActStatePath(srcRoot)
	if err != nil {
		// No .act/ at all under the source host repo → source state not
		// initialised. Use the spec-canonical code.
		return map[string]any{
			"error":   ErrActNotInitialized,
			"message": fmt.Sprintf(cmd+": source %s has no .act/ — run `act init` first", srcRoot),
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
			"message": fmt.Sprintf(cmd+": source %s missing nested .git — run `act migrate-to-nested` or `act init`", srcAct),
		}, 3
	}

	// (Pre-flight B) target parent must exist and be writable. We don't
	// mkdir ancestors silently; the caller is expected to create the
	// worktree path (typically `git worktree add` did).
	absTarget, err := filepath.Abs(opts.Target)
	if err != nil {
		return map[string]any{
			"error":   ErrBadFlag,
			"message": fmt.Sprintf(cmd+": abs(%q): %v", opts.Target, err),
		}, 2
	}
	if _, err := os.Stat(absTarget); err != nil {
		return map[string]any{
			"error":   ErrStatFailed,
			"message": fmt.Sprintf(cmd+": target %s: %v", absTarget, err),
		}, 3
	}

	// (Pre-flight C) target .act/ — refuse to overwrite a non-empty
	// existing tree unless --force.
	targetAct := filepath.Join(absTarget, ".act")
	if nonEmpty, err := dirNonEmpty(targetAct); err != nil {
		return map[string]any{
			"error":   ErrStatFailed,
			"message": fmt.Sprintf(cmd+": stat target .act/: %v", err),
		}, 3
	} else if nonEmpty && !opts.Force {
		return map[string]any{
			"error":   ErrTargetNotEmpty,
			"message": fmt.Sprintf(cmd+": %s exists and is non-empty; pass --force to overwrite", targetAct),
		}, 2
	}

	// Stage path. We copy into `.act.bootstrap/` first; the final rename
	// is the atomic commit point.
	stagingAct := filepath.Join(absTarget, ".act.bootstrap")
	// Pre-clean any leftover staging dir from a prior aborted run.
	if err := os.RemoveAll(stagingAct); err != nil {
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf(cmd+": clear staging %s: %v", stagingAct, err),
		}, 3
	}

	// Copy the source `.act/` tree → staging.
	stats, err := copyTreeWithStats(srcAct, stagingAct)
	if err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf(cmd+": copy %s → %s: %v", srcAct, stagingAct, err),
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
			"message": fmt.Sprintf(cmd+": marshal dispatch_hlc: %v", err),
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
			"message": fmt.Sprintf(cmd+": marshal meta: %v", err),
		}, 3
	}
	if err := os.WriteFile(filepath.Join(stagingAct, BootstrapMetaFileName), metaBody, 0o644); err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf(cmd+": write meta: %v", err),
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
				"message": fmt.Sprintf(cmd+": remove existing %s: %v", targetAct, err),
			}, 3
		}
	}
	if err := os.Rename(stagingAct, targetAct); err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf(cmd+": rename %s → %s: %v", stagingAct, targetAct, err),
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
			"message": fmt.Sprintf(cmd+": validation against %s failed: %v", targetAct, validErr),
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

// indexFileBasenames is the set of `.act/` top-level basenames that make up
// the live SQLite index and its journals. The worker-cwd bootstrap mode
// (act-40fce0) skips these during copy and rebuilds the index locally,
// because copying a live/locked index.db in-flight is the fragile
// interaction behind the orchestrate-worker silent-data-loss bug.
var indexFileBasenames = map[string]bool{
	"index.db":         true,
	"index.db-wal":     true,
	"index.db-shm":     true,
	"index.db-journal": true,
}

// copyTreeWithStats recursively copies src → dst, creating dst if
// necessary, and returns counts of regular files under ops/ and
// snapshots/. Symlinks are recreated as symlinks; permissions on copied
// files are preserved.
func copyTreeWithStats(src, dst string) (copyStats, error) {
	return copyTreeWithStatsOpts(src, dst, false)
}

// copyTreeWithStatsOpts is copyTreeWithStats with an explicit
// excludeIndex toggle. When excludeIndex is true, the top-level
// `index.db` and its `-wal` / `-shm` / `-journal` sidecars are NOT copied
// — the caller is responsible for rebuilding the index locally from the
// copied op log. Used by the worker-cwd bootstrap mode (act-40fce0) to
// avoid copying a live SQLite file the orchestrator may still have open.
func copyTreeWithStatsOpts(src, dst string, excludeIndex bool) (copyStats, error) {
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
		// Skip the host's hooks/ directory entirely (act-43cf99).
		// Host-specific close hooks (e.g. one that runs `go vet`) assume
		// the host project's working tree and break workers dispatched
		// into unrelated repos. The whole subtree is omitted, both the
		// directory entry and its contents.
		if rel == "hooks" || strings.HasPrefix(rel, "hooks"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Worker-cwd mode: skip the live index.db + journals (act-40fce0).
		// These are top-level files under `.act/`; the rebuild happens
		// after the copy from the copied op log.
		if excludeIndex && !info.IsDir() && indexFileBasenames[rel] {
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

// runBootstrapFromCWD handles the worker-cwd mode (act-40fce0):
//
//	act bootstrap-worker --from-cwd <orchestrator-path> [<target>] [--force]
//
// Source/target inversion of the default cwd-source mode. The WORKER runs
// this from inside its freshly-created worktree. The source is the
// orchestrator's repo root or `.act/` path (FromCWDSourcePath); the target
// is opts.Target, defaulting to the process cwd when empty (the common
// "bootstrap myself in place" call shape — the worktree was just created and
// the worker's cwd is its root).
//
// The load-bearing difference from every other bootstrap mode: it does NOT
// copy a live `index.db` (or its journals). It copies only the op log,
// config, snapshots, imports, and the nested `.act/.git`, then REBUILDS the
// index locally from the copied op log. This closes the orchestrate-worker
// silent-data-loss class — a raw `cp -r` of a live, orchestrator-open
// index.db produced a fragile concurrent-SQLite / stale-index interaction;
// rebuilding the derived cache from the source-of-truth op log is always
// correct and never depends on the source's in-memory SQLite state.
func runBootstrapFromCWD(opts BootstrapWorkerOptions) (any, int) {
	cmd := opts.CmdName
	if cmd == "" {
		cmd = "act bootstrap-worker"
	}
	// Resolve the SOURCE: the orchestrator path the worker named. Accept
	// either a repo root or a `.act/` directory; resolve to the .act/ tree.
	srcArg := opts.FromCWDSourcePath
	srcRoot, err := gitops.FindHostRepoRoot(srcArg)
	if err != nil {
		// The arg may itself be (or be inside) a `.act/`; FindHostRepoRoot
		// skips nested .act/.git, so a bare `.act/` path that has no
		// surrounding host repo surfaces ErrStandaloneActUnsupported. Fall
		// back to treating srcArg as a host root whose `.act/` we look for
		// directly so the worker can name either shape.
		if abs, aerr := filepath.Abs(srcArg); aerr == nil {
			if _, serr := os.Stat(filepath.Join(abs, ".act")); serr == nil {
				srcRoot = abs
			} else if filepath.Base(abs) == ".act" {
				// They named the .act/ dir itself; its parent is the root.
				srcRoot = filepath.Dir(abs)
			} else {
				return map[string]any{
					"error":   ErrNoRepo,
					"message": fmt.Sprintf(cmd+": --from-cwd %s: resolve source repo: %v", srcArg, err),
				}, 3
			}
		} else {
			return map[string]any{
				"error":   ErrNoRepo,
				"message": fmt.Sprintf(cmd+": --from-cwd %s: abs: %v", srcArg, aerr),
			}, 3
		}
	}
	srcAct, err := gitops.FindActStatePath(srcRoot)
	if err != nil {
		return map[string]any{
			"error":   ErrActNotInitialized,
			"message": fmt.Sprintf(cmd+": --from-cwd source %s has no .act/ — run `act init` first", srcRoot),
		}, 3
	}

	// Pre-flight: source nested .git must exist (same invariant as the
	// default cwd-source mode — without it the copy silently loses op
	// history).
	if _, err := os.Stat(filepath.Join(srcAct, ".git")); err != nil {
		return map[string]any{
			"error":   "act_state_not_initialized",
			"message": fmt.Sprintf(cmd+": --from-cwd source %s missing nested .git — run `act migrate-to-nested` or `act init`", srcAct),
		}, 3
	}

	// Resolve the TARGET: opts.Target, defaulting to cwd.
	targetArg := opts.Target
	if targetArg == "" {
		cwd, gerr := os.Getwd()
		if gerr != nil {
			return map[string]any{
				"error":   ErrNoRepo,
				"message": fmt.Sprintf(cmd+": --from-cwd: getcwd: %v", gerr),
			}, 3
		}
		targetArg = cwd
	}
	absTarget, err := filepath.Abs(targetArg)
	if err != nil {
		return map[string]any{
			"error":   ErrBadFlag,
			"message": fmt.Sprintf(cmd+": --from-cwd: abs(%q): %v", targetArg, err),
		}, 2
	}
	if _, err := os.Stat(absTarget); err != nil {
		return map[string]any{
			"error":   ErrStatFailed,
			"message": fmt.Sprintf(cmd+": --from-cwd target %s: %v", absTarget, err),
		}, 3
	}

	// Refuse to bootstrap from a source into itself — copying `.act/` onto
	// itself would be a destructive no-op surprise.
	targetAct := filepath.Join(absTarget, ".act")
	if canonicalPath(targetAct) == canonicalPath(srcAct) {
		return map[string]any{
			"error":   ErrBadFlag,
			"message": fmt.Sprintf(cmd+": --from-cwd source and target resolve to the same .act/ (%s)", canonicalPath(srcAct)),
		}, 2
	}

	// Pre-flight: refuse non-empty target .act/ unless --force.
	if nonEmpty, err := dirNonEmpty(targetAct); err != nil {
		return map[string]any{
			"error":   ErrStatFailed,
			"message": fmt.Sprintf(cmd+": --from-cwd stat target .act/: %v", err),
		}, 3
	} else if nonEmpty && !opts.Force {
		return map[string]any{
			"error":   ErrTargetNotEmpty,
			"message": fmt.Sprintf(cmd+": %s exists and is non-empty; pass --force to overwrite", targetAct),
			"details": map[string]any{"target": targetAct},
		}, 2
	}

	// Stage path under the target.
	stagingAct := filepath.Join(absTarget, ".act.bootstrap")
	if err := os.RemoveAll(stagingAct); err != nil {
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf(cmd+": --from-cwd clear staging %s: %v", stagingAct, err),
		}, 3
	}

	// Copy the source `.act/` → staging, EXCLUDING the live index.db and
	// its journals (the whole point of this mode).
	stats, err := copyTreeWithStatsOpts(srcAct, stagingAct, true)
	if err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf(cmd+": --from-cwd copy %s → %s: %v", srcAct, stagingAct, err),
		}, 3
	}

	// Stamp the dispatch_hlc / meta file into staging BEFORE the rename.
	nodeID := readSourceNodeID(srcAct)
	now := opts.Now()
	dispatchHLC := hlc.HLC{Wall: now.UTC().UnixMilli(), Logical: 0, NodeID: nodeID}
	hlcJSON, err := dispatchHLC.MarshalJSON()
	if err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrMarshalFailed,
			"message": fmt.Sprintf(cmd+": --from-cwd marshal dispatch_hlc: %v", err),
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
			"message": fmt.Sprintf(cmd+": --from-cwd marshal meta: %v", err),
		}, 3
	}
	if err := os.WriteFile(filepath.Join(stagingAct, BootstrapMetaFileName), metaBody, 0o644); err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf(cmd+": --from-cwd write meta: %v", err),
		}, 3
	}

	// Atomic-ish rename of staging → target .act/.
	if _, err := os.Stat(targetAct); err == nil {
		if err := os.RemoveAll(targetAct); err != nil {
			_ = os.RemoveAll(stagingAct)
			return map[string]any{
				"error":   ErrWriteFailed,
				"message": fmt.Sprintf(cmd+": --from-cwd remove existing %s: %v", targetAct, err),
			}, 3
		}
	}
	if err := os.Rename(stagingAct, targetAct); err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf(cmd+": --from-cwd rename %s → %s: %v", stagingAct, targetAct, err),
		}, 3
	}

	// REBUILD the index locally from the copied op log. This is the
	// load-bearing replacement for copying the live index.db: the derived
	// cache is regenerated from the source-of-truth op log, so the worker
	// never inherits the orchestrator's in-flight SQLite state. A rebuild
	// failure tears the target down — an unusable index is a hard failure
	// for a worker that will immediately run read commands.
	idxPath := filepath.Join(targetAct, "index.db")
	idx, oerr := index.Open(idxPath)
	if oerr != nil {
		_ = os.RemoveAll(targetAct)
		return map[string]any{
			"error":   ErrIndexOpenFailed,
			"message": fmt.Sprintf(cmd+": --from-cwd open local index %s: %v", idxPath, oerr),
		}, 3
	}
	if rerr := idx.Rebuild(filepath.Join(targetAct, "ops")); rerr != nil {
		_ = idx.Close()
		_ = os.RemoveAll(targetAct)
		return map[string]any{
			"error":   ErrIndexRebuildFail,
			"message": fmt.Sprintf(cmd+": --from-cwd rebuild local index: %v", rerr),
		}, 3
	}
	_ = idx.Close()

	// Round-trip validation against the new target.
	if validErr := validateBootstrappedTarget(absTarget); validErr != nil {
		_ = os.RemoveAll(targetAct)
		return map[string]any{
			"error":   "bootstrap_validation_failed",
			"message": fmt.Sprintf(cmd+": --from-cwd validation against %s failed: %v", targetAct, validErr),
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

// runBootstrapFromRemote handles `act bootstrap-worker --from-remote
// <url> <target>`. Phase 2 ticket 7 (act-0480c9). The shape mirrors the
// cwd-source path: pre-flight target, stage clone into .act.bootstrap/,
// rename, stamp role=worker, validate via ready, return the same
// BootstrapWorkerResult success envelope.
//
// Timeout: a context.WithTimeout drives the underlying git-clone
// subprocess via exec.CommandContext, so a stalled clone is killed (the
// Go runtime sends SIGKILL when the context fires) without leaving an
// orphan process. The deadline value comes from opts.TimeoutSeconds
// when > 0, otherwise from act.bootstrapTimeoutSeconds in the
// orchestrator's nested .act/.git/config (best-effort read; missing
// SourceCWD or missing config key both fall through to the default).
func runBootstrapFromRemote(opts BootstrapWorkerOptions) (any, int) {
	cmd := opts.CmdName
	if cmd == "" {
		cmd = "act bootstrap-worker"
	}
	absTarget, err := filepath.Abs(opts.Target)
	if err != nil {
		return map[string]any{
			"error":   ErrBadFlag,
			"message": fmt.Sprintf(cmd+": abs(%q): %v", opts.Target, err),
		}, 2
	}
	if _, err := os.Stat(absTarget); err != nil {
		return map[string]any{
			"error":   ErrStatFailed,
			"message": fmt.Sprintf(cmd+": target %s: %v", absTarget, err),
		}, 3
	}

	// Pre-flight: refuse non-empty .act/ unless --force.
	targetAct := filepath.Join(absTarget, ".act")
	if nonEmpty, err := dirNonEmpty(targetAct); err != nil {
		return map[string]any{
			"error":   ErrStatFailed,
			"message": fmt.Sprintf(cmd+": stat target .act/: %v", err),
		}, 3
	} else if nonEmpty && !opts.Force {
		return map[string]any{
			"error":   ErrTargetNotEmpty,
			"message": fmt.Sprintf(cmd+": %s exists and is non-empty; pass --force to overwrite", targetAct),
			"details": map[string]any{
				"target": targetAct,
			},
		}, 2
	}

	// Resolve the timeout.
	timeoutSec := resolveBootstrapTimeoutSeconds(opts)

	// Stage path.
	stagingAct := filepath.Join(absTarget, ".act.bootstrap")
	if err := os.RemoveAll(stagingAct); err != nil {
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf(cmd+": clear staging %s: %v", stagingAct, err),
		}, 3
	}

	// Run `git clone --depth 1 <url> <staging>` with a hard wall-clock
	// timeout. exec.CommandContext sends SIGKILL when the context fires,
	// which terminates the clone subprocess deterministically. The
	// alternative (Cmd.Process.Kill on a timer) races with normal
	// completion and leaks the goroutine when clone finishes first;
	// exec.CommandContext is the canonical Go answer here.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cloneCmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", opts.FromRemoteURL, stagingAct)
	cloneOut, cloneErr := cloneCmd.CombinedOutput()
	if cloneErr != nil {
		// Tear down any partial staging dir before deciding which
		// error code to surface — both branches benefit from a clean
		// filesystem.
		_ = os.RemoveAll(stagingAct)

		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return map[string]any{
				"error":   ErrBootstrapTimeout,
				"message": fmt.Sprintf(cmd+": clone %s exceeded %ds budget", opts.FromRemoteURL, timeoutSec),
				"details": map[string]any{
					"timeout_seconds": timeoutSec,
					"url":             opts.FromRemoteURL,
				},
			}, 4
		}
		return map[string]any{
			"error":   ErrRemoteUnreachable,
			"message": fmt.Sprintf(cmd+": clone %s: %v", opts.FromRemoteURL, cloneErr),
			"details": map[string]any{
				"url":         opts.FromRemoteURL,
				"stderr_tail": CaptureStderrTail(string(cloneOut)),
			},
		}, 3
	}

	// Sanity-check the staging tree shape: a successful clone must have
	// produced a nested .git directory. Without it, the rename would
	// land an unusable .act/ at the target.
	if _, err := os.Stat(filepath.Join(stagingAct, ".git")); err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf(cmd+": cloned tree at %s missing .git: %v", stagingAct, err),
		}, 3
	}

	// Strip the host's `hooks/` directory from the cloned working tree
	// (act-43cf99). A `git clone` of `.act/.git` brings the host's
	// committed `.act/hooks/close` (and any other tracked hooks) into the
	// worker, which then runs that hook on `act close`. Host hooks
	// typically assume the host project's working-tree context (e.g. the
	// act repo's close hook runs `go vet ./...`) and fail on a worker
	// dispatched into a non-host repo. Same rationale as the cwd-source
	// path; per-worker hook installation is a separate concern. We do
	// not bother committing the deletion to the nested repo — the
	// missing files on disk are enough to no-op the close-hook firing,
	// and a worker's nested .git tree is short-lived.
	if err := os.RemoveAll(filepath.Join(stagingAct, "hooks")); err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf(cmd+": strip hooks/ from clone: %v", err),
		}, 3
	}

	// Atomic-ish rename. If `<target>/.act/` exists (we're in --force
	// territory), remove it first.
	if _, err := os.Stat(targetAct); err == nil {
		if err := os.RemoveAll(targetAct); err != nil {
			_ = os.RemoveAll(stagingAct)
			return map[string]any{
				"error":   ErrWriteFailed,
				"message": fmt.Sprintf(cmd+": remove existing %s: %v", targetAct, err),
			}, 3
		}
	}
	if err := os.Rename(stagingAct, targetAct); err != nil {
		_ = os.RemoveAll(stagingAct)
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf(cmd+": rename %s → %s: %v", stagingAct, targetAct, err),
		}, 3
	}

	// Stamp act.role=worker into the new clone's nested .git/config.
	// This is the load-bearing post-bootstrap action — without it the
	// upstream-sync trigger doesn't know it's running on a worker.
	roleConfigPath := config.ActGitConfigPath(targetAct)
	if err := config.SetGitConfig(roleConfigPath, config.ActRoleKey, string(config.RoleWorker)); err != nil {
		_ = os.RemoveAll(targetAct)
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf(cmd+": set %s=%s: %v", config.ActRoleKey, config.RoleWorker, err),
		}, 3
	}

	// Round-trip validation against the new target.
	if validErr := validateBootstrappedTarget(absTarget); validErr != nil {
		_ = os.RemoveAll(targetAct)
		return map[string]any{
			"error":   "bootstrap_validation_failed",
			"message": fmt.Sprintf(cmd+": validation against %s failed: %v", targetAct, validErr),
		}, 3
	}

	// Compute ops/snapshots counts for the success envelope.
	stats := countActFiles(targetAct)

	// Derive a dispatch_hlc from the cloned tree's node_id for envelope
	// parity with the cwd-source path.
	now := opts.Now()
	dispatchHLC := hlc.HLC{
		Wall:    now.UTC().UnixMilli(),
		Logical: 0,
		NodeID:  readSourceNodeID(targetAct),
	}
	hlcJSON, _ := dispatchHLC.MarshalJSON()

	return BootstrapWorkerResult{
		SourceRoot:      opts.FromRemoteURL,
		Target:          absTarget,
		OpsCopied:       stats.OpsCopied,
		SnapshotsCopied: stats.SnapshotsCopied,
		DispatchHLC:     string(hlcJSON),
	}, 0
}

// resolveBootstrapTimeoutSeconds picks the timeout value for a
// from-remote clone. Order of precedence:
//
//  1. opts.TimeoutSeconds when > 0 (test injection point).
//  2. act.bootstrapTimeoutSeconds from the orchestrator's nested
//     .act/.git/config when opts.SourceCWD is set and the key is
//     readable.
//  3. config.DefaultEnableDefaults().BootstrapTimeoutSeconds.
func resolveBootstrapTimeoutSeconds(opts BootstrapWorkerOptions) int {
	if opts.TimeoutSeconds > 0 {
		return opts.TimeoutSeconds
	}
	if opts.SourceCWD != "" {
		// Best-effort: locate the source .act/, read the key, ignore
		// errors. The default fallback below handles any failure path.
		if root, err := gitops.FindHostRepoRoot(opts.SourceCWD); err == nil {
			if act, err := gitops.FindActStatePath(root); err == nil {
				cfgPath := config.ActGitConfigPath(act)
				if v, err := config.GetGitConfig(cfgPath, config.BootstrapTimeoutSecondsKey); err == nil && v != "" {
					if n, err := strconv.Atoi(v); err == nil && n > 0 {
						return n
					}
				}
			}
		}
	}
	return config.DefaultEnableDefaults().BootstrapTimeoutSeconds
}

// countActFiles is a minimal stat-only walk over <act>/ops and
// <act>/snapshots that returns the JSON-file counts the success
// envelope surfaces. Mirrors copyTreeWithStats's accounting but doesn't
// require a copy.
func countActFiles(actPath string) copyStats {
	var stats copyStats
	_ = filepath.Walk(filepath.Join(actPath, "ops"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if filepath.Ext(p) == ".json" {
			stats.OpsCopied++
		}
		return nil
	})
	_ = filepath.Walk(filepath.Join(actPath, "snapshots"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if filepath.Ext(p) == ".json" {
			stats.SnapshotsCopied++
		}
		return nil
	})
	return stats
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
