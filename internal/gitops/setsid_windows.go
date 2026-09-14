//go:build windows

package gitops

import "syscall"

// setsidAttr is the windows counterpart to the unix build's session-detach
// attr (act-ea6a76). windows.SysProcAttr has no Setsid field, so this is a
// no-op attr: maybeFireOrchestratorSync is a POSIX-specific background-fork
// optimization (fire-and-forget `act remote sync`), and act does not
// currently ship a windows binary (see AGENTS.md — release.yml builds only
// darwin+linux). This file exists so the package still builds on windows
// for local development/testing; it does not attempt to replicate the
// unix detach semantics.
func setsidAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
