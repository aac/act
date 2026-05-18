// `act migrate-to-nested` — one-shot migration from the legacy single-repo
// `.act/`-in-host layout to the Phase 1 nested-repo layout.
//
// See internal/cli/migrate_to_nested.go for the implementation and
// docs/migration-runbook.md for the operator story.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runMigrateToNested dispatches `act migrate-to-nested`. Flag set mirrors
// the rest of the CLI (single --json toggle). The repo root is resolved
// from cwd.
//
// Exit codes mirror cli.RunMigrateToNested:
//   - 0: success (including the idempotent already-migrated case)
//   - 1: nested-repo bootstrap or host commit failed
//   - 2: bad flag
//   - 3: missing .act/, not in a git tree
func runMigrateToNested(args []string) int {
	fs := flag.NewFlagSet("migrate-to-nested", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	if err := fs.Parse(args); err != nil {
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

	out, code := cli.RunMigrateToNested(root, getMachineID(), getGitEmail(), cli.MigrateToNestedOptions{
		AsJSON: *asJSON,
	})
	if code != 0 {
		m, _ := toMap(out)
		emitEnvelope(*asJSON, m)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act migrate-to-nested: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	// Human-friendly rendering. The output shape mirrors `act init`'s
	// human output so the two commands feel like the same family.
	m, ok := toMap(out)
	if !ok {
		fmt.Fprintf(os.Stderr, "%v\n", out)
		return 1
	}
	already, _ := m["already_migrated"].(bool)
	if already {
		fmt.Printf("act migrate-to-nested: %s already has a nested .git; checking host-side state.\n", m["act_dir"])
	} else {
		fmt.Printf("Migrated .act/ at %s to nested-repo layout.\n", m["act_dir"])
	}
	if nested, _ := m["nested_committed"].(bool); nested {
		fmt.Println(`Bootstrapped nested .act/ git repo; pre-migration op files are the initial commit.`)
	}
	if ut, _ := m["host_untracked"].(bool); ut {
		fmt.Println(`Untracked .act/ from the host repo (git rm --cached).`)
	}
	if hc, _ := m["host_committed"].(bool); hc {
		fmt.Println(`Committed host-side changes (.gitignore + untrack + CONTRIBUTING stanza).`)
	} else if gi, _ := m["gitignore_updated"].(bool); gi {
		fmt.Println(`Added .act/ to host .gitignore (commit pending; run git commit when ready).`)
	}
	if hi, _ := m["hook_installed"].(bool); hi {
		fmt.Println(`Installed host pre-commit hook to reject accidental .act/* stages.`)
	}
	if ce, _ := m["contributing_emitted"].(bool); ce {
		fmt.Println(`Appended Act-Id trailer stanza to CONTRIBUTING.md (public-looking remote detected).`)
	}
	if pf, ok := m["partial_failures"].([]any); ok && len(pf) > 0 {
		fmt.Fprintln(os.Stderr, "warning: some host-side steps did not complete:")
		for _, f := range pf {
			fmt.Fprintf(os.Stderr, "  - %v\n", f)
		}
		fmt.Fprintln(os.Stderr, "  nested .act/ is in place; re-run act migrate-to-nested to retry partial steps.")
	}
	fmt.Println(`Verify with: act doctor --check nested-layout`)
	return 0
}
