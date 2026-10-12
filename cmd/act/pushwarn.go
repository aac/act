package main

import (
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// warnPushNoRemote prints the human form of the push_no_remote warning for
// the named command ("close", "dep add", "reopen", "delete") when warnings
// carries it: --push was given, the tracker has no origin remote, and the
// write landed without a push (act-7e8600). Worded as `act create` and
// `act update` word theirs. Under --json the warning is in the result
// instead, and this is not called.
func warnPushNoRemote(cmd string, warnings []string) {
	for _, w := range warnings {
		if w == cli.WarnPushNoRemote {
			fmt.Fprintf(os.Stderr, "act %s: warning: --push: no origin remote, nothing was pushed\n", cmd)
		}
	}
}
