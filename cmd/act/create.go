package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/aac/act/internal/cli"
)

// stringSlice is a flag.Value that accumulates repeated --flag values into
// a slice (in invocation order). Used by `act create --accept`.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// runCreate dispatches `act create`. Positional argument is the title;
// flags follow the spec §3 form (`-p/--priority`, `--parent`, `--accept`,
// `--type`, `--description`) plus the universal write flags
// (`--no-commit`, `--push`, `--isolated`) and `--json`.
func runCreate(args []string) int {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	priority := fs.Int("priority", 1, "issue priority (0..3, default 1)")
	fs.IntVar(priority, "p", 1, "issue priority (shorthand)")
	parent := fs.String("parent", "", "parent issue id (full or unique prefix)")
	typ := fs.String("type", "task", "issue type (task|bug|epic|chore)")
	description := fs.String("description", "", "issue description")
	var accept stringSlice
	fs.Var(&accept, "accept", "acceptance criterion (repeatable)")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	noCommit := fs.Bool("no-commit", false, "write op file but skip the auto-commit")
	push := fs.Bool("push", false, "push after the commit")
	isolated := fs.Bool("isolated", false, "offline mode: commit but no network ops")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "act create: usage: act create <title> [flags]")
		return 2
	}
	title := fs.Arg(0)

	root, err := findRepoRoot()
	if err != nil {
		emitCreate(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunCreate(root, cli.CreateOptions{
		Title:       title,
		Priority:    *priority,
		Type:        *typ,
		Parent:      *parent,
		Description: *description,
		Accept:      []string(accept),
		AsJSON:      *asJSON,
		NoCommit:    *noCommit,
		Push:        *push,
		Isolated:    *isolated,
	})
	if code != 0 {
		m, _ := toMap(out)
		emitCreate(*asJSON, m)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act create: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	res, ok := out.(cli.CreateResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act create: unexpected output type %T\n", out)
		return 1
	}
	// Per §5.C.4: without --json, the closed-parent warning surfaces on
	// stderr. With --json the warning is embedded in the JSON envelope
	// and stderr is silent.
	for _, w := range res.Warnings {
		if w == "parent_closed" {
			fmt.Fprintln(os.Stderr, "act create: warning: parent issue is closed")
		}
	}
	fmt.Print(cli.FormatCreateHuman(res))
	return 0
}

// emitCreate renders an error envelope for the create subcommand.
func emitCreate(asJSON bool, payload map[string]any) {
	if asJSON {
		data, err := json.Marshal(payload)
		if err != nil {
			fmt.Fprintf(os.Stderr, "act create: json marshal: %v\n", err)
			return
		}
		fmt.Println(string(data))
		return
	}
	if msg, _ := payload["message"].(string); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return
	}
	if e, _ := payload["error"].(string); e != "" {
		fmt.Fprintln(os.Stderr, e)
	}
}
