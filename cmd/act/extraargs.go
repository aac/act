package main

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
)

// Extra-positional rejection for read-only subcommands (act-a79d66).
//
// The motivating field report: two agents independently ran
//
//	act log <id> "some note I want on the ticket"
//
// intending to append an annotation. `act log` is a read-only op-log
// viewer; it took Arg(0) as the issue scope, dropped the message on the
// floor, printed the op count and exited 0. Four annotations across three
// trackers were lost with no error anywhere.
//
// The interface — not the callers — is what failed here: a write-shaped
// invocation of a read-only verb produced a success-shaped response. The
// fix is to make that class loud, and to say in the same breath which
// command the caller actually wanted, so the loud error is a signpost
// rather than a dead end.
//
// Scope note: this covers the read-only verbs. The write verbs
// (`act create`, `act close`, ...) have the same silent-extra-positional
// shape and are deliberately NOT changed here — rejecting a stray
// positional on a write path can break existing invocations that
// currently succeed, which is a wider blast radius than this bug's
// acceptance criteria call for.

// rejectExtraPositionals reports whether fs carries more positional
// arguments than the subcommand accepts, emitting a bad_flag envelope
// when it does. `cmd` is the user-facing command name ("act log"),
// maxArgs the number of positionals the verb legitimately takes, and
// hint a trailing clause naming the command the caller probably wanted.
//
// Returns true when the caller should stop and exit 2.
func rejectExtraPositionals(cmd string, fs *flag.FlagSet, maxArgs int, asJSON bool, hint string) bool {
	if fs.NArg() <= maxArgs {
		return false
	}
	extras := fs.Args()[maxArgs:]
	quoted := make([]string, 0, len(extras))
	for _, e := range extras {
		quoted = append(quoted, strconv.Quote(e))
	}
	noun := "argument"
	if len(extras) > 1 {
		noun = "arguments"
	}
	msg := fmt.Sprintf("%s: unexpected extra %s: %s", cmd, noun, strings.Join(quoted, ", "))
	if hint != "" {
		msg += "; " + hint
	}
	emitBadFlag(asJSON, msg)
	return true
}

// noteHint is the shared trailing clause for read-only verbs that a
// caller might reach for when they wanted to annotate an issue. It names
// the real append path so the rejection teaches the right command
// instead of just refusing.
const noteHint = `this is a read-only command and takes no message — to append a note to an issue, use: act update <id> --description-append "..."`
