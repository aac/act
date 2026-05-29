package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/aac/act/internal/index"
)

// ExternalDepGateResult carries the gate check outcome. When Blocked==true
// the caller should return errOut, exitCode immediately (unless --force was
// passed, in which case the caller emits the ForceWarning to stderr and
// proceeds).
type ExternalDepGateResult struct {
	Blocked      bool
	ExternalDeps []string
}

// CheckExternalDepGate inspects the folded state of issue `full` via the
// already-open (and already-rebuilt) index and returns whether the issue
// has open external deps.
//
// The design keeps the gate check in one place so both `act update --claim`
// and `act close` share the same logic. Callers already have an index open
// (close rebuilt it to check current status; claim opens one here). Passing
// the already-open index avoids a redundant rebuild.
func CheckExternalDepGate(idx *index.Index, full string) (ExternalDepGateResult, error) {
	row, err := idx.Get(full)
	if err != nil {
		return ExternalDepGateResult{}, fmt.Errorf("extdepgate: get %s: %w", full, err)
	}
	if len(row.ExternalDeps) == 0 {
		return ExternalDepGateResult{Blocked: false}, nil
	}
	return ExternalDepGateResult{
		Blocked:      true,
		ExternalDeps: row.ExternalDeps,
	}, nil
}

// extDepBlockedMessage returns the human-readable message used in the
// blocked_by_external_dep envelope for both `act update --claim` and
// `act close`. cmd is the full command name for the prefix, e.g.
// "act update --claim" or "act close".
func extDepBlockedMessage(cmd, full string, deps []string) string {
	return fmt.Sprintf(
		"%s: %s has %d open external dep(s); clear with --ext-rm or override with --force",
		cmd, full, len(deps),
	)
}

// BlockedByExtDepErrorOutput builds the CloseErrorOutput envelope returned
// when `act close` is blocked by open external deps.
func BlockedByExtDepErrorOutput(cmd, full string, deps []string) CloseErrorOutput {
	return CloseErrorOutput{
		Error:   ErrBlockedByExternalDep,
		Message: extDepBlockedMessage(cmd, full, deps),
		Details: map[string]any{
			"external_deps": deps,
		},
	}
}

// UpdateBlockedByExtDepErrorOutput builds the UpdateErrorOutput envelope
// returned when `act update --claim` is blocked by open external deps.
func UpdateBlockedByExtDepErrorOutput(cmd, full string, deps []string) UpdateErrorOutput {
	return UpdateErrorOutput{
		Error:   ErrBlockedByExternalDep,
		Message: extDepBlockedMessage(cmd, full, deps),
		Details: map[string]any{
			"external_deps": deps,
		},
	}
}

// EmitExtDepForceWarning writes the --force override warning to stderr (or
// the supplied writer when non-nil). The warning is intentionally verbose
// so it appears in operator logs and makes the override audit-visible.
func EmitExtDepForceWarning(stderr io.Writer, full string, deps []string) {
	dst := stderr
	if dst == nil {
		dst = os.Stderr
	}
	fmt.Fprintf(dst, "WARNING: overriding %d open external dep(s) on %s via --force\n", len(deps), full)
	for _, d := range deps {
		fmt.Fprintf(dst, "  ext-dep: %s\n", d)
	}
}
