//go:build windows

package hooks

import (
	"os"
	"os/exec"
)

// sigterm returns the closest analog of SIGTERM on Windows. There is no
// real SIGTERM; os.Kill (== SIGKILL semantics) is used because Windows
// processes cannot install a SIGTERM handler in the POSIX sense. The
// SIGTERM/SIGKILL distinction collapses on Windows and the 1s grace
// window becomes a no-op in practice.
func sigterm() os.Signal { return os.Kill }

// setProcessGroup is a no-op on Windows; the OS' job-object machinery
// is the equivalent and is out of scope for v0.1.
func setProcessGroup(cmd *exec.Cmd) {}

// killGroup falls back to killing the leader on Windows.
func killGroup(cmd *exec.Cmd, sig os.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(sig)
}
