package main

import (
	"fmt"
	"os"
)

// bootstrapWorkerDeprecationNotice is the stderr line printed when the
// deprecated `act bootstrap-worker` alias is invoked. It points at the
// directory-scoped replacement (`act state import`). Declared as a const
// so the TestDocClaim_DeprecatedAliasesDelegate test can reference the
// exact string without drift.
const bootstrapWorkerDeprecationNotice = "act: 'bootstrap-worker' is deprecated; use 'act state import' (same behavior, worktree-blind name)"

// runBootstrapWorker is a thin deprecation alias for `act state import`.
// act is pre-v1 with a single operator, so a clean rename is acceptable;
// the alias is cheap insurance for in-flight orchestrate.md prose during
// the transition (T2 of the worktree-tool arc, MF-D). It prints a notice
// to stderr and delegates to runStateImport with the identical argument
// vector — same flags, same behavior.
func runBootstrapWorker(args []string) int {
	fmt.Fprintln(os.Stderr, bootstrapWorkerDeprecationNotice)
	return runStateImport(args)
}
