package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/importer"
)

// runImport dispatches `act import`. Positional argument is the JSONL path;
// flags follow the universal write flags (`--no-commit`, `--push`) plus
// `--json` for envelope output.
func runImport(args []string) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	noCommit := fs.Bool("no-commit", false, "write op files but skip the auto-commit")
	push := fs.Bool("push", false, "push after the commit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitBadFlag(*asJSON, "act import: usage: act import <path-to-jsonl> [--json] [--no-commit] [--push]")
		return 2
	}
	jsonl := fs.Arg(0)

	root, err := findRepoRoot()
	if err != nil {
		emitImportError(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	var g *gitops.ActGitOps
	if !*noCommit {
		// Phase 1: writes target the nested .act/ git repo.
		g = gitops.NewActGitOps(filepath.Join(root, ".act"))
	}

	res, runErr := importer.Run(root, importer.Options{
		JSONLPath: jsonl,
		AsJSON:    *asJSON,
		NoCommit:  *noCommit,
		Push:      *push,
	}, g)
	if runErr != nil {
		// Translate the structured error tag (import_invalid_jsonl) into the
		// envelope shape spec §7.7 expects.
		errTag := "import_failed"
		if msg := runErr.Error(); len(msg) >= len("import_invalid_jsonl") && msg[:len("import_invalid_jsonl")] == "import_invalid_jsonl" {
			errTag = "import_invalid_jsonl"
		}
		emitImportError(*asJSON, map[string]any{
			"error":   errTag,
			"message": runErr.Error(),
		})
		return 1
	}

	if *asJSON {
		data, jerr := json.Marshal(res)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act import: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	if res.Idempotent {
		fmt.Printf("Already imported (sha=%s); no changes.\n", res.SourceSHA)
		return 0
	}
	fmt.Printf("Imported %d ops (%d issues created); mapping at %s\n",
		res.OpsImported, res.IssuesCreated, res.MappingFile)
	return 0
}

// emitImportError mirrors the other emit*Error helpers.
func emitImportError(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}
