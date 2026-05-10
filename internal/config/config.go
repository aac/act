// Package config loads and validates .act/config.json and per-repo settings.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aac/act/internal/canonicaljson"
)

// HLCState mirrors hlc.HLC. Defined locally because the hlc package has not
// yet declared its concrete type (act-9cae); once that lands this can become
// an alias: `type HLCState = hlc.HLC`.
//
// TODO(act-9cae): replace with a type alias to hlc.HLC.
type HLCState struct {
	Wall    int64  `json:"wall"`
	Logical uint32 `json:"logical"`
}

// BundleStrategy controls when act-op files are committed to git.
//
//   - "per_op"      — every op write auto-commits immediately (original behavior;
//                     default for repos initialized before this feature).
//   - "per_session" — claim and close auto-commit/push; all other ops written
//                     during a claim→close window ride the close commit (deferred).
//                     Ops written outside a claim→close window auto-commit as today.
//                     Default for newly-initialized repos.
const (
	BundleStrategyPerOp      = "per_op"
	BundleStrategyPerSession = "per_session"
)

// Config is the on-disk shape of .act/config.json.
//
// Field order in this struct does not matter for serialization: writes use
// canonicaljson which sorts keys lexicographically.
type Config struct {
	NodeID         string   `json:"node_id"`
	BundleStrategy string   `json:"bundle_strategy,omitempty"`
	CreatedAt      string   `json:"created_at"`
	Version        string   `json:"version"`
	LastHLC        HLCState `json:"last_hlc"`
}

// EffectiveBundleStrategy returns the resolved bundle strategy, defaulting to
// BundleStrategyPerOp when the field is empty (pre-feature repos).
func (c Config) EffectiveBundleStrategy() string {
	if c.BundleStrategy == "" {
		return BundleStrategyPerOp
	}
	return c.BundleStrategy
}

// LayoutPaths holds absolute paths for every .act/ artifact a writer touches.
type LayoutPaths struct {
	Root           string // <repo>/.act
	Ops            string // <repo>/.act/ops
	Snapshots      string // <repo>/.act/snapshots
	FoldCheckpoint string // <repo>/.act/fold-checkpoint.json
	IndexDB        string // <repo>/.act/index.db
	Hooks          string // <repo>/.act/hooks
	Imports        string // <repo>/.act/imports
	ConfigJSON     string // <repo>/.act/config.json
	CompactLock    string // <repo>/.act/.lock
}

// Layout derives the conventional .act/ layout paths under repoRoot.
//
// Paths are filepath.Clean'd so they are safe to compare and use as keys.
// Layout does not touch the filesystem; callers wanting directories on disk
// should pass the result to InitDirs.
func Layout(repoRoot string) LayoutPaths {
	root := filepath.Join(repoRoot, ".act")
	return LayoutPaths{
		Root:           filepath.Clean(root),
		Ops:            filepath.Clean(filepath.Join(root, "ops")),
		Snapshots:      filepath.Clean(filepath.Join(root, "snapshots")),
		FoldCheckpoint: filepath.Clean(filepath.Join(root, "fold-checkpoint.json")),
		IndexDB:        filepath.Clean(filepath.Join(root, "index.db")),
		Hooks:          filepath.Clean(filepath.Join(root, "hooks")),
		Imports:        filepath.Clean(filepath.Join(root, "imports")),
		ConfigJSON:     filepath.Clean(filepath.Join(root, "config.json")),
		CompactLock:    filepath.Clean(filepath.Join(root, ".lock")),
	}
}

// ComputeNodeID derives the per-installation node identifier as the first 8
// hex characters of sha256(machineID || gitEmail).
//
// The two inputs are concatenated as raw bytes with no separator; this matches
// the spec's `sha256(machine-id || git user.email)[0:8]` definition. Callers
// are responsible for normalizing inputs (trimming trailing newlines from
// machine-id, lowercasing email if desired) before invocation.
func ComputeNodeID(machineID, gitEmail string) string {
	h := sha256.New()
	h.Write([]byte(machineID))
	h.Write([]byte(gitEmail))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:4]) // 4 bytes -> 8 hex chars
}

// InitDirs creates all directories listed in paths. It is idempotent: if a
// directory already exists no error is returned. Files (config.json, index.db,
// fold-checkpoint.json, .lock) are NOT created here; they are written by the
// component that owns them.
func InitDirs(paths LayoutPaths) error {
	for _, dir := range []string{paths.Root, paths.Ops, paths.Snapshots, paths.Hooks, paths.Imports} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("config: mkdir %s: %w", dir, err)
		}
	}
	return nil
}

// WriteConfig writes c to paths.ConfigJSON via canonicaljson with an atomic
// write-temp + fsync + rename sequence. The temp file lives in the same
// directory as the destination so that os.Rename is guaranteed atomic on
// POSIX. The parent directory is fsynced after rename so the new entry is
// durable across power loss.
func WriteConfig(paths LayoutPaths, c Config) error {
	if err := os.MkdirAll(paths.Root, 0o755); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", paths.Root, err)
	}
	data, err := canonicaljson.Marshal(c)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}

	tmp, err := os.CreateTemp(paths.Root, "config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("config: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// On any failure path before rename, remove the temp file.
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("config: close temp: %w", err)
	}
	if err := os.Rename(tmpName, paths.ConfigJSON); err != nil {
		cleanup()
		return fmt.Errorf("config: rename: %w", err)
	}

	// Best-effort fsync of the parent directory so the rename is durable.
	if dir, err := os.Open(paths.Root); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// ReadConfig reads and decodes paths.ConfigJSON. The file is parsed with the
// standard JSON decoder; callers concerned with byte-identical round-trips
// should re-marshal via canonicaljson.
func ReadConfig(paths LayoutPaths) (Config, error) {
	var c Config
	data, err := os.ReadFile(paths.ConfigJSON)
	if err != nil {
		return c, fmt.Errorf("config: read %s: %w", paths.ConfigJSON, err)
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("config: parse: %w", err)
	}
	return c, nil
}
