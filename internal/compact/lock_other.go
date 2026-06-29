//go:build !unix

package compact

import (
	"errors"
	"io/fs"
	"os"
)

// acquireLock is the non-unix fallback: it relies on O_CREATE|O_EXCL to give
// a single process exclusive ownership of lockPath. Release deletes the file
// so subsequent invocations can re-acquire.
func acquireLock(lockPath string) (release func(), locked bool, err error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	release = func() {
		_ = f.Close()
		_ = os.Remove(lockPath)
	}
	return release, true, nil
}
