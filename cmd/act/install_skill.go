package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/aac/act/internal/cli"
)

// runInstallSkill dispatches `act install-skill`. It writes the embedded
// canonical skill tree (SKILL.md + references/) to ~/.claude/skills/act
// (or --dest), making the act binary itself the distribution mechanism
// for the workflow doc.
//
// The default policy is "overwrite if our embedded copy differs, skip
// if it already matches, refuse if a file differs and --force was not
// passed." This lets agents run install-skill safely after every act
// upgrade without clobbering user-authored extensions sitting next to
// the canonical files.
func runInstallSkill(args []string) int {
	fs := flag.NewFlagSet("install-skill", flag.ContinueOnError)
	dest := fs.String("dest", "", "destination skills directory; defaults to ~/.claude/skills/act")
	force := fs.Bool("force", false, "overwrite existing files that differ from the embedded copy")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	out, code := cli.RunInstallSkill(cli.InstallSkillOptions{
		Dest:   *dest,
		Force:  *force,
		AsJSON: *asJSON,
	})

	if code >= 2 {
		// Hard failure path: error envelope.
		emitEnvelope(*asJSON, out)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act install-skill: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return code
	}

	res, ok := out.(cli.InstallSkillResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act install-skill: unexpected output type %T\n", out)
		return 1
	}
	renderInstallSkillHuman(os.Stdout, res, code)
	return code
}

// renderInstallSkillHuman prints the human-friendly install summary.
// Lines mirror the JSON shape — one section per outcome class — so
// agents reading stdout get the same information as JSON consumers.
func renderInstallSkillHuman(w *os.File, res cli.InstallSkillResult, code int) {
	fmt.Fprintf(w, "act install-skill → %s\n", res.Dest)
	if len(res.Written) > 0 {
		fmt.Fprintln(w, "  written:")
		for _, p := range res.Written {
			fmt.Fprintln(w, "    "+p)
		}
	}
	if len(res.Skipped) > 0 {
		fmt.Fprintln(w, "  unchanged (already matches embedded copy):")
		for _, p := range res.Skipped {
			fmt.Fprintln(w, "    "+p)
		}
	}
	if len(res.Refused) > 0 {
		fmt.Fprintln(w, "  refused (differs from embedded copy; pass --force to overwrite):")
		for _, p := range res.Refused {
			fmt.Fprintln(w, "    "+p)
		}
	}
	if code == 0 && len(res.Written) == 0 && len(res.Skipped) == 0 && len(res.Refused) == 0 {
		// Defensive — should not happen with at least SKILL.md embedded.
		fmt.Fprintln(w, "  (no files to install)")
	}
	if code != 0 {
		fmt.Fprintln(w, strings.TrimSpace("install incomplete; see refused list above"))
	}
}
