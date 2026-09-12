package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aac/act/internal/op"
)

// This file owns act's answer to "which machine am I on?" — the half of
// machine affinity (act-2c7be3) that must NOT come from a caller.
//
// The requirement is Andrew's and it is the whole point: a session learns
// its own identity from the machine it is on, never from a flag a caller has
// to remember. A flag would be forgotten by exactly the unattended callers
// that need it — the ones that spent eleven /orchestrate captains on
// 2026-09-11 surveying a queue whose every row needed the other machine.
//
// Resolution order, most explicit first:
//
//  1. $ACT_MACHINE — the escape hatch for a container, a test, or a session
//     that legitimately stands in for another machine.
//  2. $XDG_CONFIG_HOME/act/machine (default ~/.config/act/machine), first line.
//     PER MACHINE, not per store: there are 29 .act/ stores on one of
//     these machines, and a label in each store's config.json would be
//     58 places to keep in sync across two machines, which is 58 places
//     to drift. One file per machine cannot drift from itself. XDG
//     because a widespread external convention beats a bespoke path.
//  3. The short hostname, lower-cased — os.Hostname() up to the first dot.
//
// act deliberately ships NO mapping from hostname to label. quota-floor
// carries one ("mini" in h, "macbook" in h) because it serves one fleet;
// a published CLI must not. act ships the mechanism; the operator names
// the machines.

// MachineEnvVar is the environment override for this machine's label.
const MachineEnvVar = "ACT_MACHINE"

// Host source strings, as reported by ResolveMachine and printed by
// `act machine`. They are part of the user-visible contract because the
// exclusion notice names the source: a reader who sees an unexpected
// label needs to know which layer produced it before they can fix it.
const (
	MachineSourceEnv      = "ACT_MACHINE"
	MachineSourceConfig   = "config"
	MachineSourceHostname = "hostname"
)

// MachineInfo is the resolved identity of this machine.
type MachineInfo struct {
	// Label is the machine's name, as resolved. Never empty in practice:
	// the hostname fallback always produces something.
	Label string `json:"label"`
	// Source is one of MachineSourceEnv, MachineSourceConfig, MachineSourceHostname.
	Source string `json:"source"`
	// Path is the config file consulted — populated for MachineSourceConfig,
	// and also for the other sources so `act machine` can tell you where
	// `--set` would write.
	Path string `json:"path,omitempty"`
}

// Explicit reports whether this machine was NAMED — by $ACT_MACHINE or by
// the config file — as opposed to guessed from its hostname.
//
// This is the fail-open switch, and it is the most important line in the
// file. Only an explicit label may drive exclusion. A hostname-derived
// label is fine to print but must never filter, because it is the layer
// that can change underneath the fleet without anybody touching act: an OS
// update renames the machine, a reinstall loses the config file, and
// suddenly the label matches no pin at all.
//
// What that failure looks like if inference were allowed to filter is the
// reason for the rule. Every pinned row is excluded on the affected
// machine; its runnable count drops; quota-floor ranks the store lower or
// skips it (`if n <= 0: return None`); so no drain is ever launched there;
// so nothing ever runs `act ready` in it; so nobody ever sees the stderr
// notice that was supposed to be the guard. The exclusion suppresses its
// own alarm, the ledger line is indistinguishable from "that repo is
// finished", and the realistic detection latency is weeks — someone
// noticing a repo went quiet.
//
// Failing open inverts that into "an unconfigured machine behaves exactly
// as it did before this feature existed", which is a failure the system
// already survives.
func (m MachineInfo) Explicit() bool {
	return m.Source == MachineSourceEnv || m.Source == MachineSourceConfig
}

// Describe renders the "mini (from hostname)" form used in the ready
// exclusion notice and in `act machine`. Naming the SOURCE and not just the
// label is load-bearing: the one catastrophic failure mode of machine
// affinity is a label that matches nothing, which hides real work on
// every machine at once. A reader who sees
// `this machine: "andrews-mbp" (from hostname)` beside seven excluded rows
// diagnoses that in a glance; one who sees only `andrews-mbp` does not
// know whether to edit a file, an env var, or the machine's name.
func (h MachineInfo) Describe() string {
	switch h.Source {
	case MachineSourceEnv:
		return fmt.Sprintf("%q (from $%s)", h.Label, MachineEnvVar)
	case MachineSourceConfig:
		return fmt.Sprintf("%q (from %s)", h.Label, h.Path)
	default:
		return fmt.Sprintf("%q (from hostname)", h.Label)
	}
}

// MachineConfigPath returns the per-machine machine-label file, honouring
// XDG_CONFIG_HOME and falling back to ~/.config/act/machine.
func MachineConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			// No home and no XDG: there is nowhere to read or write, so
			// return a path that will simply fail to open. ResolveMachine
			// falls through to the hostname, which is the right answer.
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "act", "machine")
}

// ResolveMachine answers "which machine is this?" per the order documented at
// the top of this file. It never fails: the hostname fallback is always
// available, and an unreadable config file is skipped rather than fatal —
// a read-only command must not die because a label file has bad
// permissions.
func ResolveMachine() MachineInfo {
	path := MachineConfigPath()
	if v := strings.TrimSpace(os.Getenv(MachineEnvVar)); v != "" {
		return MachineInfo{Label: v, Source: MachineSourceEnv, Path: path}
	}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			// First line only: a label is one token, and tolerating a
			// trailing comment line costs nothing.
			line := b
			if i := strings.IndexByte(string(b), '\n'); i >= 0 {
				line = b[:i]
			}
			if v := strings.TrimSpace(string(line)); v != "" {
				return MachineInfo{Label: v, Source: MachineSourceConfig, Path: path}
			}
		}
	}
	name, err := os.Hostname()
	if err != nil {
		name = ""
	}
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	return MachineInfo{Label: strings.ToLower(strings.TrimSpace(name)), Source: MachineSourceHostname, Path: path}
}

// SetMachineLabel writes label to the per-machine label file and returns the
// path written. It exists so configuring a machine is one command rather
// than a file-editing chore handed to a person.
//
// An empty label DELETES the file, restoring the hostname fallback — the
// clearing form, symmetric with `act update <id> --machine ""`.
func SetMachineLabel(label string) (string, error) {
	path := MachineConfigPath()
	if path == "" {
		return "", fmt.Errorf("act machine --set: no XDG_CONFIG_HOME and no home directory to write to")
	}
	label = strings.TrimSpace(label)
	if label == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("act machine --set: remove %s: %w", path, err)
		}
		return path, nil
	}
	if err := op.ValidateMachineLabel("machine", label); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("act machine --set: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(label+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("act machine --set: write %s: %w", path, err)
	}
	return path, nil
}

// MachineMatches reports whether an issue pinned to issueHost can run on the
// machine labelled thisHost.
//
// An empty issueHost is "runs anywhere" and matches every machine. That is
// the default for every issue act has ever created, and it is the
// direction Andrew set: anything that CAN run on the mini MUST run on the
// mini, so an unpinned ticket stays available everywhere and the field
// only ever SUBTRACTS from where work can run.
//
// Comparison is case-insensitive so a label typed `LAPTOP` in one place
// and `laptop` in another is one machine, not two.
func MachineMatches(issueHost, thisHost string) bool {
	if strings.TrimSpace(issueHost) == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(issueHost), strings.TrimSpace(thisHost))
}
