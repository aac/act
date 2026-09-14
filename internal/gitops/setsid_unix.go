//go:build !windows

package gitops

import "syscall"

// setsidAttr returns the SysProcAttr that detaches a spawned child into its
// own session (see maybeFireOrchestratorSync's doc comment for why). Unix
// only: syscall.SysProcAttr has no Setsid field on windows (act-ea6a76).
func setsidAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
