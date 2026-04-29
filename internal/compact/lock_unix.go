//go:build unix

package compact

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// acquireLock takes a non-blocking exclusive flock on lockPath. The return
// values are: a release callback that must be invoked once the caller is done
// (always non-nil unless err != nil), a "locked" boolean (false means the
// lock was contended — not an error), and an error for genuine IO failures.
func acquireLock(lockPath string) (release func(), locked bool, err error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	release = func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}
	return release, true, nil
}
