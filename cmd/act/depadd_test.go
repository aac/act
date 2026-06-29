package main

import (
	"flag"
	"strings"
	"testing"
)

// newDepAddTestFS mirrors the FlagSet built inside runDepAdd. It is
// reproduced here (rather than refactored out) so tests pin the
// exact surface a real invocation sees: flag names, default values,
// and bool/string-ness must all match runDepAdd.
//
// If runDepAdd ever grows or renames a flag, this helper must move
// in lockstep — tests below assert behavior at the rearrange+parse
// boundary, not the runDepAdd entry-point.
func newDepAddTestFS() (fs *flag.FlagSet, typ, blockedBy, blocks *string) {
	fs = flag.NewFlagSet("dep add", flag.ContinueOnError)
	fs.SetOutput(&strings.Builder{})
	typ = fs.String("type", "blocks", "edge type")
	blockedBy = fs.String("blocked-by", "", "")
	blocks = fs.String("blocks", "", "")
	fs.Bool("json", false, "")
	fs.Bool("no-commit", false, "")
	fs.Bool("push", false, "")
	fs.Bool("isolated", false, "")
	return fs, typ, blockedBy, blocks
}

// parseDepAddArgs runs the production rearrange + parse pipeline and
// returns the resolved (child, parent, edgeType) plus the usage exit
// code. This is exactly what runDepAdd does up to the point of
// dispatching cli.RunDepAdd, minus the network/git side effects.
func parseDepAddArgs(t *testing.T, argv []string) (child, parent, edgeType string, code int, msg string) {
	t.Helper()
	fs, typ, blockedBy, blocks := newDepAddTestFS()
	rearranged, err := rearrangeArgs(argv, fs)
	if err != nil {
		t.Fatalf("rearrangeArgs(%v): %v", argv, err)
	}
	if perr := fs.Parse(rearranged); perr != nil {
		t.Fatalf("fs.Parse(%v): %v", rearranged, perr)
	}
	return resolveDepAddArgs(fs, *typ, *blockedBy, *blocks)
}

// TestDepAddPositionalForm: today's `<child> <parent>` form must keep
// working unchanged. This is the load-bearing back-compat assertion
// for act-63a1.
func TestDepAddPositionalForm(t *testing.T) {
	child, parent, edge, code, msg := parseDepAddArgs(t, []string{"act-aaaa", "act-bbbb"})
	if code != 0 {
		t.Fatalf("code = %d (%q), want 0", code, msg)
	}
	if child != "act-aaaa" || parent != "act-bbbb" {
		t.Errorf("child/parent = %q/%q, want act-aaaa/act-bbbb", child, parent)
	}
	if edge != "blocks" {
		t.Errorf("edge = %q, want blocks", edge)
	}
}

// TestDepAddPositionalFormWithType: --type relates on the positional
// form is unchanged. Direction flags only touch the blocks semantic;
// non-blocks edge types still go through the positional path.
func TestDepAddPositionalFormWithType(t *testing.T) {
	child, parent, edge, code, _ := parseDepAddArgs(t, []string{"act-aaaa", "act-bbbb", "--type", "relates"})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if edge != "relates" || child != "act-aaaa" || parent != "act-bbbb" {
		t.Errorf("got (%s,%s,%s), want (act-aaaa,act-bbbb,relates)", child, parent, edge)
	}
}

// TestDepAddBlockedByFlag: `<a> --blocked-by <b>` ≡ positional `<a> <b>`
// with edge=blocks. a is the child (whose deps[] grows), b is the
// parent (referenced from the child's deps[]).
func TestDepAddBlockedByFlag(t *testing.T) {
	child, parent, edge, code, msg := parseDepAddArgs(t, []string{"act-aaaa", "--blocked-by", "act-bbbb"})
	if code != 0 {
		t.Fatalf("code = %d (%q), want 0", code, msg)
	}
	if child != "act-aaaa" || parent != "act-bbbb" {
		t.Errorf("child/parent = %q/%q, want act-aaaa/act-bbbb", child, parent)
	}
	if edge != "blocks" {
		t.Errorf("edge = %q, want blocks", edge)
	}
}

// TestDepAddBlocksFlag: `<a> --blocks <b>` ≡ positional `<b> <a>`
// with edge=blocks. a is the parent (the blocker), b is the child
// (whose deps[] gains the entry pointing at a).
func TestDepAddBlocksFlag(t *testing.T) {
	child, parent, edge, code, msg := parseDepAddArgs(t, []string{"act-aaaa", "--blocks", "act-bbbb"})
	if code != 0 {
		t.Fatalf("code = %d (%q), want 0", code, msg)
	}
	// Subject "a" blocks "b": b is child, a is parent.
	if child != "act-bbbb" || parent != "act-aaaa" {
		t.Errorf("child/parent = %q/%q, want act-bbbb/act-aaaa", child, parent)
	}
	if edge != "blocks" {
		t.Errorf("edge = %q, want blocks", edge)
	}
}

// TestDepAddDirectionFlagsMutuallyExclusive: passing both --blocks and
// --blocked-by would have ambiguous semantics; reject as bad flags.
func TestDepAddDirectionFlagsMutuallyExclusive(t *testing.T) {
	_, _, _, code, msg := parseDepAddArgs(t, []string{"act-aaaa", "--blocks", "act-bbbb", "--blocked-by", "act-cccc"})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(msg, "mutually exclusive") {
		t.Errorf("msg = %q, want mutually-exclusive complaint", msg)
	}
}

// TestDepAddDirectionFlagsRejectSecondPositional: a second positional
// alongside --blocked-by/--blocks is ambiguous (which id wins?). The
// usage error keeps callers honest.
func TestDepAddDirectionFlagsRejectSecondPositional(t *testing.T) {
	_, _, _, code, msg := parseDepAddArgs(t, []string{"act-aaaa", "act-bbbb", "--blocked-by", "act-cccc"})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(msg, "second positional") {
		t.Errorf("msg = %q, want second-positional complaint", msg)
	}
}

// TestDepAddDirectionFlagsRejectNonBlocksType: --blocked-by/--blocks
// are blocks-only directional aliases. Combining them with --type
// relates (or supersedes) is contradictory.
func TestDepAddDirectionFlagsRejectNonBlocksType(t *testing.T) {
	_, _, _, code, msg := parseDepAddArgs(t, []string{"act-aaaa", "--blocked-by", "act-bbbb", "--type", "relates"})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(msg, "imply --type blocks") {
		t.Errorf("msg = %q, want type-conflict complaint", msg)
	}
}

// TestDepAddDirectionFlagsAcceptRedundantBlocksType: --type blocks
// alongside --blocked-by is redundant but consistent; accept it.
func TestDepAddDirectionFlagsAcceptRedundantBlocksType(t *testing.T) {
	child, parent, edge, code, _ := parseDepAddArgs(t, []string{"act-aaaa", "--blocked-by", "act-bbbb", "--type", "blocks"})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if child != "act-aaaa" || parent != "act-bbbb" || edge != "blocks" {
		t.Errorf("got (%s,%s,%s)", child, parent, edge)
	}
}

// TestDepAddNoArgs: zero-positional invocation still produces a usage
// error. Unchanged from pre-aliases behavior; locked in to catch a
// future refactor accidentally accepting `act dep add`.
func TestDepAddNoArgs(t *testing.T) {
	_, _, _, code, msg := parseDepAddArgs(t, []string{})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(msg, "usage:") {
		t.Errorf("msg = %q, want usage line", msg)
	}
}

// TestDepAddOnePositionalNoFlag: one positional with no direction flag
// is still an arity error — callers must supply either a second
// positional or one of the direction flags.
func TestDepAddOnePositionalNoFlag(t *testing.T) {
	_, _, _, code, msg := parseDepAddArgs(t, []string{"act-aaaa"})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(msg, "usage:") {
		t.Errorf("msg = %q, want usage line", msg)
	}
}

// TestDepAddFlagsAfterPositional: the rearrangeArgs helper makes
// `<a> --blocked-by <b>` parse identically to `--blocked-by <b> <a>`.
// Locking this in here so a future regression in argparse can't
// silently break the natural typing order.
func TestDepAddFlagsAfterPositional(t *testing.T) {
	child, parent, _, code, _ := parseDepAddArgs(t, []string{"act-aaaa", "--blocked-by", "act-bbbb"})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	child2, parent2, _, code2, _ := parseDepAddArgs(t, []string{"--blocked-by", "act-bbbb", "act-aaaa"})
	if code2 != 0 {
		t.Fatalf("code2 = %d, want 0", code2)
	}
	if child != child2 || parent != parent2 {
		t.Errorf("post-positional form differs: (%s,%s) vs (%s,%s)", child, parent, child2, parent2)
	}
}

// TestDepAddUsageMentionsBothFlags: the canonical usage line must
// mention both direction flags so an agent who sees the error has
// enough context to file the right edge without consulting docs.
// This satisfies the "no agent needs to consult docs" acceptance
// criterion at the surface where bad flags are reported.
func TestDepAddUsageMentionsBothFlags(t *testing.T) {
	usage := depAddUsage()
	for _, want := range []string{"--blocked-by", "--blocks", "<a> <b>"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage = %q, missing %q", usage, want)
		}
	}
	for _, banned := range []string{"<child>", "<parent>"} {
		if strings.Contains(usage, banned) {
			t.Errorf("usage = %q, contains banned token %q (use --blocked-by/--blocks instead)", usage, banned)
		}
	}
}
