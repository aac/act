package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runRedact dispatches `act redact <id>`. Positional argument is the id;
// flags follow spec §3 (`--field PATH` required, `--value TEXT`,
// `--json`) plus the universal write flags (`--no-commit`, `--push`,
// `--isolated`, `--verify`).
func runRedact(args []string) int {
	fs := flag.NewFlagSet("redact", flag.ContinueOnError)
	field := fs.String("field", "", "field path to redact (e.g. description, acceptance_criteria[2].text)")
	value := fs.String("value", "<redacted>", "rendered replacement value (default \"<redacted>\")")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	noCommit := fs.Bool("no-commit", false, "write op file but skip the auto-commit")
	push := fs.Bool("push", false, "push after the commit")
	isolated := fs.Bool("isolated", false, "offline mode: commit but no network ops")
	verify := fs.Bool("verify", false, "run host pre-commit hooks")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitBadFlag(*asJSON, "act redact: usage: act redact <id> --field <path> [--value TEXT] [--json]")
		return 2
	}
	idArg := fs.Arg(0)

	root, err := findRepoRoot()
	if err != nil {
		emitRedact(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunRedact(root, cli.RedactOptions{
		ID:          idArg,
		FieldPath:   *field,
		Replacement: *value,
		AsJSON:      *asJSON,
		NoCommit:    *noCommit,
		Push:        *push,
		Isolated:    *isolated,
		Verify:      *verify,
	})
	if code != 0 {
		m, _ := toMap(out)
		emitRedact(*asJSON, m)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act redact: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	switch v := out.(type) {
	case cli.RedactResult:
		fmt.Print(cli.FormatRedactHuman(v))
	case cli.RedactNoChange:
		fmt.Print(cli.FormatRedactNoChangeHuman(v))
	default:
		fmt.Fprintf(os.Stderr, "act redact: unexpected output type %T\n", out)
		return 1
	}
	return 0
}

// emitRedact renders an error envelope for the redact subcommand.
func emitRedact(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}
