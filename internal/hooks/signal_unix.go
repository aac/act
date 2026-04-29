//go:build !windows

package hooks

import (
	"os"
	"os/exec"
	"syscall"
)

// sigterm returns the signal used to politely ask a hook to exit.
func sigterm() os.Signal { return syscall.SIGTERM }

// setProcessGroup configures cmd so its child runs in a fresh process
// group. killGroup can then signal the whole group, ensuring shell
// children (e.g. `sleep` invoked from a `#!/bin/sh` hook) are reaped on
// timeout.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killGroup sends sig to the entire process group spawned by cmd.
// Falling back to the leader if the group lookup fails.
func killGroup(cmd *exec.Cmd, sig os.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	ssig, ok := sig.(syscall.Signal)
	if !ok {
		_ = cmd.Process.Signal(sig)
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Signal(sig)
		return
	}
	// Negative pid signals the whole group.
	_ = syscall.Kill(-pgid, ssig)
}
