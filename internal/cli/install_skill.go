package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/aac/act/skills"
)

// InstallSkillOptions controls `act install-skill` behaviour. Dest is the
// target skills directory on disk; when empty the destination is derived from
// Target (default "claude" → ~/.claude/skills/act, "codex" →
// ~/.codex/skills/act). When Dest is non-empty it overrides Target. Force
// overwrites existing files that differ from the embedded copy without
// prompting. AsJSON selects the output rendering branch. Check switches to
// read-only mode: compare embedded vs installed bytes, never write, exit 0 if
// everything matches and 1 if anything drifts or is missing.
type InstallSkillOptions struct {
	Dest   string
	Target string
	Force  bool
	AsJSON bool
	Check  bool
}

// InstallSkillResult is the success payload. Written records absolute
// paths of files that were freshly written (new) or rewritten (changed
// with --force). Skipped records files whose contents already match the
// embedded copy verbatim. Refused records files that already exist with
// different contents and were left untouched because --force was not set;
// when Refused is non-empty the exit code is non-zero so an agent can
// detect the partial state.
type InstallSkillResult struct {
	Dest    string   `json:"dest"`
	Written []string `json:"written"`
	Skipped []string `json:"skipped"`
	Refused []string `json:"refused,omitempty"`
}

// CheckSkillResult is the payload returned by `act install-skill --check`.
// It mirrors the per-file classification of the install path but in a
// read-only shape: every embedded file lands in exactly one of Match,
// Drift, or Missing. Exit code is 0 iff Drift and Missing are both empty.
type CheckSkillResult struct {
	Dest    string   `json:"dest"`
	Version string   `json:"version"`
	Match   []string `json:"match"`
	Drift   []string `json:"drift"`
	Missing []string `json:"missing"`
}

// RunInstallSkill writes the embedded skill tree (SKILL.md plus
// references/*.md) into opts.Dest, creating parent directories as
// needed. The operation is idempotent: re-running with no source change
// and no destination change is a no-op. The file-existence policy is:
//
//   - destination missing → write, record under Written.
//   - destination present and bytes-equal to embedded → skip, record under Skipped.
//   - destination present and bytes-differ:
//   - if opts.Force: overwrite, record under Written.
//   - else: leave untouched, record under Refused; exit code becomes 1
//     so the caller learns the install was partial.
//
// The directory is never wiped — files in opts.Dest that are not part of
// the embedded tree (e.g. user-authored extensions, references the user
// added themselves) are left alone. This matches the principle "the act
// repo owns the canonical skill; users may extend, but install never
// destroys."
//
// Returns a JSON-encodable value (InstallSkillResult on success or
// partial, error envelope map on hard failure) plus a process exit code:
// 0 = clean install, 1 = partial (refused files present), 2 = bad input
// (unresolvable home dir or unknown --target), 3 = filesystem error.
func RunInstallSkill(opts InstallSkillOptions) (any, int) {
	dest, code, env := resolveInstallDest(opts.Dest, opts.Target)
	if env != nil {
		return env, code
	}

	if opts.Check {
		return runCheckSkill(dest)
	}

	res := InstallSkillResult{
		Dest:    dest,
		Written: []string{},
		Skipped: []string{},
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return map[string]any{
			"error":   "write_failed",
			"message": fmt.Sprintf("act install-skill: mkdir %s: %v", dest, err),
		}, 3
	}

	root := skill.FS()
	walkErr := fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		target := filepath.Join(dest, filepath.FromSlash(p))
		if d.IsDir() {
			if mkErr := os.MkdirAll(target, 0o755); mkErr != nil {
				return fmt.Errorf("mkdir %s: %w", target, mkErr)
			}
			return nil
		}
		want, rerr := fs.ReadFile(root, p)
		if rerr != nil {
			return fmt.Errorf("read embedded %s: %w", p, rerr)
		}
		existing, statErr := os.ReadFile(target)
		switch {
		case statErr == nil:
			if bytes.Equal(existing, want) {
				res.Skipped = append(res.Skipped, target)
				return nil
			}
			if !opts.Force {
				res.Refused = append(res.Refused, target)
				return nil
			}
		case errors.Is(statErr, os.ErrNotExist):
			// fall through and write
		default:
			return fmt.Errorf("stat %s: %w", target, statErr)
		}
		if werr := os.WriteFile(target, want, 0o644); werr != nil {
			return fmt.Errorf("write %s: %w", target, werr)
		}
		res.Written = append(res.Written, target)
		return nil
	})
	if walkErr != nil {
		return map[string]any{
			"error":   "write_failed",
			"message": fmt.Sprintf("act install-skill: %v", walkErr),
		}, 3
	}

	if len(res.Refused) > 0 {
		return res, 1
	}
	return res, 0
}

// resolveInstallDest applies the default destination when the caller passes an
// empty Dest. An explicit Dest always wins (and Target is ignored). When Dest
// is empty, Target picks the host: "" or "claude" → ~/.claude/skills/act,
// "codex" → ~/.codex/skills/act. An unknown Target returns a 2 ("bad input")
// envelope. On home-dir lookup failure it returns a 2 envelope so both the
// write and check paths share identical error shape.
func resolveInstallDest(dest, target string) (string, int, map[string]any) {
	if dest != "" {
		return dest, 0, nil
	}
	var hostDir string
	switch target {
	case "", "claude":
		hostDir = ".claude"
	case "codex":
		hostDir = ".codex"
	default:
		return "", 2, map[string]any{
			"error":   "bad_flag",
			"message": fmt.Sprintf("act install-skill: unknown --target %q; valid: claude, codex", target),
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", 2, map[string]any{
			"error":   "bad_flag",
			"message": fmt.Sprintf("act install-skill: cannot resolve home dir: %v; pass --dest <path>", err),
		}
	}
	return filepath.Join(home, hostDir, "skills", "act"), 0, nil
}

// runCheckSkill is the read-only verification path for
// `act install-skill --check`. It never writes; it classifies every
// embedded file against its on-disk counterpart and returns exit 1 if
// any file is missing or drifted.
func runCheckSkill(dest string) (any, int) {
	res := CheckSkillResult{
		Dest:    dest,
		Version: BinaryVersion,
		Match:   []string{},
		Drift:   []string{},
		Missing: []string{},
	}

	root := skill.FS()
	walkErr := fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." || d.IsDir() {
			return nil
		}
		want, rerr := fs.ReadFile(root, p)
		if rerr != nil {
			return fmt.Errorf("read embedded %s: %w", p, rerr)
		}
		target := filepath.Join(dest, filepath.FromSlash(p))
		existing, statErr := os.ReadFile(target)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			res.Missing = append(res.Missing, target)
			return nil
		case statErr != nil:
			return fmt.Errorf("stat %s: %w", target, statErr)
		}
		if bytes.Equal(existing, want) {
			res.Match = append(res.Match, target)
		} else {
			res.Drift = append(res.Drift, target)
		}
		return nil
	})
	if walkErr != nil {
		return map[string]any{
			"error":   "check_failed",
			"message": fmt.Sprintf("act install-skill --check: %v", walkErr),
		}, 3
	}

	if len(res.Drift) > 0 || len(res.Missing) > 0 {
		return res, 1
	}
	return res, 0
}
