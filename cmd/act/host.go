package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runHost dispatches `act host`: print (or set) THIS machine's label — the
// value `act ready` and `act next` compare an issue's `--host` pin
// against.
//
// It needs no repo and no .act/ state: the label is a property of the
// machine, not of a tracker, which is the whole reason it lives in one
// per-machine file instead of in each of the 29 stores on this box
// (act-2c7be3).
//
// The output always names the SOURCE as well as the label. That is not
// decoration: the one way host affinity fails badly is a label that
// matches nothing, which makes pinned work vanish from `act ready` on
// every machine at once. Whoever is diagnosing that needs to know
// whether to edit a file, an env var, or the machine's name.
func runHost(args []string) int {
	fs := flag.NewFlagSet("host", flag.ContinueOnError)
	set := fs.String("set", "", "write this label to the per-machine host file and print the result; the empty string removes the file, restoring the hostname fallback")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraPositionals("act host", fs, 0, *asJSON,
		"act host takes no positional arguments; to set the label use `act host --set <label>`") {
		return 2
	}

	// fs.Visit distinguishes `--set ""` (the clearing form) from --set
	// never supplied, exactly as `act update --host ""` does.
	setSupplied := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "set" {
			setSupplied = true
		}
	})

	if setSupplied {
		path, err := cli.SetHostLabel(*set)
		if err != nil {
			emitEnvelope(*asJSON, map[string]any{
				"error":   "bad_flag",
				"message": err.Error(),
			})
			return 2
		}
		info := cli.ResolveHost()
		if *asJSON {
			return emitHostJSON(info, path)
		}
		if *set == "" {
			fmt.Printf("removed %s\nthis host: %s\n", path, info.Describe())
		} else {
			fmt.Printf("wrote %s\nthis host: %s\n", path, info.Describe())
		}
		if info.Source == cli.HostSourceEnv {
			fmt.Fprintf(os.Stderr,
				"act host: WARNING: $%s is set, so it still wins over the file just written.\n",
				cli.HostEnvVar)
		}
		return 0
	}

	info := cli.ResolveHost()
	if *asJSON {
		return emitHostJSON(info, info.Path)
	}
	fmt.Printf("this host: %s\n", info.Describe())
	if info.Source == cli.HostSourceHostname {
		fmt.Printf("set a different label with: act host --set <label>   (writes %s)\n", info.Path)
	}
	return 0
}

// emitHostJSON writes the machine-readable form. `config_path` is where
// --set writes, reported whatever the active source is, so a tool can fix
// a misconfigured machine without guessing the path.
func emitHostJSON(info cli.HostInfo, path string) int {
	data, err := json.Marshal(map[string]any{
		"host":        info.Label,
		"source":      info.Source,
		"config_path": path,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "act host: json marshal: %v\n", err)
		return 1
	}
	fmt.Println(string(data))
	return 0
}
