package main

import (
	"flag"
	"reflect"
	"strings"
	"testing"
)

// newTestFS builds a FlagSet that mimics the surface of `act create`:
// a string-valued -p / --priority pair, a string --reason, a bool
// --json, and a bool --no-commit. It exercises every dimension the
// rearrangeArgs helper cares about (string flag, bool flag, both short
// and long names).
func newTestFS() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	// Suppress default error output to keep test logs clean when
	// fs.Parse is invoked with a deliberately bad arg in one case.
	fs.SetOutput(&strings.Builder{})
	var p int
	fs.IntVar(&p, "priority", 1, "priority")
	fs.IntVar(&p, "p", 1, "priority shorthand")
	var r string
	fs.StringVar(&r, "reason", "", "reason")
	var j bool
	fs.BoolVar(&j, "json", false, "json")
	var nc bool
	fs.BoolVar(&nc, "no-commit", false, "no-commit")
	return fs
}

func TestRearrangeArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "flags-before-positional",
			in:   []string{"-p", "1", "--json", "title"},
			want: []string{"-p", "1", "--json", "title"},
		},
		{
			name: "flags-after-positional",
			in:   []string{"title", "-p", "1", "--json"},
			want: []string{"-p", "1", "--json", "title"},
		},
		{
			name: "interleaved",
			in:   []string{"-p", "1", "title", "--json"},
			want: []string{"-p", "1", "--json", "title"},
		},
		{
			name: "equals-form",
			in:   []string{"title", "--priority=2", "--json"},
			want: []string{"--priority=2", "--json", "title"},
		},
		{
			name: "boolean-flag-then-positional",
			in:   []string{"--json", "title"},
			want: []string{"--json", "title"},
		},
		{
			name: "boolean-flag-then-positional-trailing-flag",
			in:   []string{"--json", "title", "--no-commit"},
			want: []string{"--json", "--no-commit", "title"},
		},
		{
			// Per act-6218: rearrangeArgs preserves the `--` terminator so
			// fs.Parse honours it and treats flag-shaped tokens after it
			// as positional verbatim. Previously the `--` was dropped,
			// causing flag-shaped titles to misparse.
			name: "double-dash-terminator",
			in:   []string{"--json", "--", "--reason", "literal"},
			want: []string{"--json", "--", "--reason", "literal"},
		},
		{
			name: "string-flag-with-flag-looking-value",
			in:   []string{"title", "--reason", "smoke complete", "--json"},
			want: []string{"--reason", "smoke complete", "--json", "title"},
		},
		{
			name: "two-positionals-flags-after",
			in:   []string{"child", "parent", "--json"},
			want: []string{"--json", "child", "parent"},
		},
		{
			name: "single-dash-treated-as-positional",
			in:   []string{"-", "--json"},
			want: []string{"--json", "-"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rearrangeArgs(tc.in, newTestFS())
			if err != nil {
				t.Fatalf("rearrangeArgs: unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("rearrangeArgs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestRearrangeArgsParseRoundtrip drives the rearranged slice through
// fs.Parse and checks both the bound flag values and the residual
// positional list. This is the actual contract callers rely on.
func TestRearrangeArgsParseRoundtrip(t *testing.T) {
	fs := newTestFS()
	rearranged, err := rearrangeArgs([]string{"my title", "-p", "2", "--json"}, fs)
	if err != nil {
		t.Fatalf("rearrange: %v", err)
	}
	if err := fs.Parse(rearranged); err != nil {
		t.Fatalf("fs.Parse: %v", err)
	}
	if got := fs.Lookup("p").Value.String(); got != "2" {
		t.Fatalf("p = %s, want 2", got)
	}
	if got := fs.Lookup("json").Value.String(); got != "true" {
		t.Fatalf("json = %s, want true", got)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "my title" {
		t.Fatalf("positional = %v, want [\"my title\"]", fs.Args())
	}
}

// TestRearrangeArgsUnknownFlagBubblesUp confirms the helper passes
// unknown flag tokens through to fs.Parse, which then produces its
// usual error envelope. The helper itself must not error on unknowns.
func TestRearrangeArgsUnknownFlagBubblesUp(t *testing.T) {
	fs := newTestFS()
	rearranged, err := rearrangeArgs([]string{"title", "--bogus", "value"}, fs)
	if err != nil {
		t.Fatalf("rearrange: unexpected error %v", err)
	}
	if perr := fs.Parse(rearranged); perr == nil {
		t.Fatalf("fs.Parse: expected unknown-flag error, got nil")
	}
}
