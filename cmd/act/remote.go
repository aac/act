package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/aac/act/internal/cli"
)

// runRemote dispatches `act remote <enable|disable|sync> [--json]`.
// Wraps cli.RunRemote / cli.RunRemoteSync and renders either JSON or a
// small human-friendly summary.
//
// Phase 2 tickets 1a (enable/disable) + 6a (sync). The verbs toggle the
// `act.*` config keys in `.act/.git/config` plus the post-receive hook
// (1a installs the skeleton; 6a fills the body), and `sync` pushes the
// orchestrator's `.act/.git` to `origin-upstream` (fail-soft on push
// failure). See the respective Run* functions for schema details and
// the rationale for why these knobs live in the nested `.git/config`
// rather than `.act/config.json`.
func runRemote(args []string) int {
	// Parse the verb positional before constructing a FlagSet so
	// that `act remote --help` and `act remote --json enable` both
	// work. Walk args; the first non-flag token is the verb.
	verb := ""
	rest := args
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		verb = a
		// Splice the verb out of the remainder; FlagSet only sees flags.
		rest = append(append([]string{}, args[:i]...), args[i+1:]...)
		break
	}

	fs := flag.NewFlagSet("remote", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	if verb == "" {
		emitBadFlag(*asJSON, "act remote: usage: act remote <enable|disable|sync> [--json]")
		return 2
	}

	// Dispatch on verb. `sync` has its own Run* function and result
	// type; `enable`/`disable` continue to flow through cli.RunRemote.
	if verb == "sync" {
		return runRemoteSync(*asJSON)
	}

	out, code := cli.RunRemote(cli.RemoteOptions{
		Verb:   verb,
		AsJSON: *asJSON,
	})

	if code != 0 {
		emitEnvelope(*asJSON, out)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act remote: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	res, ok := out.(cli.RemoteResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act remote: unexpected output type %T\n", out)
		return 1
	}
	switch res.Verb {
	case "enable":
		fmt.Printf("Enabled act remote at %s\n", res.ActStateRoot)
		fmt.Printf("  config:  %s\n", res.ConfigPath)
		fmt.Printf("  hook:    %s\n", res.HookPath)
		fmt.Printf("  doctor:  %d finding(s)\n", res.DoctorFindings)
	case "disable":
		if res.Changed {
			fmt.Printf("Disabled act remote at %s\n", res.ActStateRoot)
		} else {
			fmt.Printf("act remote already disabled at %s (no-op)\n", res.ActStateRoot)
		}
		fmt.Printf("  config:  %s\n", res.ConfigPath)
		fmt.Printf("  hook:    %s (removed)\n", res.HookPath)
	}
	return 0
}

// runRemoteSync is the `act remote sync` subcommand path. Split from
// runRemote so the result-type rendering doesn't have to juggle two
// disjoint Result structs.
func runRemoteSync(asJSON bool) int {
	out, code := cli.RunRemoteSync(cli.RemoteSyncOptions{
		AsJSON: asJSON,
	})

	if code != 0 {
		emitEnvelope(asJSON, out)
		return code
	}

	if asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act remote sync: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	res, ok := out.(cli.RemoteSyncResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act remote sync: unexpected output type %T\n", out)
		return 1
	}
	switch {
	case res.Pushed:
		fmt.Printf("act remote sync: pushed to origin-upstream\n")
	case res.Logged:
		fmt.Printf("act remote sync: upstream unreachable; logged to %s (exit 0)\n", res.SyncLogPath)
	default:
		fmt.Printf("act remote sync: upstream already up-to-date (no-op)\n")
	}
	return 0
}
