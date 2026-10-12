package main

import (
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// ixwWarnIndexUpdateFailed prints the human form of the index_update_failed
// warning (act-c3aa0b) for the write command named cmd: its op is written
// and committed, and only the index refresh after it failed. Worded as
// `act create` words it (act-e3782c).
func ixwWarnIndexUpdateFailed(cmd string, warnings []string) {
	for _, w := range warnings {
		if w == cli.WarnIndexUpdateFailed {
			fmt.Fprintf(os.Stderr, "act %s: warning: the change is written and committed, but .act/index.db could not be updated; do not retry it\n", cmd)
		}
	}
}
