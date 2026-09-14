package gitops

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aac/act/internal/flock"
)

// wlockHoldExternally takes the write lock through a separate open file
// description, which flock treats exactly like another process holding it.
func wlockHoldExternally(t *testing.T, stateRoot string) func() {
	t.Helper()
	rel, locked, err := flock.TryLock(filepath.Join(stateRoot, WriteLockFile))
	if err != nil || !locked {
		t.Fatalf("hold write lock: locked=%v err=%v", locked, err)
	}
	return rel
}

func TestWriteLock_TimesOutWhileHeldElsewhere(t *testing.T) {
	root := t.TempDir()
	release := wlockHoldExternally(t, root)
	defer release()

	start := time.Now()
	_, err := acquireWriteLockTimeout(root, 150*time.Millisecond)
	var te *WriteLockTimeoutError
	if !errors.As(err, &te) || !errors.Is(err, ErrWriteLockTimeout) {
		t.Fatalf("err = %v, want *WriteLockTimeoutError", err)
	}
	if waited := time.Since(start); waited < 150*time.Millisecond || waited > 5*time.Second {
		t.Fatalf("waited %s, want ~150ms bounded wait", waited)
	}
}

func TestWriteLock_AcquiredOnceHolderReleases(t *testing.T) {
	root := t.TempDir()
	release := wlockHoldExternally(t, root)
	go func() {
		time.Sleep(100 * time.Millisecond)
		release()
	}()
	rel, err := acquireWriteLockTimeout(root, 5*time.Second)
	if err != nil {
		t.Fatalf("acquire after holder released: %v", err)
	}
	rel()
}

func TestWriteLock_ReentrantInProcessAndReleasedAtZero(t *testing.T) {
	root := t.TempDir()
	outer, err := acquireWriteLockTimeout(root, time.Second)
	if err != nil {
		t.Fatalf("outer: %v", err)
	}
	inner, err := acquireWriteLockTimeout(root, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("nested acquire in the holding process must not wait: %v", err)
	}
	inner()
	inner() // double release is a no-op
	if _, locked, _ := flock.TryLock(filepath.Join(root, WriteLockFile)); locked {
		t.Fatalf("lock released while the outer acquisition still holds it")
	}
	outer()
	rel, locked, err := flock.TryLock(filepath.Join(root, WriteLockFile))
	if err != nil || !locked {
		t.Fatalf("lock still held after final release: locked=%v err=%v", locked, err)
	}
	rel()
}
