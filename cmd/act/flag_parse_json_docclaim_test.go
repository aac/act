package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Doc-claim cover for act-6a7b5e: a flag the flag parser rejects under --json
// used to print nothing on stdout (Go's flag package wrote its message and the
// usage text to stderr), so a caller reading stdout as JSON got an empty
// string instead of an error envelope. Every check here runs the real binary.

// uflJSONFlagRe matches the usage line the flag package prints for -json.
var uflJSONFlagRe = regexp.MustCompile(`(?m)^\s+-json\b`)

// uflJSONCommands returns every command (as its words, without "act") whose
// -h lists -json. The candidates are read from the dispatcher's own source —
// each flag.NewFlagSet("<name>", …) literal and each `case "<name>":` of
// main's switch that calls a run function — so a command added later is
// covered without editing this file. A candidate whose -h does not list -json
// is dropped.
func uflJSONCommands(t *testing.T) [][]string {
	t.Helper()
	dir := filepath.Join(sp6RepoRoot(t), "cmd", "act")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	flagSetRe := regexp.MustCompile(`flag\.NewFlagSet\("([a-z][a-z -]*)"`)
	caseRe := regexp.MustCompile(`case "([a-z][a-z-]*)":\n\t\tos\.Exit\(run`)
	names := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatalf("read %s: %v", n, err)
		}
		for _, m := range flagSetRe.FindAllStringSubmatch(string(body), -1) {
			names[m[1]] = true
		}
		if n == "main.go" {
			for _, m := range caseRe.FindAllStringSubmatch(string(body), -1) {
				names[m[1]] = true
			}
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	outside := t.TempDir()
	var out [][]string
	for _, n := range sorted {
		sub := strings.Fields(n)
		stdout, stderr, _ := runActIn(t, outside, append(append([]string{}, sub...), "-h")...)
		if uflJSONFlagRe.MatchString(stdout + stderr) {
			out = append(out, sub)
		}
	}
	return out
}

// uflEnvelope decodes stdout as exactly one JSON object and returns it.
func uflEnvelope(t *testing.T, what, stdout string) map[string]json.RawMessage {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var env map[string]json.RawMessage
	if err := dec.Decode(&env); err != nil {
		t.Errorf("%s: stdout is not a JSON object: %v\n  stdout: %q", what, err, stdout)
		return nil
	}
	if dec.More() {
		t.Errorf("%s: stdout carries more than one JSON value: %q", what, stdout)
	}
	return env
}

// uflWantBadFlag asserts the bad_flag envelope contract for one invocation:
// exit 2, one JSON object on stdout with error=bad_flag, a message containing
// wantMsg, a details object, and nothing from the flag package on stderr.
func uflWantBadFlag(t *testing.T, wantMsg string, args ...string) {
	t.Helper()
	what := "act " + strings.Join(args, " ")
	stdout, stderr, code := runActIn(t, t.TempDir(), args...)
	if code != 2 {
		t.Errorf("%s: exit %d, want 2", what, code)
	}
	env := uflEnvelope(t, what, stdout)
	if env == nil {
		return
	}
	var slug, msg string
	_ = json.Unmarshal(env["error"], &slug)
	_ = json.Unmarshal(env["message"], &msg)
	if slug != "bad_flag" {
		t.Errorf("%s: error=%q, want bad_flag\n  stdout: %s", what, slug, stdout)
	}
	if !strings.Contains(msg, wantMsg) {
		t.Errorf("%s: message %q does not contain %q", what, msg, wantMsg)
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("%s: message is not one line: %q", what, msg)
	}
	if d := strings.TrimSpace(string(env["details"])); !strings.HasPrefix(d, "{") {
		t.Errorf("%s: details=%s, want an object", what, d)
	}
	// stderr stays empty under --json. The two deprecated aliases print
	// their one-line deprecation notice before any flag is parsed; that
	// line is the only thing allowed through.
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if line == "" || strings.Contains(line, "is deprecated; use") {
			continue
		}
		t.Errorf("%s: stderr is not empty under --json: %q", what, stderr)
		break
	}
}

// TestDocClaim_FlagParseError_JSONEnvelope asserts, for every command whose
// -h lists -json, that an unknown flag under --json answers with the bad_flag
// envelope on stdout and exit 2 — whichever side of the unknown flag --json
// sits on, and in the single-dash spelling — and that without --json the
// same mistake still prints the flag package's message and usage on stderr
// with nothing on stdout.
func TestDocClaim_FlagParseError_JSONEnvelope(t *testing.T) {
	cmds := uflJSONCommands(t)
	have := map[string]bool{}
	for _, sub := range cmds {
		have[strings.Join(sub, " ")] = true
	}
	// The enumeration must not have quietly shrunk: every command the spec
	// gives a synopsis naming --json must be in it.
	for _, s := range sp7Synopses(t) {
		for _, f := range s.flags {
			if f == "json" && !have[strings.Join(s.sub, " ")] {
				t.Errorf("act %s has a spec synopsis naming --json but was not enumerated (%v)", strings.Join(s.sub, " "), cmds)
			}
		}
	}
	if len(cmds) < 20 {
		t.Fatalf("enumerated %d commands accepting --json, want at least 20: %v", len(cmds), cmds)
	}
	const want = "flag provided but not defined: -no-such-flag"
	for _, sub := range cmds {
		sub := sub
		t.Run(strings.Join(sub, "_"), func(t *testing.T) {
			with := func(extra ...string) []string { return append(append([]string{}, sub...), extra...) }
			uflWantBadFlag(t, want, with("--json", "--no-such-flag")...)
			uflWantBadFlag(t, want, with("--no-such-flag", "--json")...)
			uflWantBadFlag(t, want, with("-json", "--no-such-flag")...)

			// Without --json nothing changes: message and usage on stderr,
			// stdout empty, exit 2.
			stdout, stderr, code := runActIn(t, t.TempDir(), with("--no-such-flag")...)
			if code != 2 || stdout != "" || !strings.Contains(stderr, want) || !uflJSONFlagRe.MatchString(stderr) {
				t.Errorf("act %s --no-such-flag (no --json): exit %d, stdout %q, stderr %q; want exit 2, empty stdout, the flag message and the usage text on stderr",
					strings.Join(sub, " "), code, stdout, stderr)
			}
		})
	}
}

// TestDocClaim_FlagParseError_OtherParseFailures covers the other two errors
// the flag parser raises: a flag missing its value and a non-integer for an
// integer flag. Both get the same envelope under --json.
func TestDocClaim_FlagParseError_OtherParseFailures(t *testing.T) {
	uflWantBadFlag(t, "flag needs an argument: -limit", "list", "--json", "--limit")
	uflWantBadFlag(t, `invalid value "abc" for flag -limit`, "list", "--json", "--limit", "abc")
	uflWantBadFlag(t, `invalid value "abc" for flag -limit`, "list", "--limit", "abc", "--json")
	uflWantBadFlag(t, "flag needs an argument: -reason", "close", "act-0000", "--json", "--reason")
	uflWantBadFlag(t, `invalid value "x" for flag -priority`, "create", "t", "-p", "x", "--json")
}

// TestDocClaim_FlagParseError_HelpIsNotAnError asserts -h and --help are
// untouched by --json: the usage text on stderr, no envelope on stdout, and
// the same exit code as without --json.
func TestDocClaim_FlagParseError_HelpIsNotAnError(t *testing.T) {
	for _, sub := range uflJSONCommands(t) {
		cmd := strings.Join(sub, " ")
		_, plainErr, plainCode := runActIn(t, t.TempDir(), append(append([]string{}, sub...), "-h")...)
		for _, h := range []string{"-h", "--help"} {
			stdout, stderr, code := runActIn(t, t.TempDir(), append(append([]string{}, sub...), "--json", h)...)
			if stdout != "" {
				t.Errorf("act %s --json %s: stdout %q, want empty (help is not an error)", cmd, h, stdout)
			}
			if stderr != plainErr || code != plainCode {
				t.Errorf("act %s --json %s: exit %d and stderr differ from plain -h (exit %d)\n  got:  %q\n  want: %q", cmd, h, code, plainCode, stderr, plainErr)
			}
		}
	}
}
