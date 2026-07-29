package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
	"github.com/aac/act/internal/config"
)

// runMine dispatches `act mine`. It is sugar over `act list --assignee
// <self> --status in_progress,blocked`, with identity resolved from
// .act/config.json's node_id (or an explicit --as override).
//
// Output schema mirrors `act list` exactly per spec — agents already
// parse list output, so mine reuses that shape.
func runMine(args []string) int {
	fs := flag.NewFlagSet("mine", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	as := fs.String("as", "", "override identity; defaults to .act/config.json node_id")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	// `act mine` is a read-only query taking no positionals; a stray one
	// is most often an identity the caller meant to pass as --as
	// (act-a79d66).
	if rejectExtraPositionals("act mine", fs, 0, *asJSON, "act mine takes no positional arguments; to query another identity use `act mine --as <node-id>`") {
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

	identity := *as
	if identity == "" {
		paths := config.Layout(root)
		cfg, cerr := config.ReadConfig(paths)
		if cerr != nil {
			emitEnvelope(*asJSON, map[string]any{
				"error":   "no_repo",
				"message": fmt.Sprintf("act mine: cannot read .act/config.json: %v; run 'act init' first or pass --as <id>", cerr),
			})
			return 3
		}
		identity = cfg.NodeID
	}

	// Delegate to RunList with the canonical mine filters.
	return runListWithOptions(root, *asJSON, cli.ListOptions{
		Status:   "in_progress,blocked",
		Assignee: identity,
	})
}

// runListWithOptions is a tiny adapter so runMine can drive RunList with
// pre-baked options without re-implementing the rendering path. The list
// command's own runList parses flags from argv; here we pass the options
// directly, then mirror its emit pattern.
func runListWithOptions(root string, asJSON bool, opts cli.ListOptions) int {
	out, code := cli.RunList(root, opts)
	if code != 0 {
		emitEnvelope(asJSON, out)
		return code
	}
	res, ok := out.(cli.ListResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act mine: unexpected output type %T\n", out)
		return 1
	}
	if asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act mine: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
	} else {
		fmt.Print(cli.FormatListHuman(res))
	}
	// runMine passes no Limit, so this never fires today; it is wired so
	// that adding a cap here later cannot silently reintroduce the
	// act-b50d81 under-count.
	if notice := cli.FormatListTruncationNotice(res); notice != "" {
		fmt.Fprint(os.Stderr, notice)
	}
	return 0
}
