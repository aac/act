package gitops

// act-38330b: cross-process write lock on the nested .act/ repo.
//
// Why a lock exists. Several `act` processes routinely share one checkout
// (the fleet shape: many sessions in one repo). Every write runs a
// multi-step git pipeline against the SAME .act/.git — write op file → stage
// → commit → push, with fetch+rebase on rejection — and git's own
// index.lock/HEAD.lock only make each single git command atomic, never the
// pipeline. A stress run (TestWriteLockStress in internal/integration:
// 8 concurrent `act create`+`act update` writers in one checkout plus 2
// writers in a second clone of the same bare remote) showed, WITHOUT this
// lock, over 20 iterations (221 runs):
//
//   - 159 non-zero exits: 147 `stale_git_lock` envelopes whose remedy tells
//     the agent to `rm -f .act/.git/index.lock` — a LIVE sibling's lock —
//     and 12 `write_failed` at commit (`exit status 1` because a sibling's
//     `git commit` had already swept this process's staged op into its own
//     commit, or `cannot lock ref 'HEAD': is at X but expected Y`).
//   - 15 creates acknowledged with exit 0 whose op never reached the remote.
//   - torn state: after that sweep the failing writer withdrew "its" op to
//     .failed-ops/ and reported "nothing was recorded", yet the op was in
//     HEAD — `git status` showed a committed op file deleted from the tree
//     (a later retry would duplicate the op).
//   - rebases refused ("cannot rebase: You have unstaged changes") because a
//     sibling's half-finished write sat in the tree, so pushes exhausted
//     and ops were acked (exit 0) but never reached the remote.
//
// With the lock the same run is clean (360 runs, 0 failures, 0 lost); see
// TestWriteLockStress for the recorded numbers and how to reproduce them.
//
// Design:
//
//   - flock(2) on <state root>/.write.lock via internal/flock (the helper
//     compaction already used). The kernel drops the lock when the holding
//     process dies, so there is no stale-lock state to clean up on unix.
//   - Bounded wait: the lock is polled until DefaultWriteLockTimeout, then the
//     caller gets *WriteLockTimeoutError (envelope `write_lock_timeout`).
//     A hung holder therefore degrades into a clear, retryable error
//     instead of a fleet-wide hang.
//   - Re-entrant within one process: the pipeline nests (a write's publish
//     flushes deferred pushes, which pushes again), so a second acquisition
//     by the process already holding the lock just bumps a count. act's MCP
//     server serves tool calls sequentially, so in-process re-entrancy never
//     lets two concurrent pipelines through.
//   - Scope: op write through publish, and the read path's fetch+rebase.
//     Hooks and folding run outside it.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/aac/act/internal/flock"
)

// WriteLockFile is the lock file's name under the nested .act/ state root.
const WriteLockFile = ".write.lock"

// DefaultWriteLockTimeout bounds how long a writer waits for a sibling
// process to finish its pipeline. A single pipeline includes a push with up
// to five retry rounds (each possibly a bounded fetch), so the bound is
// generous; it exists to turn a wedged holder into an error, not to pace
// normal contention.
const DefaultWriteLockTimeout = 120 * time.Second

// envWriteLockTimeoutMs overrides DefaultWriteLockTimeout (milliseconds).
// Test hook; production leaves it unset.
const envWriteLockTimeoutMs = "ACT_WRITE_LOCK_TIMEOUT_MS"

// envDisableWriteLock=1 makes AcquireWriteLock a no-op. Test hook only: it
// exists so TestWriteLockStress can reproduce the unlocked baseline.
const envDisableWriteLock = "ACT_TEST_DISABLE_WRITE_LOCK"

// ErrWriteLockTimeout is the sentinel wrapped by *WriteLockTimeoutError.
var ErrWriteLockTimeout = errors.New("gitops: timed out waiting for the act write lock")

// WriteLockTimeoutError reports that another process held the write lock
// for longer than the bounded wait. Nothing was written.
type WriteLockTimeoutError struct {
	LockPath string
	Waited   time.Duration
}

func (e *WriteLockTimeoutError) Error() string {
	return fmt.Sprintf("another act process has held %s for over %s; nothing was written — retry once it finishes",
		e.LockPath, e.Waited.Round(time.Millisecond))
}

func (e *WriteLockTimeoutError) Unwrap() error { return ErrWriteLockTimeout }

type heldWriteLock struct {
	count   int
	release func()
}

var (
	writeLocksMu sync.Mutex
	writeLocks   = map[string]*heldWriteLock{}
)

// writeLockPoll is the interval between non-blocking lock attempts.
const writeLockPoll = 10 * time.Millisecond

// AcquireWriteLock takes the cross-process write lock for the nested .act/
// state root at stateRoot, waiting up to the configured bound. The returned
// release must be called exactly once.
func AcquireWriteLock(stateRoot string) (release func(), err error) {
	if os.Getenv(envDisableWriteLock) == "1" {
		return func() {}, nil
	}
	return acquireWriteLockTimeout(stateRoot, resolveWriteLockTimeout())
}

func resolveWriteLockTimeout() time.Duration {
	if v := os.Getenv(envWriteLockTimeoutMs); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return DefaultWriteLockTimeout
}

func acquireWriteLockTimeout(stateRoot string, timeout time.Duration) (func(), error) {
	path := filepath.Join(stateRoot, WriteLockFile)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}

	writeLocksMu.Lock()
	if h, ok := writeLocks[path]; ok {
		h.count++
		writeLocksMu.Unlock()
		return writeLockReleaser(path), nil
	}
	writeLocksMu.Unlock()

	start := time.Now()
	for {
		rel, locked, err := flock.TryLock(path)
		if err != nil {
			return nil, fmt.Errorf("gitops: acquire write lock %s: %w", path, err)
		}
		if locked {
			writeLocksMu.Lock()
			writeLocks[path] = &heldWriteLock{count: 1, release: rel}
			writeLocksMu.Unlock()
			return writeLockReleaser(path), nil
		}
		waited := time.Since(start)
		if waited >= timeout {
			return nil, &WriteLockTimeoutError{LockPath: path, Waited: waited}
		}
		time.Sleep(writeLockPoll)
	}
}

func writeLockReleaser(path string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			writeLocksMu.Lock()
			defer writeLocksMu.Unlock()
			h, ok := writeLocks[path]
			if !ok {
				return
			}
			h.count--
			if h.count == 0 {
				delete(writeLocks, path)
				h.release()
			}
		})
	}
}
