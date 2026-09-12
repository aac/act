package cli

// Doc-claim tests for machine affinity (act-2c7be3).
//
// Every one of these drives the real `act` subprocess against a real
// store, because the whole class of bug this feature can have is a write
// that goes through and then does nothing. act carries TWO independent
// allowlists for updatable fields — `op.validUpdateFields` at write time
// and `fold.isAllowedUpdateField` at fold time — and the fold one
// SILENTLY returns without mutating for a field it does not know. Add
// `machine` to only one and `act update --machine laptop` writes a valid,
// committed op that folds to nothing, with no error on any command. An
// assertion on the write path alone cannot see that; an assertion on what
// `act ready` subsequently returns can.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// machineSite builds a store with a known machine label, returning the
// site path. The label is supplied through ACT_MACHINE by the callers'
// env, so no real ~/.config/act/machine is read or written.
func machineSite(t *testing.T) string {
	t.Helper()
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")
	return site
}

// readyJSON runs `act ready --json` with the given extra env and decodes
// the envelope.
func readyJSON(t *testing.T, site string, env []string, args ...string) map[string]any {
	t.Helper()
	out, _ := mustRunActEnv(t, site, env, 0, append([]string{"ready", "--json"}, args...)...)
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("ready --json: %v\n%s", err, out)
	}
	return doc
}

func machineObj(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	m, ok := doc["machine"].(map[string]any)
	if !ok {
		t.Fatalf("ready --json carries no `machine` object; keys=%v", machineKeysOf(doc))
	}
	return m
}

func machineKeysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func machineReadyIDs(doc map[string]any) []string {
	rows, _ := doc["ready"].([]any)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if m, ok := r.(map[string]any); ok {
			if id, ok := m["id"].(string); ok {
				out = append(out, id)
			}
		}
	}
	return out
}

func machineContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// TestDocClaim_Machine_ReadyExcludesOtherMachines pins the headline
// claim, made in `act ready --help`, README, docs/spec.md §act ready and
// skills/act/SKILL.md: an issue pinned to another machine is excluded
// from the ready set, and an unpinned one never is.
//
// Asserted through `act ready --json` rather than through `act show`,
// because the failure this feature exists to prevent is a ready set that
// offers work this machine cannot do.
func TestDocClaim_Machine_ReadyExcludesOtherMachines(t *testing.T) {
	site := machineSite(t)
	env := []string{"ACT_MACHINE=mini"}

	pinned, _ := mustRunActEnv(t, site, env, 0, "create", "laptop work", "--machine", "laptop", "--json")
	pinnedID := pickIDFromJSON(t, pinned)
	free, _ := mustRunActEnv(t, site, env, 0, "create", "anywhere work", "--json")
	freeID := pickIDFromJSON(t, free)
	mine, _ := mustRunActEnv(t, site, env, 0, "create", "mini work", "--machine", "mini", "--json")
	mineID := pickIDFromJSON(t, mine)

	doc := readyJSON(t, site, env)
	ids := machineReadyIDs(doc)
	if machineContains(ids, pinnedID) {
		t.Errorf("ready on mini returned the laptop-pinned issue %s; ids=%v", pinnedID, ids)
	}
	if !machineContains(ids, freeID) {
		t.Errorf("ready dropped the UNPINNED issue %s — an unpinned issue runs anywhere; ids=%v", freeID, ids)
	}
	if !machineContains(ids, mineID) {
		t.Errorf("ready dropped the mini-pinned issue %s on the mini; ids=%v", mineID, ids)
	}

	m := machineObj(t, doc)
	if m["filtered"] != true {
		t.Errorf("machine.filtered = %v, want true on an explicitly-labelled machine", m["filtered"])
	}
	if got := m["pinned_elsewhere"]; got != float64(1) {
		t.Errorf("machine.pinned_elsewhere = %v, want 1", got)
	}
	if m["label"] != "mini" {
		t.Errorf("machine.label = %v, want mini", m["label"])
	}

	// The same store answered from the laptop's point of view must
	// return the mirror image. Without this the test passes for a
	// binary that simply drops every pinned row.
	lap := readyJSON(t, site, []string{"ACT_MACHINE=laptop"})
	lapIDs := machineReadyIDs(lap)
	if !machineContains(lapIDs, pinnedID) {
		t.Errorf("ready on laptop dropped the laptop-pinned issue %s; ids=%v", pinnedID, lapIDs)
	}
	if machineContains(lapIDs, mineID) {
		t.Errorf("ready on laptop returned the mini-pinned issue %s; ids=%v", mineID, lapIDs)
	}
}

// TestDocClaim_Machine_UpdatePinsAndUnpins pins the `act update --machine`
// claim in docs/spec.md §act update and the flag help, INCLUDING the
// clearing form. It is the test that catches the fold-layer allowlist
// miss described at the top of this file: a write that commits and folds
// to nothing leaves ready unchanged, and only this assertion sees it.
func TestDocClaim_Machine_UpdatePinsAndUnpins(t *testing.T) {
	site := machineSite(t)
	env := []string{"ACT_MACHINE=mini"}

	out, _ := mustRunActEnv(t, site, env, 0, "create", "movable work", "--json")
	id := pickIDFromJSON(t, out)

	if ids := machineReadyIDs(readyJSON(t, site, env)); !machineContains(ids, id) {
		t.Fatalf("fixture broken: %s not ready before pinning; ids=%v", id, ids)
	}

	mustRunActEnv(t, site, env, 0, "update", id, "--machine", "laptop", "--json")
	if ids := machineReadyIDs(readyJSON(t, site, env)); machineContains(ids, id) {
		t.Errorf("after `act update --machine laptop`, %s is still ready on the mini — "+
			"the update_field op folded to nothing; ids=%v", id, ids)
	}

	mustRunActEnv(t, site, env, 0, "update", id, "--machine", "", "--json")
	if ids := machineReadyIDs(readyJSON(t, site, env)); !machineContains(ids, id) {
		t.Errorf("after `act update --machine \"\"`, %s did not come back to the ready set; ids=%v", id, ids)
	}
}

// TestDocClaim_Machine_FailsOpenWithoutExplicitLabel pins the safety
// claim made in README, docs/spec.md §act ready and `act help machines`:
// a machine with no EXPLICIT label filters nothing.
//
// The fixture has to make the hostname-derived label wrong on purpose,
// which is exactly the real-world shape (an OS rename, a lost config
// file). It does that by pinning to a label no hostname can be, then
// running with neither ACT_MACHINE nor a config file — XDG_CONFIG_HOME
// is pointed at an empty temp dir so no real ~/.config/act/machine is
// read.
func TestDocClaim_Machine_FailsOpenWithoutExplicitLabel(t *testing.T) {
	site := machineSite(t)
	cfg := t.TempDir()
	unlabelled := []string{"ACT_MACHINE=", "XDG_CONFIG_HOME=" + cfg}

	out, _ := mustRunActEnv(t, site, unlabelled, 0, "create", "pinned work",
		"--machine", "some-other-machine", "--json")
	id := pickIDFromJSON(t, out)

	doc := readyJSON(t, site, unlabelled)
	if ids := machineReadyIDs(doc); !machineContains(ids, id) {
		t.Errorf("an unlabelled machine EXCLUDED pinned issue %s — act must fail open "+
			"and never filter on a hostname guess; ids=%v", id, ids)
	}
	m := machineObj(t, doc)
	if m["filtered"] != false {
		t.Errorf("machine.filtered = %v, want false on an unlabelled machine", m["filtered"])
	}
	if m["source"] != "hostname" {
		t.Errorf("machine.source = %v, want hostname", m["source"])
	}
	// pinned_elsewhere stays an honest count even though nothing was
	// dropped — that is what lets a consumer say "1 row here needs
	// another machine" without knowing which mode produced the answer.
	if got := m["pinned_elsewhere"]; got != float64(1) {
		t.Errorf("machine.pinned_elsewhere = %v, want 1 even when nothing was filtered", got)
	}

	// And it must SAY so. Silence here would be the worst outcome: the
	// pins would look honoured while every one was being ignored.
	_, stderr := mustRunActEnv(t, site, unlabelled, 0, "ready")
	if !strings.Contains(stderr, "no label") {
		t.Errorf("an unlabelled machine said nothing about the pins it ignored; stderr=%q", stderr)
	}

	// The control: give the same store an explicit label and the same
	// row IS excluded. Without this the test passes for a binary that
	// never filters at all.
	labelled := readyJSON(t, site, []string{"ACT_MACHINE=mini", "XDG_CONFIG_HOME=" + cfg})
	if ids := machineReadyIDs(labelled); machineContains(ids, id) {
		t.Errorf("with ACT_MACHINE=mini the pinned row was still returned; ids=%v", ids)
	}
}

// TestDocClaim_Machine_ReadyJSONKeySet pins the cross-process contract
// docs/spec.md §act ready states: the `machine` object is ALWAYS emitted
// and carries exactly these four keys. quota-floor keys its behaviour on
// the object's presence — an absent one means "this act predates machine
// affinity" — so the day someone adds `omitempty` for tidiness, the
// fleet silently reverts to an unfiltered count with every other test
// still green. rules/09: every contract needs one test at the real
// boundary.
func TestDocClaim_Machine_ReadyJSONKeySet(t *testing.T) {
	site := machineSite(t)
	env := []string{"ACT_MACHINE=mini"}
	// Deliberately an EMPTY store: the object must be present even when
	// there is nothing to say, which is the case a zero-value elision
	// would break.
	doc := readyJSON(t, site, env)
	m := machineObj(t, doc)

	want := map[string]bool{"label": true, "source": true, "filtered": true, "pinned_elsewhere": true}
	for k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("ready --json machine object is missing key %q; got %v", k, machineKeysOf(m))
		}
	}
	for k := range m {
		if !want[k] {
			t.Errorf("ready --json machine object carries unexpected key %q — "+
				"consumers key on this shape; got %v", k, machineKeysOf(m))
		}
	}
}

// TestDocClaim_Machine_NextAllMachinesRequiresPeek pins the refusal
// documented in `act next --help`, docs/spec.md §act next and `act help
// machines`. Claiming an issue pinned elsewhere makes it in_progress,
// and `ready` admits only open issues, so the claim would hide it from
// BOTH machines.
//
// Asserted as an exit code and a message, plus the mirror: --peek
// --all-machines works and writes nothing.
func TestDocClaim_Machine_NextAllMachinesRequiresPeek(t *testing.T) {
	site := machineSite(t)
	env := []string{"ACT_MACHINE=mini"}
	out, _ := mustRunActEnv(t, site, env, 0, "create", "laptop work", "--machine", "laptop", "--json")
	id := pickIDFromJSON(t, out)

	_, stderr := mustRunActEnv(t, site, env, 2, "next", "--all-machines")
	if !strings.Contains(stderr, "--peek") {
		t.Errorf("the refusal must name --peek so the caller knows the working form; stderr=%q", stderr)
	}

	// The mirror: with --peek it is allowed, and it claims nothing.
	peek, _ := mustRunActEnv(t, site, env, 0, "next", "--peek", "--all-machines", "--json")
	if !strings.Contains(peek, id) {
		t.Errorf("next --peek --all-machines did not surface the pinned issue %s; out=%s", id, peek)
	}
	show, _ := mustRunActEnv(t, site, env, 0, "show", id, "--json")
	if strings.Contains(show, `"status":"in_progress"`) || strings.Contains(show, `"status": "in_progress"`) {
		t.Errorf("next --peek --all-machines CLAIMED %s; show=%s", id, show)
	}
}

// TestDocClaim_Machine_SubcommandReportsLabelAndSource pins `act machine`
// as documented in docs/spec.md §act machine and `act help machines`:
// it reports the label AND where it came from, and --set writes the
// per-machine file. The source is load-bearing — it is the difference
// between "of course, that is laptop work" and "why does this machine
// think it is called andrews-mbp".
func TestDocClaim_Machine_SubcommandReportsLabelAndSource(t *testing.T) {
	site := t.TempDir()
	cfg := t.TempDir()
	base := []string{"ACT_MACHINE=", "XDG_CONFIG_HOME=" + cfg}

	// With nothing set, the source is the hostname.
	out, _ := mustRunActEnv(t, site, base, 0, "machine", "--json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("act machine --json: %v\n%s", err, out)
	}
	if doc["source"] != "hostname" {
		t.Errorf("act machine source = %v, want hostname", doc["source"])
	}

	// --set writes the per-machine file and the label follows it.
	mustRunActEnv(t, site, base, 0, "machine", "--set", "testbox")
	want := filepath.Join(cfg, "act", "machine")
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("act machine --set did not write %s: %v", want, err)
	}
	if strings.TrimSpace(string(body)) != "testbox" {
		t.Errorf("%s = %q, want testbox", want, string(body))
	}
	out, _ = mustRunActEnv(t, site, base, 0, "machine", "--json")
	doc = nil
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("act machine --json after --set: %v\n%s", err, out)
	}
	if doc["machine"] != "testbox" || doc["source"] != "config" {
		t.Errorf("after --set: machine=%v source=%v, want testbox/config", doc["machine"], doc["source"])
	}

	// ACT_MACHINE still wins over the file — the documented order.
	out, _ = mustRunActEnv(t, site, []string{"ACT_MACHINE=envbox", "XDG_CONFIG_HOME=" + cfg}, 0, "machine", "--json")
	doc = nil
	_ = json.Unmarshal([]byte(out), &doc)
	if doc["machine"] != "envbox" || doc["source"] != "ACT_MACHINE" {
		t.Errorf("with ACT_MACHINE set: machine=%v source=%v, want envbox/ACT_MACHINE", doc["machine"], doc["source"])
	}
}

// runActEnv is runAct with extra environment entries appended to the
// inherited environment. It lives here rather than in
// concurrent_helper_test.go because machine affinity is the only surface
// whose behaviour depends on the process environment, and the tests that
// need it must be able to say "this machine is called X" without writing
// anything outside the test's own temp dirs.
func runActEnv(t *testing.T, site string, extraEnv []string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	if actBinaryPath == "" {
		t.Fatalf("runActEnv: act binary not built (TestMain did not run?)")
	}
	cmd := exec.Command(actBinaryPath, args...)
	cmd.Dir = site
	cmd.Env = append(os.Environ(), extraEnv...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("runActEnv: %s %s: %v", site, strings.Join(args, " "), err)
		}
		code = ee.ExitCode()
	}
	return so.String(), se.String(), code
}

func mustRunActEnv(t *testing.T, site string, extraEnv []string, want int, args ...string) (stdout, stderr string) {
	t.Helper()
	so, se, code := runActEnv(t, site, extraEnv, args...)
	if code != want {
		t.Fatalf("act %s in %s (env %v): exit %d (want %d)\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), site, extraEnv, code, want, so, se)
	}
	return so, se
}

// TestDocClaim_Machine_ReadyHelpNamesShippedContract pins `act ready
// --help` to the contract that actually shipped (act-58947a).
//
// The first implementation commit wrote this flag's help against the
// design brief rather than against the code that landed on top of it:
// it named a `--host` flag and a top-level JSON `elsewhere` key, and
// design review had already replaced both — the flag is `--machine`,
// and the count lives at `machine.pinned_elsewhere` inside an object
// whose ABSENCE is the feature-detection signal. A consumer written
// against the old sentence does not get a warning; it gets a KeyError
// on a key act never emits.
//
// So the assertion is two-sided. The forbidden strings matter as much
// as the required ones: a help text that merely gains the right words
// while keeping the wrong ones still tells the reader to look for a
// key that is not there.
func TestDocClaim_Machine_ReadyHelpNamesShippedContract(t *testing.T) {
	site := t.TempDir()
	env := []string{"ACT_MACHINE=", "XDG_CONFIG_HOME=" + t.TempDir()}
	// flag.ContinueOnError renders usage to stderr and exits 2; read
	// both streams so this test does not also pin which one carries it.
	so, se, _ := runActEnv(t, site, env, "ready", "--help")
	help := so + se

	for _, bad := range []string{"--host", "`elsewhere` key"} {
		if strings.Contains(help, bad) {
			t.Errorf("act ready --help still names %q, which never shipped\n%s", bad, help)
		}
	}
	for _, want := range []string{"--machine", "machine.pinned_elsewhere", "act machine"} {
		if !strings.Contains(help, want) {
			t.Errorf("act ready --help does not name %q\n%s", want, help)
		}
	}
}

// TestDocClaim_Machine_UnlabelledMachineSaysItIsNotFiltering pins the
// accepted design-synth nit (act-71708d): `act machine` says when its
// label came from the hostname and is therefore NOT filtering.
//
// Naming the source is not the same as naming the consequence. A reader
// who sees `this machine: "mini" (from hostname)` has been told where
// the label came from and nothing about what it does — and what it does
// is nothing: act fails open on a guessed label, so every issue pinned
// to another machine is still returned by `act ready`. `act machine` is
// the command you run while SETTING A MACHINE UP, before any pin exists
// for `act ready`'s stderr notice to fire on, so it is the only place
// the fact can reach that reader in time.
func TestDocClaim_Machine_UnlabelledMachineSaysItIsNotFiltering(t *testing.T) {
	site := t.TempDir()
	cfg := t.TempDir()

	// No ACT_MACHINE, no config file: the label is a hostname guess.
	out, _ := mustRunActEnv(t, site, []string{"ACT_MACHINE=", "XDG_CONFIG_HOME=" + cfg}, 0, "machine")
	if !strings.Contains(out, "from hostname") {
		t.Fatalf("precondition: expected a hostname-sourced label\n%s", out)
	}
	if !strings.Contains(out, "not filtering") {
		t.Errorf("act machine on an unlabelled machine does not say it is not filtering\n%s", out)
	}
	if !strings.Contains(out, "act machine --set") {
		t.Errorf("act machine does not name the command that fixes it\n%s", out)
	}

	// Once the machine is named, the warning must go away — otherwise it
	// is noise on every correctly configured machine in the fleet.
	mustRunActEnv(t, site, []string{"ACT_MACHINE=", "XDG_CONFIG_HOME=" + cfg}, 0, "machine", "--set", "testbox")
	out, _ = mustRunActEnv(t, site, []string{"ACT_MACHINE=", "XDG_CONFIG_HOME=" + cfg}, 0, "machine")
	if strings.Contains(out, "not filtering") {
		t.Errorf("act machine still warns after --set named the machine\n%s", out)
	}
}
