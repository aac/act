// Package cli wires the act subcommands into a single binary entry point.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aac/act/internal/config"
)

// rfc3339Millis is the millisecond-precision RFC 3339 layout used throughout
// the on-disk format. It matches the HLC wall format so timestamps written by
// init are comparable with those embedded in op files.
const rfc3339Millis = "2006-01-02T15:04:05.000Z"

// writerVersion is the on-disk writer version stamped into config.json.
const writerVersion = "0.1.0"

// errorOutput is the structured shape returned to the caller when init refuses.
type errorOutput struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// successOutput is the structured shape returned on a successful init.
type successOutput struct {
	OK     bool   `json:"ok"`
	ActDir string `json:"act_dir"`
	NodeID string `json:"node_id"`
}

// RunInit executes the `act init` command logic. It is decoupled from
// stdin/stdout/exec so tests can drive it directly.
//
// Returns a JSON-encodable value (errorOutput on failure, successOutput on
// success) plus a process exit code.
func RunInit(repoRoot string, force bool, machineID, gitEmail string, now func() time.Time) (any, int) {
	if now == nil {
		now = time.Now
	}

	// Refuse if repoRoot is not inside a git working tree. We walk upward
	// looking for a `.git` entry; this matches the resolution helper in
	// main.go but defends in depth in case a caller passes an arbitrary path.
	if !hasGitDir(repoRoot) {
		return errorOutput{
			Error:   "not_in_git",
			Message: fmt.Sprintf("act init: %s is not inside a git working tree", repoRoot),
		}, 3
	}

	paths := config.Layout(repoRoot)

	// Refuse re-init unless --force.
	if _, err := os.Stat(paths.ConfigJSON); err == nil && !force {
		return errorOutput{
			Error:   "act_already_initialized",
			Message: fmt.Sprintf("act init: %s already exists; pass --force to reinitialize", paths.ConfigJSON),
		}, 1
	}

	nodeID := config.ComputeNodeID(machineID, gitEmail)

	if err := config.InitDirs(paths); err != nil {
		return errorOutput{
			Error:   "init_dirs_failed",
			Message: err.Error(),
		}, 1
	}

	cfg := config.Config{
		NodeID:         nodeID,
		BundleStrategy: config.BundleStrategyPerSession,
		CreatedAt:      now().UTC().Format(rfc3339Millis),
		Version:        writerVersion,
		LastHLC:        config.HLCState{},
	}
	if err := config.WriteConfig(paths, cfg); err != nil {
		return errorOutput{
			Error:   "write_config_failed",
			Message: err.Error(),
		}, 1
	}

	if err := ensureGitignoreEntry(repoRoot, ".act/index.db"); err != nil {
		return errorOutput{
			Error:   "gitignore_failed",
			Message: err.Error(),
		}, 1
	}

	return successOutput{
		OK:     true,
		ActDir: paths.Root,
		NodeID: nodeID,
	}, 0
}

// hasGitDir reports whether repoRoot or any of its ancestors contains a
// `.git` entry (file or directory). Walks up to the filesystem root.
func hasGitDir(repoRoot string) bool {
	dir, err := filepath.Abs(repoRoot)
	if err != nil {
		return false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// ensureGitignoreEntry appends `entry` to <repoRoot>/.gitignore if it is not
// already present on its own line. Idempotent.
func ensureGitignoreEntry(repoRoot, entry string) error {
	path := filepath.Join(repoRoot, ".gitignore")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("gitignore: read: %w", err)
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}

	var out strings.Builder
	out.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		out.WriteString("\n")
	}
	out.WriteString(entry)
	out.WriteString("\n")

	if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
		return fmt.Errorf("gitignore: write: %w", err)
	}
	return nil
}
