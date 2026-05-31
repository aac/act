package main

import (
	"fmt"
	"os"
)

// harvestDeprecationNotice is the stderr line printed when the deprecated
// `act harvest` alias is invoked. It points at the directory-scoped
// replacement (`act state export`). Declared as a const so the
// TestDocClaim_DeprecatedAliasesDelegate test can reference the exact
// string without drift.
const harvestDeprecationNotice = "act: 'harvest' is deprecated; use 'act state export' (same behavior, worktree-blind name)"

// runHarvest is a thin deprecation alias for `act state export`. act is
// pre-v1 with a single operator, so a clean rename is acceptable; the
// alias is cheap insurance for in-flight orchestrate.md prose during the
// transition (T2 of the worktree-tool arc, MF-D). It prints a notice to
// stderr and delegates to runStateExport with the identical argument
// vector — same flags, same behavior.
func runHarvest(args []string) int {
	fmt.Fprintln(os.Stderr, harvestDeprecationNotice)
	return runStateExport(args)
}
