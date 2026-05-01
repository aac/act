package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/aac/act/internal/cli"
)

// stringSliceFlag is a flag.Value implementation that accumulates
// repeated string flags (used by --accept and --dep-rm).
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return fmt.Sprintf("%v", []string(*s)) }
func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// runUpdate dispatches `act update <id>`. Positional <id> plus the rich
// flag surface defined by spec §3 `act update`. Pointer-typed flags
// distinguish "unset" from "explicitly cleared".
func runUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	statusFlag := fs.String("status", "", "new status (open|blocked); use --claim for in_progress; `act close` for closed")
	priorityFlag := fs.Int("priority", -1, "new priority [0..3]")
	assigneeFlag := fs.String("assignee", "", "new assignee (empty string clears)")
	descriptionFlag := fs.String("description", "", "new description (empty string clears)")
	var acceptFlag stringSliceFlag
	fs.Var(&acceptFlag, "accept", "append an acceptance criterion (repeatable)")
	var depRmFlag stringSliceFlag
	fs.Var(&depRmFlag, "dep-rm", "remove a dependency edge as <id> or <id>:<edge_type> (repeatable)")
	claimFlag := fs.Bool("claim", false, "atomic claim protocol")
	noCommit := fs.Bool("no-commit", false, "write op file but skip the auto-commit")
	push := fs.Bool("push", false, "push after the commit")
	isolated := fs.Bool("isolated", false, "offline mode: commit but no network ops")
	verify := fs.Bool("verify", false, "run host pre-commit hooks")
	wait := fs.Bool("wait", false, "with --claim: poll until claimable")
	waitTimeout := fs.Duration("wait-timeout", 30*time.Second, "with --wait: bound on the polling loop")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")

	// Track which flags were explicitly set; we need this to distinguish
	// "user did not pass --status" from "user passed --status open" etc.
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitBadFlag(*asJSON, "act update: usage: act update <id> [flags]")
		return 2
	}
	idArg := fs.Arg(0)

	// Build presence map by walking the parsed flag set.
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })

	opts := cli.UpdateOptions{
		ID:          idArg,
		Accept:      []string(acceptFlag),
		DepRm:       []string(depRmFlag),
		Claim:       *claimFlag,
		Wait:        *wait,
		WaitTimeout: *waitTimeout,
		Push:        *push,
		NoCommit:    *noCommit,
		Isolated:    *isolated,
		AsJSON:      *asJSON,
		Verify:      *verify,
	}
	if visited["status"] {
		s := *statusFlag
		opts.Status = &s
	}
	if visited["priority"] {
		p := *priorityFlag
		opts.Priority = &p
	}
	if visited["assignee"] {
		a := *assigneeFlag
		opts.Assignee = &a
	}
	if visited["description"] {
		d := *descriptionFlag
		opts.Description = &d
	}

	root, err := findRepoRoot()
	if err != nil {
		emitUpdate(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunUpdate(root, opts)

	if code != 0 {
		// Claim-loss carries structured fields; pass through verbatim.
		if cl, ok := out.(cli.UpdateClaimResult); ok {
			if *asJSON {
				data, jerr := json.Marshal(cl)
				if jerr != nil {
					fmt.Fprintf(os.Stderr, "act update: json marshal: %v\n", jerr)
					return 1
				}
				fmt.Println(string(data))
				return code
			}
			fmt.Print(cli.FormatUpdateClaimHuman(cl))
			return code
		}
		m, _ := toMap(out)
		emitUpdate(*asJSON, m)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act update: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	switch v := out.(type) {
	case cli.UpdateResult:
		fmt.Print(cli.FormatUpdateHuman(v))
	case cli.UpdateClaimResult:
		fmt.Print(cli.FormatUpdateClaimHuman(v))
	default:
		fmt.Fprintf(os.Stderr, "act update: unexpected output type %T\n", out)
		return 1
	}
	return 0
}

// emitUpdate renders an error envelope for the update subcommand. Delegates
// to the shared emitEnvelope helper so the JSON shape matches the rest of
// the CLI surface.
func emitUpdate(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}
