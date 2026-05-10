package main

import (
	"flag"
	"strings"
)

// rearrangeArgs reorders args so that all flag tokens precede all
// positional tokens, then returns the rearranged slice. Go's stdlib
// `flag` package stops parsing at the first non-flag argument; spec
// docs and the test plan write commands like `act create "title" -p 1
// --json` (flags AFTER the positional). Callers run this helper before
// `fs.Parse(rearranged)` so interleaved orderings parse identically to
// the strict flags-first form.
//
// The helper distinguishes `--flag=value` (single token) from `--flag
// value` (two tokens) by consulting the FlagSet's known flags via
// `fs.Lookup`. Boolean flags do NOT consume the following token. The
// lone `--` token terminates flag parsing: everything that follows is
// treated as a positional verbatim.
//
// Unknown flags fall through to fs.Parse so it can produce its usual
// error envelope; the helper itself never errors but returns an error
// slot for forward compatibility.
func rearrangeArgs(args []string, fs *flag.FlagSet) ([]string, error) {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))

	i := 0
	for i < len(args) {
		a := args[i]

		// Bare `--` ends flag parsing entirely; everything after it is
		// positional verbatim (including tokens that look like flags).
		// Preserve the terminator in the rearranged output so fs.Parse
		// honours it — without this, a title that starts with `--`
		// (e.g. `act create -- "--my-flag-named-issue"`) was passed
		// through as a flag-shaped first positional and fs.Parse
		// rejected it as an unknown flag (act-6218). Convention: all
		// flags MUST appear before the `--` terminator on the command
		// line; flags after `--` become positional, same as every
		// other Unix tool.
		if a == "--" {
			flags = append(flags, "--")
			positionals = append(positionals, args[i+1:]...)
			break
		}

		if !strings.HasPrefix(a, "-") || a == "-" {
			positionals = append(positionals, a)
			i++
			continue
		}

		// Strip the leading dashes and split on '=' to recover the bare
		// flag name. Single-dash and double-dash forms are normalized.
		name := a
		if strings.HasPrefix(name, "--") {
			name = name[2:]
		} else {
			name = name[1:]
		}
		eqIdx := strings.IndexByte(name, '=')
		hasEq := eqIdx >= 0
		if hasEq {
			name = name[:eqIdx]
		}

		flags = append(flags, a)
		i++

		// `--flag=value` carries its value inline; never consume next.
		if hasEq {
			continue
		}

		// Look up the flag definition. If unknown, leave the next token
		// alone — fs.Parse will report the unknown-flag error.
		f := fs.Lookup(name)
		if f == nil {
			continue
		}

		// Boolean flags do not consume the following token. The stdlib
		// recognizes them via the optional boolFlag interface on the
		// flag.Value.
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}

		// Non-boolean flag with no inline value: pull the next token as
		// its argument, even if that token itself looks like a flag —
		// the stdlib parser does the same.
		if i < len(args) {
			flags = append(flags, args[i])
			i++
		}
	}

	out := make([]string, 0, len(flags)+len(positionals))
	out = append(out, flags...)
	out = append(out, positionals...)
	return out, nil
}
