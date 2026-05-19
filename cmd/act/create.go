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
	// Priority defaults are applied in cli.RunCreate. We use a sentinel
	// default of 1 so `--help` advertises the right value, then detect
	// "user actually set the flag" via fs.Visit so that `-p 0` is not
	// silently coerced to the default (dogfood-report.md finding #1).
	priority := fs.Int("priority", 2, "issue priority (0..3, default 2; lower = more urgent)")
	fs.IntVar(priority, "p", 2, "issue priority (shorthand)")
	parent := fs.String("parent", "", "parent issue id (full or unique prefix)")
	typ := fs.String("type", "task", "issue type (task|bug|epic|chore)")
	description := fs.String("description", "", "issue description")
	descriptionFile := fs.String("description-file", "", "read description from file (UTF-8); use - for stdin")
	var accept stringSlice
	fs.Var(&accept, "accept", "acceptance criterion (repeatable)")
	var blockedBy stringSlice
	fs.Var(&blockedBy, "blocked-by", "id (full or prefix) the new issue is blocked by; writes a blocks-edge alongside the create op in a single atomic commit (repeatable)")
	var blocks stringSlice
	fs.Var(&blocks, "blocks", "id (full or prefix) the new issue blocks; writes a blocks-edge in the inverse direction alongside the create op in a single atomic commit (repeatable). Symmetric to --blocked-by; use this when filing a follow-up that must gate an existing issue (e.g. a critic review that should land before a fanout meta-ticket runs).")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	noCommit := fs.Bool("no-commit", false, "write op file but skip the auto-commit")
	push := fs.Bool("push", false, "push after the commit")
	isolated := fs.Bool("isolated", false, "offline mode: commit but no network ops")
	offline := fs.Bool("offline", false, "commit locally, skip push; record in .act/.pending-pushes for retry on next non-offline write")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitBadFlag(*asJSON, "act create: usage: act create <title> [flags]\n  if your title starts with '-' or '--', put it after a '--' terminator: act create [flags] -- '--my-title'")
		return 2
	}
	title := fs.Arg(0)

	// --description-file is mutually exclusive with --description (per
	// act-6bbd acceptance criterion). Both set ⇒ exit 2 before any I/O.
	// Treat an empty --description as "user passed an empty string"
	// only when fs.Visit reports it as set; otherwise the zero-value
	// default does not collide with --description-file.
	descSet := false
	descFileSet := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "description":
			descSet = true
		case "description-file":
			descFileSet = true
		}
	})
	if descSet && descFileSet {
		emitBadFlag(*asJSON, "act create: --description and --description-file are mutually exclusive")
		return 2
	}
	if descFileSet {
		body, code, errEnv := loadDescriptionFile(*descriptionFile)
		if code != 0 {
			errEnv["message"] = "act create: " + errEnv["message"].(string)
			emitCreate(*asJSON, errEnv)
			return code
		}
		*description = body
	}

	// Detect whether the user supplied -p/--priority so that an explicit
	// -p 0 is propagated to the payload instead of being treated as unset.
	var prioritySet bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "priority" || f.Name == "p" {
			prioritySet = true
		}
	})
	var priorityOpt *int
	if prioritySet {
		v := *priority
		priorityOpt = &v
	}

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
		Priority:    priorityOpt,
		Type:        *typ,
		Parent:      *parent,
		Description: *description,
		Accept:      []string(accept),
		AsJSON:      *asJSON,
		NoCommit:    *noCommit,
		Push:        *push,
		Isolated:    *isolated,
		Offline:     *offline,
		BlockedBy:   []string(blockedBy),
		Blocks:      []string(blocks),
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

// emitCreate renders an error envelope for the create subcommand. Delegates
// to the shared emitEnvelope helper so the JSON shape is uniform.
func emitCreate(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}
