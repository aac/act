// Package cli — `act harvest` subcommand.
//
// Phase 1.5 prerequisite for the upcoming coordination-plane Phase 2 work
// (docs/coordination-plane-phase2-plan.md). Mirror of `act bootstrap-worker`:
// where bootstrap-worker pushes the host's `.act/` state into a worker
// worktree at dispatch time, harvest pulls the worker's new ops back into
// the host at completion time. The pair gives the orchestrator a local-only
// fan-out/fan-in primitive that's independent of the host repo's git remote
// (Phase 2's `--from-remote` mode will extend harvest with a clone path).
//
// Surface:
//
//	act harvest <worker-path> [--dry-run] [--json]
//
// Behaviour:
//
//   - Resolve the host's `.act/` from cwd via the existing FindHostRepoRoot
//     / FindActStatePath resolvers; harvest writes happen there.
//   - Walk `<worker-path>/.act/ops/`; collect every op file (relative to
//     ops/) into a candidate set.
//   - For each candidate: compute identity by filename. Op filenames are
//     `<hlc>-<hash>-<type>.json`, where hash is content-derived, so
//     filename equality = byte equality (within one op_version). When the
//     host already has the same filename:
//   - identical bytes  → skipped, reason `already_present`
//   - divergent bytes  → error envelope `op_filename_collision` (this
//     is a corruption signal; we do NOT silently overwrite).
//     When the host does not have the filename: marked for harvest.
//   - --dry-run: emit the JSON envelope with the harvested/skipped lists
//     populated and exit; no files copied, no commit made.
//   - Otherwise: copy each harvest candidate into the host's
//     `.act/ops/<issue-id>/<month>/`, stage every copied path via
//     ActGitOps on the host's nested `.act/.git`, then run a single
//     `git commit -m "act harvest: <N> ops from <basename>"`.
//   - After commit, refold-and-rebuild the index via index.Rebuild
//     (paths.Ops). A fold failure does NOT roll back the copy or commit —
//     harvest is one-way append, and the op log on disk remains the source
//     of truth (a subsequent `act doctor` or `act list` will rebuild the
//     index from scratch). The fold error is surfaced in the JSON
//     envelope under `fold_error`.
//
// Idempotency: re-running harvest with the same worker as input is a no-op
// (all ops already harvested → empty harvested list, zero-op commit is
// suppressed). The set-difference approach gives this for free — there is
// no half-state to recover from, because copy + stage + commit is the only
// path that writes anything.
//
// Failure modes:
//
//   - Host has no `.act/`: ErrActNotInitialized, exit 3 (matches every
//     other write command's precondition class).
//   - Worker path's `.act/` is missing entirely:
//     code `worker_state_not_found`, exit 2 (bad input).
//   - Worker `.act/ops/` is missing or empty: success with empty
//     harvested list. Not an error — a freshly bootstrapped worker that
//     did nothing is a legitimate workflow.
//   - Same filename, different content: code `op_filename_collision`,
//     exit 1 (runtime corruption — distinct from the 2-vs-3 bad-input
//     spectrum).
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/index"
)

// HarvestOptions controls `act harvest`.
type HarvestOptions struct {
	// HostCWD is the directory the host-repo resolver should start from.
	// Callers usually pass os.Getwd(); tests set it explicitly to keep
	// hermetic. The host's `.act/` is discovered from this.
	HostCWD string

	// WorkerPath is the worker worktree to harvest from. Its `.act/ops/`
	// is the source of new ops; everything else (config, .git, etc.) is
	// ignored.
	WorkerPath string

	// DryRun reports what would be harvested without copying, staging,
	// committing, or re-folding.
	DryRun bool

	// AsJSON selects the JSON output rendering branch in the cmd-level
	// dispatcher; the cli-level result struct is JSON-encodable either
	// way, so this is plumbed for parity with the other commands.
	AsJSON bool

	// indexRebuild is an injectable seam for tests that need to simulate
	// a fold failure. Default (nil) uses the production index.Rebuild
	// path. The signature mirrors index.(*Index).Rebuild so we can swap
	// the function pointer without exposing an interface.
	indexRebuild func(opsDir string, idx *index.Index) error
}

// HarvestResult is the success payload. JSON shape is stable; new fields
// may be appended but existing keys are pinned (the orchestrator may grow
// to depend on them).
type HarvestResult struct {
	// HarvestedOps are the op file paths (relative to `.act/ops/`) that
	// were newly copied into the host. Sorted lexicographically for
	// determinism. On --dry-run this is the set that WOULD be copied.
	HarvestedOps []string `json:"harvested_ops"`

	// SkippedOps lists ops present in the worker that the host already
	// had. Each entry carries the relative path and a reason slug.
	SkippedOps []SkippedOp `json:"skipped_ops"`

	// FoldDiffSummary captures the index-rebuild outcome. Counts are
	// drawn from the rebuilt RenderState; on dry-run, both fields are
	// zero (we don't touch the index).
	FoldDiffSummary FoldDiffSummary `json:"fold_diff_summary"`

	// FoldError is set when index.Rebuild returned an error after a
	// successful copy + commit. Empty string when the fold succeeded
	// (or wasn't attempted because of --dry-run / zero harvested ops).
	// The presence of FoldError does NOT cause a non-zero exit — the
	// op log is the source of truth, and the index is recoverable.
	FoldError string `json:"fold_error,omitempty"`

	// CommitMessage is the message used for the harvest commit on the
	// host's nested `.act/.git`. Empty when DryRun is set or when there
	// were zero ops to harvest.
	CommitMessage string `json:"commit_message,omitempty"`

	// DryRun echoes the input flag so consumers parsing the envelope
	// don't have to track the call shape separately.
	DryRun bool `json:"dry_run"`
}

// SkippedOp is one entry in HarvestResult.SkippedOps. Reason is a stable
// slug (today only `already_present`; future reasons append).
type SkippedOp struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// FoldDiffSummary surfaces the rough effect of the harvested ops on the
// host's rendered state. Phase 2 may extend this with per-issue diffs;
// for Phase 1.5 the counts are sufficient signal for the orchestrator.
type FoldDiffSummary struct {
	IssuesIndexed int `json:"issues_indexed"`
	OpsAdded      int `json:"ops_added"`
}

// Stable error code slug for the same-filename-different-content corruption
// case. Defined locally so the constant block in errors.go stays untouched
// (per CLAUDE.md "halt on breaking changes" + the locality of this code).
const ErrOpFilenameCollision = "op_filename_collision"

// Stable error code slug for "worker .act/ doesn't exist". Distinct from
// act_not_initialized (which is "the HOST has no state") and from
// not_in_git (which is "we're not inside a git repo at all"). New code.
const ErrWorkerStateNotFound = "worker_state_not_found"

// RunHarvest is the package-public entry point. Returns either HarvestResult
// (success) or an error-envelope map (failure) plus an exit code per the
// universal table:
//
//	0  success (including idempotent no-op and dry-run)
//	2  bad input (missing worker path, worker .act/ missing)
//	3  host precondition failure (no host repo, no host .act/)
//	1  runtime failure (op_filename_collision, copy/stage/commit error)
func RunHarvest(opts HarvestOptions) (any, int) {
	if opts.WorkerPath == "" {
		return map[string]any{
			"error":   ErrBadFlag,
			"message": "act harvest: <worker-path> is required",
		}, 2
	}

	// Resolve the host's repo root and `.act/`.
	hostStart := opts.HostCWD
	if hostStart == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return map[string]any{
				"error":   ErrNoRepo,
				"message": fmt.Sprintf("act harvest: getcwd: %v", err),
			}, 3
		}
		hostStart = cwd
	}
	hostRoot, err := gitops.FindHostRepoRoot(hostStart)
	if err != nil {
		if errors.Is(err, gitops.ErrNoHostRepo) {
			return map[string]any{
				"error":   ErrNotInGit,
				"message": fmt.Sprintf("act harvest: %v", err),
			}, 3
		}
		return map[string]any{
			"error":   ErrNoRepo,
			"message": fmt.Sprintf("act harvest: resolve host repo: %v", err),
		}, 3
	}
	hostAct, err := gitops.FindActStatePath(hostRoot)
	if err != nil {
		return map[string]any{
			"error":   ErrActNotInitialized,
			"message": fmt.Sprintf("act harvest: host %s has no .act/ — run `act init` first", hostRoot),
		}, 3
	}

	// Validate the worker side. The worker path must exist, have a
	// `.act/` directory, and that `.act/` may or may not have an
	// `ops/` subtree — an empty/missing ops/ is a no-op success, not a
	// failure.
	absWorker, err := filepath.Abs(opts.WorkerPath)
	if err != nil {
		return map[string]any{
			"error":   ErrBadFlag,
			"message": fmt.Sprintf("act harvest: abs(%q): %v", opts.WorkerPath, err),
		}, 2
	}
	workerAct := filepath.Join(absWorker, ".act")
	if info, serr := os.Stat(workerAct); serr != nil || !info.IsDir() {
		return map[string]any{
			"error":   ErrWorkerStateNotFound,
			"message": fmt.Sprintf("act harvest: worker %s has no .act/ — pass a path that was seeded by `act bootstrap-worker`", absWorker),
		}, 2
	}

	// Build the candidate set: every `.json` file under
	// `<worker>/.act/ops/`. The relative key is the path under ops/ —
	// `<issue-id>/<month>/<filename>` — so the set-difference against
	// the host is exact even when the same issue had ops written in
	// two different months.
	workerOps := filepath.Join(workerAct, "ops")
	candidates, scanErr := scanOpFiles(workerOps)
	if scanErr != nil {
		return map[string]any{
			"error":   ErrOpsScanFailed,
			"message": fmt.Sprintf("act harvest: scan worker ops: %v", scanErr),
		}, 1
	}

	// Compute the difference. We sort for output determinism; the
	// loop preserves the sorted order on the harvested/skipped lists.
	sort.Strings(candidates)

	hostOps := filepath.Join(hostAct, "ops")
	harvested := []string{}
	skipped := []SkippedOp{}

	for _, rel := range candidates {
		srcPath := filepath.Join(workerOps, rel)
		dstPath := filepath.Join(hostOps, rel)
		dstInfo, statErr := os.Stat(dstPath)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				harvested = append(harvested, rel)
				continue
			}
			return map[string]any{
				"error":   ErrStatFailed,
				"message": fmt.Sprintf("act harvest: stat %s: %v", dstPath, statErr),
			}, 1
		}
		// Filename present at host. Compare bytes — if equal, skip; if
		// not, error loudly (corruption signal: HLC+hash should make
		// filenames unique-per-content).
		if dstInfo.IsDir() {
			return map[string]any{
				"error":   ErrStatFailed,
				"message": fmt.Sprintf("act harvest: %s is a directory at host, expected op file", dstPath),
			}, 1
		}
		sameContent, cerr := filesEqualByHash(srcPath, dstPath)
		if cerr != nil {
			return map[string]any{
				"error":   ErrOpsReadFailed,
				"message": fmt.Sprintf("act harvest: compare %s vs %s: %v", srcPath, dstPath, cerr),
			}, 1
		}
		if sameContent {
			skipped = append(skipped, SkippedOp{Path: rel, Reason: "already_present"})
			continue
		}
		return map[string]any{
			"error":   ErrOpFilenameCollision,
			"message": fmt.Sprintf("act harvest: op filename %q exists at host with divergent content — refusing to overwrite (this is a corruption signal: HLC + content-hash should make filenames unique-per-content)", rel),
			"details": map[string]any{
				"path":   rel,
				"worker": srcPath,
				"host":   dstPath,
			},
		}, 1
	}

	// --dry-run short-circuits before any writes. The envelope still
	// carries the same shape so consumers can diff dry-run vs real-run.
	if opts.DryRun {
		return HarvestResult{
			HarvestedOps:    harvested,
			SkippedOps:      skipped,
			FoldDiffSummary: FoldDiffSummary{},
			DryRun:          true,
		}, 0
	}

	// Zero harvested ops → no-op. We skip the commit entirely (an empty
	// `git commit` would fail with "nothing to commit"). The index is
	// not touched either; whatever state it already had remains correct.
	if len(harvested) == 0 {
		return HarvestResult{
			HarvestedOps:    harvested,
			SkippedOps:      skipped,
			FoldDiffSummary: FoldDiffSummary{},
			DryRun:          false,
		}, 0
	}

	// Copy each harvested op into the host. We do this before any
	// staging so a copy error doesn't leave the index half-updated.
	copied := []string{} // host-relative paths for staging
	for _, rel := range harvested {
		srcPath := filepath.Join(workerOps, rel)
		dstPath := filepath.Join(hostOps, rel)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return map[string]any{
				"error":   ErrWriteFailed,
				"message": fmt.Sprintf("act harvest: mkdir parent of %s: %v", dstPath, err),
			}, 1
		}
		if err := copyFileForHarvest(srcPath, dstPath); err != nil {
			return map[string]any{
				"error":   ErrWriteFailed,
				"message": fmt.Sprintf("act harvest: copy %s → %s: %v", srcPath, dstPath, err),
			}, 1
		}
		copied = append(copied, dstPath)
	}

	// Stage and commit on the host's nested `.act/.git`. ActGitOps is
	// constructed against the host's `.act/` directory (paths.Root),
	// matching how every other write command in this package commits.
	paths := config.Layout(hostRoot)
	gops := gitops.NewActGitOps(paths.Root)
	for _, dstPath := range copied {
		if err := gops.StageOpFile(dstPath); err != nil {
			return map[string]any{
				"error":   ErrWriteFailed,
				"message": fmt.Sprintf("act harvest: stage %s: %v", dstPath, err),
			}, 1
		}
	}
	commitMsg := fmt.Sprintf("act harvest: %d ops from %s", len(harvested), filepath.Base(absWorker))
	if err := gops.Commit(commitMsg); err != nil {
		return map[string]any{
			"error":   ErrWriteFailed,
			"message": fmt.Sprintf("act harvest: commit: %v", err),
		}, 1
	}

	// Re-fold via index rebuild. A failure here does NOT roll back the
	// copy/commit — harvest is one-way append. We surface the fold error
	// in the JSON envelope so the orchestrator knows to retry/rebuild.
	result := HarvestResult{
		HarvestedOps:    harvested,
		SkippedOps:      skipped,
		FoldDiffSummary: FoldDiffSummary{OpsAdded: len(harvested)},
		CommitMessage:   commitMsg,
		DryRun:          false,
	}

	rebuild := opts.indexRebuild
	if rebuild == nil {
		rebuild = func(opsDir string, idx *index.Index) error {
			return idx.Rebuild(opsDir)
		}
	}
	idx, oerr := index.Open(paths.IndexDB)
	if oerr != nil {
		result.FoldError = fmt.Sprintf("index open: %v", oerr)
		return result, 0
	}
	defer func() { _ = idx.Close() }()
	if serr := idx.ApplySchema(); serr != nil {
		result.FoldError = fmt.Sprintf("apply schema: %v", serr)
		return result, 0
	}
	if rerr := rebuild(paths.Ops, idx); rerr != nil {
		result.FoldError = rerr.Error()
		return result, 0
	}
	// Best-effort count of issues now indexed; surface as fold_diff_summary.
	// We do not block on a query failure here — the fold succeeded, and
	// the count is diagnostic only.
	if n, qerr := countIndexedIssues(idx); qerr == nil {
		result.FoldDiffSummary.IssuesIndexed = n
	}
	return result, 0
}

// scanOpFiles walks opsDir and returns the relative paths of every regular
// `.json` file under it (relative to opsDir). Missing opsDir is treated as
// an empty result (the no-op-success path for an empty worker).
func scanOpFiles(opsDir string) ([]string, error) {
	if _, err := os.Stat(opsDir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	walkErr := filepath.Walk(opsDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".json") {
			return nil
		}
		rel, rerr := filepath.Rel(opsDir, p)
		if rerr != nil {
			return rerr
		}
		// Skip hidden files (e.g. .gitkeep is filtered by the .json
		// extension check above; this is belt-and-braces for future
		// dotfiles).
		if strings.HasPrefix(filepath.Base(rel), ".") {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

// filesEqualByHash compares two files by SHA-256 of their bytes. We hash
// rather than byte-loop so a very large op file (compaction snapshot,
// future bundled op) doesn't pin both files in memory simultaneously.
func filesEqualByHash(a, b string) (bool, error) {
	ha, err := hashFile(a)
	if err != nil {
		return false, err
	}
	hb, err := hashFile(b)
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFileForHarvest copies src → dst, preserving the source's mode bits.
// Distinct from bootstrap_worker.go's copyFile to keep harvest's surface
// hermetic (different error wrapping, different mode handling — we don't
// want to share a helper that grows divergent requirements over time).
func copyFileForHarvest(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("copy %s → %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dst, err)
	}
	return nil
}

// countIndexedIssues runs a single COUNT(*) against the index's issues
// table. Index.* doesn't expose a dedicated count, so we hand-roll the
// SQL via the underlying *sql.DB exposed through the Query helper used
// elsewhere. If the index doesn't yet have a writable handle (callers
// haven't rebuilt), we return (0, nil) so the harvest envelope still
// emits.
func countIndexedIssues(idx *index.Index) (int, error) {
	rows, err := idx.ListAll(index.Filter{})
	if err != nil {
		return 0, err
	}
	return len(rows), nil
}
