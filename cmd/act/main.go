// Command act is the CLI entry point. It dispatches subcommands to the
// internal/cli package.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	switch sub {
	case "version":
		os.Exit(runVersion(args))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "act: unknown subcommand %q\n", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: act <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "subcommands: version")
}

func runVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	checkRepo := fs.Bool("check-repo", false, "walk .act/ops/ and report max writer_version; exit 4 on skew")
	jsonOut := fs.Bool("json", false, "emit JSON output")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	repoRoot := ""
	if *checkRepo {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "act version: %v\n", err)
			return 1
		}
		repoRoot = cwd
	}

	out, code := cli.RunVersion(*checkRepo, repoRoot)
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return code
	}

	// Human-friendly rendering.
	m, ok := out.(map[string]any)
	if !ok {
		fmt.Fprintf(os.Stderr, "act version: unexpected output\n")
		return 1
	}
	if code == 0 {
		fmt.Printf("act %s (writer %s)\n", m["binary_version"], m["writer_version"])
		if v, ok := m["max_op_version"]; ok {
			fmt.Printf("repo max writer_version: %v\n", v)
		}
		return 0
	}
	// Error path.
	if msg, ok := m["message"].(string); ok {
		fmt.Fprintf(os.Stderr, "act version: %s\n", msg)
	} else if errStr, ok := m["error"].(string); ok {
		fmt.Fprintf(os.Stderr, "act version: %s\n", errStr)
	}
	return code
}
