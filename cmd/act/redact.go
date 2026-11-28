package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aac/act/internal/cli"
	"github.com/aac/act/internal/op"
	"github.com/aac/act/internal/redaction"
)

// redactPositionalMsg is the fixed refusal for a positional argument. The
// argument is never echoed: it may be the secret someone typed by mistake
// (docs/redaction-design.md §5.1).
const redactPositionalMsg = "act redact: takes no positional arguments; strings to remove are read from --patterns-file"

// runRedact dispatches `act redact --patterns-file PATH [--dry-run] [--json]`
// (docs/redaction-design.md §5). It takes none of the universal write flags:
// a redaction committed but not published, or staged but not committed, is a
// half-state with no use, so each of them is an unknown flag (exit 2).
//
// Step 1 of the run happens here, before the tracker is touched: flags are
// parsed and the patterns read and checked, so a bad patterns file changes
// nothing. The strings to remove are never taken from the command line.
func runRedact(args []string) int {
	fs := flag.NewFlagSet("redact", flag.ContinueOnError)
	patternsFile := fs.String("patterns-file", "", "file listing the strings to remove, one per line (UTF-8); use - for stdin. Strings are never taken from the command line")
	dryRun := fs.Bool("dry-run", false, "print the counts a run would produce and change nothing")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := parseFlags(fs, rearranged); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		emitBadFlag(*asJSON, redactPositionalMsg)
		return 2
	}
	if *patternsFile == "" {
		emitBadFlag(*asJSON, "act redact: --patterns-file is required; usage: act redact --patterns-file PATH [--dry-run] [--json]")
		return 2
	}
	text, code, envelope := loadPatternsPayload(*patternsFile)
	if code != 0 {
		emitEnvelope(*asJSON, envelope)
		return code
	}
	patterns, perr := redaction.LoadPatterns(strings.NewReader(text))
	if perr != nil {
		// A PatternError names a line number and a fixed reason, never
		// the line's text.
		emitEnvelope(*asJSON, map[string]any{
			"error":   "bad_input",
			"message": "act redact: " + perr.Error(),
		})
		return 2
	}

	root, err := findRepoRoot()
	if err != nil {
		emitEnvelope(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunRedact(root, cli.RedactOptions{
		Patterns: patterns,
		DryRun:   *dryRun,
	})
	res, isResult := out.(cli.RedactResult)
	if !isResult {
		m, _ := toMap(out)
		emitEnvelope(*asJSON, m)
		return code
	}
	if *asJSON {
		data, jerr := json.Marshal(res)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act redact: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return code
	}
	fmt.Print(cli.FormatRedactHuman(res))
	return code
}

// loadPatternsPayload reads the patterns file (or stdin for "-") for
// `act redact`. Unlike the shared description reader it never puts the path
// or an OS error in a message: the path may be the secret someone pasted by
// mistake, and an OS error quotes it (docs/redaction-design.md §5.1). The
// size limit is the same one the description reader applies. Exit codes
// match the shared reader: 3 for a missing file, 2 otherwise.
func loadPatternsPayload(path string) (string, int, map[string]any) {
	var r io.Reader = os.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				return "", 3, map[string]any{
					"error":   "file_not_found",
					"message": "act redact: the patterns file was not found",
				}
			}
			return "", 2, map[string]any{
				"error":   "bad_flag",
				"message": "act redact: the patterns file could not be read",
			}
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	buf, err := io.ReadAll(io.LimitReader(r, op.MaxDescriptionLen+1))
	if err != nil {
		return "", 2, map[string]any{
			"error":   "bad_flag",
			"message": "act redact: the patterns file could not be read",
		}
	}
	if len(buf) > op.MaxDescriptionLen {
		return "", 2, map[string]any{
			"error":   "bad_flag",
			"message": fmt.Sprintf("act redact: the patterns file is larger than its %d-byte limit", op.MaxDescriptionLen),
		}
	}
	return string(buf), 0, nil
}
