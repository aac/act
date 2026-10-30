// Package config — per-checkout claim identity (node_id).
//
// A claimant is one checkout of a tracker: one `.act/` working tree with its
// own op log, index and write lock (docs/claim-identity-design.md §2). Its
// node_id is 8 lowercase hex characters minted at random on first use and
// kept in the nested repo's own git config as `act.nodeId`, with the
// absolute state-root path beside it as `act.nodeIdPath`.
//
// Why `.act/.git/config`: that file is per-clone, so `git clone` (state
// import --from-remote, act-sync) never carries it. The path fingerprint
// covers the other way a checkout is made — `bootstrap-worker --from-cwd`
// copies `.act/.git` wholesale — by re-minting whenever the stored path is
// not this checkout's state root.
//
// node_id is deliberately NOT read from `.act/config.json` any more: that file
// is committed, so it travelled with every clone and made two checkouts of
// one tracker indistinguishable claimants.
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/aac/act/internal/flock"
)

const (
	// NodeIDKey holds this checkout's node_id in `.act/.git/config`.
	NodeIDKey = "act.nodeId"
	// NodeIDPathKey holds the canonical absolute state-root path the
	// node_id was minted for. A mismatch means the nested repo was copied
	// from another checkout, and the id is re-minted.
	NodeIDPathKey = "act.nodeIdPath"
)

// nodeIDRe is the shape op.Envelope.Validate and hlc already enforce. Both
// keep their checks unexported, so the regexp is restated here.
var nodeIDRe = regexp.MustCompile(`^[0-9a-f]{8}$`)

// NodeIDLockFile is the flock file ResolveNodeID takes inside .act/.git
// while minting a node_id. It is checkout-local state: copies of .act/
// (bootstrap-worker) must not carry it.
const NodeIDLockFile = "act-nodeid.lock"

// nodeIDLockWait bounds how long ResolveNodeID waits for a sibling process
// that is minting the id for the same checkout at the same moment.
const nodeIDLockWait = 10 * time.Second

// ResolveNodeID returns this checkout's node_id, minting and storing one on
// first use (or when the stored act.nodeIdPath is not paths.Root).
//
// When paths.Root has no nested `.git`, it returns the first 8 hex chars of
// sha256(canonical state root) without writing anything, so reads keep
// working on a store that cannot hold the key.
func ResolveNodeID(paths LayoutPaths) (string, error) {
	root := canonicalStateRoot(paths.Root)
	gitDir := filepath.Join(paths.Root, ".git")
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		sum := sha256.Sum256([]byte(root))
		return hex.EncodeToString(sum[:4]), nil
	}
	cfgPath := ActGitConfigPath(paths.Root)

	if id, ok, err := readStoredNodeID(cfgPath, root); err != nil {
		return "", err
	} else if ok {
		return id, nil
	}

	// Mint under a lock so two processes resolving a fresh checkout at the
	// same moment agree on one id instead of the later write silently
	// replacing the id the earlier process already used.
	release, err := lockNodeID(filepath.Join(gitDir, NodeIDLockFile))
	if err != nil {
		return "", err
	}
	defer release()

	if id, ok, err := readStoredNodeID(cfgPath, root); err != nil {
		return "", err
	} else if ok {
		return id, nil
	}

	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("config: mint node_id: %w", err)
	}
	id := hex.EncodeToString(b[:])
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		// A nested .git with no config file yet: create it so
		// SetGitConfig (which requires an existing file) can write.
		if werr := os.WriteFile(cfgPath, nil, 0o644); werr != nil {
			return "", fmt.Errorf("config: create %s: %w", cfgPath, werr)
		}
	}
	if err := SetGitConfig(cfgPath, NodeIDKey, id); err != nil {
		return "", err
	}
	if err := SetGitConfig(cfgPath, NodeIDPathKey, root); err != nil {
		return "", err
	}
	return id, nil
}

// readStoredNodeID returns the stored id when it is well-formed and was
// minted for root.
func readStoredNodeID(cfgPath, root string) (string, bool, error) {
	id, err := GetGitConfig(cfgPath, NodeIDKey)
	if err != nil {
		return "", false, err
	}
	if !nodeIDRe.MatchString(id) {
		return "", false, nil
	}
	p, err := GetGitConfig(cfgPath, NodeIDPathKey)
	if err != nil {
		return "", false, err
	}
	if p != root {
		return "", false, nil
	}
	return id, true, nil
}

// canonicalStateRoot resolves symlinks (macOS /tmp vs /private/tmp) and makes
// the path absolute, so the same checkout reached by two spellings keeps one
// identity instead of re-minting on every spelling change.
func canonicalStateRoot(root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	return filepath.Clean(root)
}

func lockNodeID(path string) (func(), error) {
	deadline := time.Now().Add(nodeIDLockWait)
	for {
		release, locked, err := flock.TryLock(path)
		if err != nil {
			return nil, fmt.Errorf("config: lock %s: %w", path, err)
		}
		if locked {
			return release, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("config: lock %s: still held after %s", path, nodeIDLockWait)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
