package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aac/act/internal/cli"
)

// runInstallSkill dispatches `act install-skill`. It writes the embedded
// canonical skill tree (SKILL.md + references/) to ~/.claude/skills/act
// (default) or ~/.codex/skills/act (--target codex), making the act binary
// itself the distribution mechanism for the workflow doc.
//
// The default policy is "overwrite if our embedded copy differs, skip
// if it already matches, refuse if a file differs and --force was not
// passed." This lets agents run install-skill safely after every act
// upgrade without clobbering user-authored extensions sitting next to
// the canonical files.
//
// --check switches to read-only mode: compares embedded vs installed bytes,
// never writes, exits 0 if everything matches and 1 if anything drifts or
// is missing.
func runInstallSkill(args []string) int {
	fs := flag.NewFlagSet("install-skill", flag.ContinueOnError)
	dest := fs.String("dest", "", "destination skills directory; overrides --target")
	target := fs.String("target", "claude", "skill host: claude (~/.claude/skills/act) or codex (~/.codex/skills/act)")
	force := fs.Bool("force", false, "overwrite existing files that differ from the embedded copy")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	check := fs.Bool("check", false, "read-only: report whether installed skill matches the embedded copy; never write")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	out, code := cli.RunInstallSkill(cli.InstallSkillOptions{
		Dest:   *dest,
		Target: *target,
		Force:  *force,
		AsJSON: *asJSON,
		Check:  *check,
	})

	if code >= 2 {
		// Hard failure path: error envelope.
		emitEnvelope(*asJSON, out)
		return code
	}

	if *check {
		res, ok := out.(cli.CheckSkillResult)
		if !ok {
			fmt.Fprintf(os.Stderr, "act install-skill --check: unexpected output type %T\n", out)
			return 1
		}
		if *asJSON {
			data, jerr := json.Marshal(res)
			if jerr != nil {
				fmt.Fprintf(os.Stderr, "act install-skill --check: json marshal: %v\n", jerr)
				return 1
			}
			fmt.Println(string(data))
			return code
		}
		renderCheckSkillHuman(os.Stdout, res, code)
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
func renderInstallSkillHuman(w io.Writer, res cli.InstallSkillResult, code int) {
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

// renderCheckSkillHuman prints the human-friendly --check summary. The
// shape mirrors renderInstallSkillHuman so output between the two modes
// reads consistently.
func renderCheckSkillHuman(w io.Writer, res cli.CheckSkillResult, code int) {
	fmt.Fprintf(w, "act install-skill --check %s → %s\n", res.Version, res.Dest)
	if len(res.Match) > 0 {
		fmt.Fprintln(w, "  match:")
		for _, p := range res.Match {
			fmt.Fprintln(w, "    "+p)
		}
	}
	if len(res.Drift) > 0 {
		fmt.Fprintln(w, "  drift (installed differs from embedded copy):")
		for _, p := range res.Drift {
			fmt.Fprintln(w, "    "+p)
		}
	}
	if len(res.Missing) > 0 {
		fmt.Fprintln(w, "  missing (no installed copy at expected path):")
		for _, p := range res.Missing {
			fmt.Fprintln(w, "    "+p)
		}
	}
	if code == 0 && len(res.Match) == 0 && len(res.Drift) == 0 && len(res.Missing) == 0 {
		fmt.Fprintln(w, "  (no embedded files to check)")
	}
	if code != 0 {
		fmt.Fprintln(w, "skill out of date; re-run `act install-skill` (or with --force) to update.")
	}
}
