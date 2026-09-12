package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aac/act/internal/op"
)

// This file owns act's answer to "which machine am I on?" — the half of
// host affinity (act-2c7be3) that must NOT come from a caller.
//
// The requirement is Andrew's and it is the whole point: a session learns
// its own host from the machine it is on, never from a flag a caller has
// to remember. A flag would be forgotten by exactly the unattended callers
// that need it — the ones that spent eleven /orchestrate captains on
// 2026-09-11 surveying a queue whose every row needed the other machine.
//
// Resolution order, most explicit first:
//
//  1. $ACT_HOST — the escape hatch for a container, a test, or a session
//     that legitimately stands in for another machine.
//  2. $XDG_CONFIG_HOME/act/host (default ~/.config/act/host), first line.
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

// HostEnvVar is the environment override for this machine's label.
const HostEnvVar = "ACT_HOST"

// Host source strings, as reported by ResolveHost and printed by
// `act host`. They are part of the user-visible contract because the
// exclusion notice names the source: a reader who sees an unexpected
// label needs to know which layer produced it before they can fix it.
const (
	HostSourceEnv      = "ACT_HOST"
	HostSourceConfig   = "config"
	HostSourceHostname = "hostname"
)

// HostInfo is the resolved identity of this machine.
type HostInfo struct {
	// Label is the machine's name, as resolved. Never empty in practice:
	// the hostname fallback always produces something.
	Label string `json:"label"`
	// Source is one of HostSourceEnv, HostSourceConfig, HostSourceHostname.
	Source string `json:"source"`
	// Path is the config file consulted — populated for HostSourceConfig,
	// and also for the other sources so `act host` can tell you where
	// `--set` would write.
	Path string `json:"path,omitempty"`
}

// Describe renders the "mini (from hostname)" form used in the ready
// exclusion notice and in `act host`. Naming the SOURCE and not just the
// label is load-bearing: the one catastrophic failure mode of host
// affinity is a label that matches nothing, which hides real work on
// every machine at once. A reader who sees
// `this host: "andrews-mbp" (from hostname)` beside seven excluded rows
// diagnoses that in a glance; one who sees only `andrews-mbp` does not
// know whether to edit a file, an env var, or the machine's name.
func (h HostInfo) Describe() string {
	switch h.Source {
	case HostSourceEnv:
		return fmt.Sprintf("%q (from $%s)", h.Label, HostEnvVar)
	case HostSourceConfig:
		return fmt.Sprintf("%q (from %s)", h.Label, h.Path)
	default:
		return fmt.Sprintf("%q (from hostname)", h.Label)
	}
}

// HostConfigPath returns the per-machine host-label file, honouring
// XDG_CONFIG_HOME and falling back to ~/.config/act/host.
func HostConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			// No home and no XDG: there is nowhere to read or write, so
			// return a path that will simply fail to open. ResolveHost
			// falls through to the hostname, which is the right answer.
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "act", "host")
}

// ResolveHost answers "which machine is this?" per the order documented at
// the top of this file. It never fails: the hostname fallback is always
// available, and an unreadable config file is skipped rather than fatal —
// a read-only command must not die because a label file has bad
// permissions.
func ResolveHost() HostInfo {
	path := HostConfigPath()
	if v := strings.TrimSpace(os.Getenv(HostEnvVar)); v != "" {
		return HostInfo{Label: v, Source: HostSourceEnv, Path: path}
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
				return HostInfo{Label: v, Source: HostSourceConfig, Path: path}
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
	return HostInfo{Label: strings.ToLower(strings.TrimSpace(name)), Source: HostSourceHostname, Path: path}
}

// SetHostLabel writes label to the per-machine host file and returns the
// path written. It exists so configuring a machine is one command rather
// than a file-editing chore handed to a person.
//
// An empty label DELETES the file, restoring the hostname fallback — the
// clearing form, symmetric with `act update <id> --host ""`.
func SetHostLabel(label string) (string, error) {
	path := HostConfigPath()
	if path == "" {
		return "", fmt.Errorf("act host --set: no XDG_CONFIG_HOME and no home directory to write to")
	}
	label = strings.TrimSpace(label)
	if label == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("act host --set: remove %s: %w", path, err)
		}
		return path, nil
	}
	if err := op.ValidateHostLabel("host", label); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("act host --set: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(label+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("act host --set: write %s: %w", path, err)
	}
	return path, nil
}

// HostMatches reports whether an issue pinned to issueHost can run on the
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
func HostMatches(issueHost, thisHost string) bool {
	if strings.TrimSpace(issueHost) == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(issueHost), strings.TrimSpace(thisHost))
}
