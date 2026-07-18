package gitops

import (
	"errors"
	"fmt"
	"testing"
)

// TestClassifyStaleLock pins the classifier against git's canonical
// lock-collision stderr for both lock files, plus the negative cases that must
// NOT be misread as a stale lock (act-8fe6eb).
func TestClassifyStaleLock(t *testing.T) {
	cases := []struct {
		name     string
		msg      string
		wantLock string
		wantOK   bool
	}{
		{
			name:     "index.lock canonical",
			msg:      "git add -- ops/x.json: exit status 128 (stderr: fatal: Unable to create '/repo/.act/.git/index.lock': File exists.\n\nAnother git process seems to be running in this repository...)",
			wantLock: "index.lock",
			wantOK:   true,
		},
		{
			name:     "HEAD.lock ref update",
			msg:      "git commit -m msg: exit status 128 (stderr: error: cannot lock ref 'HEAD': Unable to create '/repo/.act/.git/HEAD.lock': File exists)",
			wantLock: "HEAD.lock",
			wantOK:   true,
		},
		{
			name:     "unrelated git failure",
			msg:      "git push: exit status 1 (stderr: fatal: could not read from remote repository)",
			wantLock: "",
			wantOK:   false,
		},
		{
			name:     "merge conflict is not a lock",
			msg:      "git rebase: exit status 1 (stderr: CONFLICT (content): Merge conflict in ops/x.json)",
			wantLock: "",
			wantOK:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lock, ok := classifyStaleLock(tc.msg)
			if ok != tc.wantOK || lock != tc.wantLock {
				t.Fatalf("classifyStaleLock(%q) = (%q, %v); want (%q, %v)", tc.msg, lock, ok, tc.wantLock, tc.wantOK)
			}
		})
	}
}

// TestWrapIfStaleLock_ErrorsAs confirms the wrapped error is discoverable via
// errors.As so the CLI write helpers can recognise the failure class through
// their own `fmt.Errorf("cli: commit: %w", err)` wrapping layers.
func TestWrapIfStaleLock_ErrorsAs(t *testing.T) {
	base := fmt.Errorf("git add -- ops/x.json: exit status 128 (stderr: fatal: Unable to create '/repo/.act/.git/index.lock': File exists.)")
	wrapped := wrapIfStaleLock(base)
	// Simulate the CLI's outer wrap.
	outer := fmt.Errorf("cli: stage: %w", wrapped)

	var se *StaleGitLockError
	if !errors.As(outer, &se) {
		t.Fatalf("errors.As failed to find *StaleGitLockError through the wrap chain: %v", outer)
	}
	if se.LockFile != "index.lock" {
		t.Errorf("LockFile = %q; want index.lock", se.LockFile)
	}

	// A non-lock error passes through unchanged.
	plain := fmt.Errorf("git push: exit status 1 (stderr: auth failed)")
	if got := wrapIfStaleLock(plain); got != plain {
		t.Errorf("wrapIfStaleLock wrapped a non-lock error: %v", got)
	}
}
