package cli

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Tests for ask-freed external deps (act-3731ab). The fake stands in for
// `ask show <id> --json`; its item shape is the real one, captured from
// ask's own --json output (every field present, null for unset optionals —
// `ask help schema`), so the parser is tested against the contract rather
// than a paraphrase of it. One end-to-end run against the real ask binary
// is in the ticket's verification, not here: unit tests must not depend on
// a sibling tool being installed.

// askFreedFakeStore is a fake ask store keyed by (dir, id).
type askFreedFakeStore struct {
	mu    sync.Mutex
	items map[string]string // dir + "\x00" + id → stdout ("" = exit non-zero)
	calls []string          // dir + "\x00" + id for a show, "list\x00" + dir for a list, in call order
	// listErr, when set, makes every `ask list` fail (act-a1a83a).
	listErr bool
}

// askFreedInstallFake swaps askFreedShow and askFreedList for fakes for
// the duration of the test. The fake list answers a store that has any
// item registered with every registered item whose stdout is a JSON
// object, under the id inside it — so a registered stdout that does not
// parse, or no registration at all, is an id the list did not return,
// and the resolver falls back to the fake show for it. A dir with
// nothing registered fails like ask with no store. Tests using it must
// not run in parallel.
func askFreedInstallFake(t *testing.T) *askFreedFakeStore {
	t.Helper()
	f := &askFreedFakeStore{items: map[string]string{}}
	prev, prevList := askFreedShow, askFreedList
	askFreedList = func(dir string) ([]byte, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, "list\x00"+dir)
		if f.listErr {
			return nil, errors.New("ask list: signal: killed")
		}
		list := []json.RawMessage{}
		known := false
		for key, out := range f.items {
			d, _, _ := strings.Cut(key, "\x00")
			if d != dir {
				continue
			}
			known = true
			var probe map[string]any
			if json.Unmarshal([]byte(out), &probe) == nil {
				list = append(list, json.RawMessage(out))
			}
		}
		if !known {
			return nil, errors.New("ask list: exit status 5")
		}
		return json.Marshal(list)
	}
	askFreedShow = func(dir, id string) ([]byte, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		key := dir + "\x00" + id
		f.calls = append(f.calls, key)
		out, ok := f.items[key]
		if !ok || out == "" {
			return nil, errors.New("ask show: exit status 3")
		}
		return []byte(out), nil
	}
	t.Cleanup(func() { askFreedShow, askFreedList = prev, prevList })
	return f
}

func (f *askFreedFakeStore) set(dir, id, stdout string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[dir+"\x00"+id] = stdout
}

func (f *askFreedFakeStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// askFreedItem renders an ask item in `ask show --json` shape. note and
// resolvedAt of "" render as JSON null, as ask does.
func askFreedItem(id, status, note, resolvedAt string) string {
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	var closedAt any
	if status == "closed" {
		closedAt = "2026-11-28T19:40:00.000000Z"
	}
	b, _ := json.MarshalIndent(map[string]any{
		"schema_version":      "1",
		"id":                  id,
		"title":               "probe",
		"body":                "",
		"urgency":             "normal",
		"status":              status,
		"filed_by":            nil,
		"recipient":           nil,
		"tracker_ref":         nil,
		"verifier":            nil,
		"links":               []any{},
		"blocks":              []any{},
		"resolution_note":     nullable(note),
		"verification_output": nil,
		"created_at":          "2026-11-28T19:32:07.703256Z",
		"resolved_at":         nullable(resolvedAt),
		"verified_at":         nil,
		"closed_at":           closedAt,
	}, "", "  ")
	return string(b) + "\n"
}

const askFreedResolvedAt = "2026-11-28T19:32:11.859017Z"

// askFreedReadyRow returns the ready row for id, or nil.
func askFreedReadyRow(t *testing.T, root, id string) *ReadyIssue {
	t.Helper()
	out, code := RunReady(root, ReadyOptions{NoFetch: true})
	if code != 0 {
		t.Fatalf("ready: code = %d, out=%+v", code, out)
	}
	res := out.(ReadyResult)
	for i := range res.Ready {
		if res.Ready[i].ID == id {
			return &res.Ready[i]
		}
	}
	return nil
}

func TestAskFreed_ParseRef(t *testing.T) {
	cases := []struct {
		ref      string
		ok       bool
		id, repo string
	}{
		{"ask-1a2b", true, "ask-1a2b", ""},
		{"ask:ask-1a2b", true, "ask-1a2b", ""},
		{"mac-mini-setup:ask-f3ae", true, "ask-f3ae", "mac-mini-setup"},
		{"linear:ENG-1", false, "", ""},
		{"waiting on Andrew (ask-1a2b)", false, "", ""},
		{"ask-1a2b ", false, "", ""},
		{"ASK-1A2B", false, "", ""},
		{"ask-12", false, "", ""},
		{"../x:ask-1a2b", false, "", ""},
		{"a/b:ask-1a2b", false, "", ""},
		{"..:ask-1a2b", false, "", ""},
		{"gh:org/repo#42", false, "", ""},
	}
	for _, c := range cases {
		got, ok := askFreedParseRef(c.ref)
		if ok != c.ok || got.id != c.id || got.repo != c.repo {
			t.Errorf("askFreedParseRef(%q) = (%+v, %v), want ({id:%s repo:%s}, %v)", c.ref, got, ok, c.id, c.repo, c.ok)
		}
	}
}

// TestDocClaim_AskFreed_ReadyAdmitsResolvedAsk pins the docs/spec.md "Ask
// refs" claim at RunReady: a ticket whose only external dep is an ask form
// is hidden while the ask is open and ready once it is resolved, carrying
// freed_by with the answer — for all three forms, the sibling form reading
// the sibling folder's store. The dep itself is never removed.
func TestDocClaim_AskFreed_ReadyAdmitsResolvedAsk(t *testing.T) {
	fake := askFreedInstallFake(t)
	root, bare := makeUpdateRepoWithIssue(t)
	qualified := mustCreate(t, root, "qualified")
	sib := mustCreate(t, root, "sibling")
	sibDir := filepath.Join(filepath.Dir(root), "askfreed-sib")
	if err := os.MkdirAll(sibDir, 0o755); err != nil {
		t.Fatal(err)
	}
	addExt(t, root, bare, "ask-1a2b")
	addExt(t, root, qualified, "ask:ask-5e6f")
	addExt(t, root, sib, "askfreed-sib:ask-3c4d")

	fake.set(root, "ask-1a2b", askFreedItem("ask-1a2b", "open", "", ""))
	fake.set(root, "ask-5e6f", askFreedItem("ask-5e6f", "open", "", ""))
	fake.set(sibDir, "ask-3c4d", askFreedItem("ask-3c4d", "open", "", ""))
	for _, id := range []string{bare, qualified, sib} {
		if r := askFreedReadyRow(t, root, id); r != nil {
			t.Fatalf("%s is ready while its ask is open: %+v", id, r)
		}
	}

	fake.set(root, "ask-1a2b", askFreedItem("ask-1a2b", "resolved", "use the staging key", askFreedResolvedAt))
	fake.set(root, "ask-5e6f", askFreedItem("ask-5e6f", "resolved", "", askFreedResolvedAt))
	fake.set(sibDir, "ask-3c4d", askFreedItem("ask-3c4d", "resolved", "done upstream", askFreedResolvedAt))
	want := map[string]AskFreed{
		bare:      {Ref: "ask-1a2b", AskID: "ask-1a2b", Status: "resolved", Resolution: "use the staging key", ResolvedAt: askFreedResolvedAt},
		qualified: {Ref: "ask:ask-5e6f", AskID: "ask-5e6f", Status: "resolved", Resolution: "", ResolvedAt: askFreedResolvedAt},
		sib:       {Ref: "askfreed-sib:ask-3c4d", AskID: "ask-3c4d", Status: "resolved", Resolution: "done upstream", ResolvedAt: askFreedResolvedAt},
	}
	for id, w := range want {
		r := askFreedReadyRow(t, root, id)
		if r == nil {
			t.Fatalf("%s is not ready after its ask was resolved", id)
		}
		if len(r.FreedBy) != 1 || r.FreedBy[0] != w {
			t.Errorf("%s freed_by = %+v, want [%+v]", id, r.FreedBy, w)
		}
	}

	// The dep is still attached: act derived, it did not write.
	out, code := RunShow(root, ShowOptions{ID: bare})
	if code != 0 {
		t.Fatalf("show: %d", code)
	}
	if refs, _ := out.(ShowResult).Fields["external_deps"].([]string); len(refs) != 1 || refs[0] != "ask-1a2b" {
		t.Errorf("external_deps after freeing = %v, want [ask-1a2b] still attached", refs)
	}
}

// TestDocClaim_AskFreed_UnknownKeepsBlocking pins "An unknown is not a yes":
// every way of failing to learn that the ask was answered keeps the ticket
// out of ready, as do non-ask refs and free text mentioning an ask. The
// one closed-ask case that frees — resolved, then closed — is the control.
func TestDocClaim_AskFreed_UnknownKeepsBlocking(t *testing.T) {
	fake := askFreedInstallFake(t)
	root, _ := makeUpdateRepoWithIssue(t)

	type tc struct {
		name, ref, stdout string
		ready             bool
	}
	cases := []tc{
		{"non-ask ref", "linear:ENG-1", "", false},
		{"free text mentioning an ask", "waiting on Andrew (ask-0a01)", "", false},
		{"store cannot read the ask", "ask-0a02", "", false},
		{"unparseable output", "ask-0a03", "ask: something went wrong\n", false},
		{"answer about another id", "ask-0a04", askFreedItem("ask-0a04ff", "resolved", "x", askFreedResolvedAt), false},
		{"open ask", "ask-0a05", askFreedItem("ask-0a05", "open", "", ""), false},
		{"closed without an answer", "ask-0a06", askFreedItem("ask-0a06", "closed", "won't do", ""), false},
		{"sibling folder missing", "askfreed-nosuch:ask-0a07", askFreedItem("ask-0a07", "resolved", "x", askFreedResolvedAt), false},
		{"unknown status", "ask-0a08", askFreedItem("ask-0a08", "snoozed", "", askFreedResolvedAt), false},
		{"resolved then closed (control)", "ask-0a09", askFreedItem("ask-0a09", "closed", "verified", askFreedResolvedAt), true},
	}
	ids := map[string]string{}
	for _, c := range cases {
		id := mustCreate(t, root, c.name)
		ids[c.name] = id
		addExt(t, root, id, c.ref)
		if c.stdout != "" {
			// The missing-sibling case registers under its would-be dir;
			// the lookup must never reach the fake for it.
			r, _ := askFreedParseRef(c.ref)
			dir := root
			if r.repo != "" {
				dir = filepath.Join(filepath.Dir(root), r.repo)
			}
			fake.set(dir, r.id, c.stdout)
		}
	}
	// A resolved ask beside a still-blocking ref does not free the ticket.
	mixed := mustCreate(t, root, "resolved ask plus a linear ref")
	addExt(t, root, mixed, "ask-0a0a", "linear:ENG-2")
	fake.set(root, "ask-0a0a", askFreedItem("ask-0a0a", "resolved", "x", askFreedResolvedAt))

	for _, c := range cases {
		got := askFreedReadyRow(t, root, ids[c.name]) != nil
		if got != c.ready {
			t.Errorf("%s (%q): ready = %v, want %v", c.name, c.ref, got, c.ready)
		}
	}
	if askFreedReadyRow(t, root, mixed) != nil {
		t.Errorf("ticket with a resolved ask AND linear:ENG-2 is ready; the linear ref must still block")
	}
	for _, call := range fake.calls {
		if strings.Contains(call, "ENG-") || strings.Contains(call, "askfreed-nosuch") || strings.Contains(call, "ask-0a01") {
			t.Errorf("ask was exec'd for a ref that is not a readable ask form: %q", call)
		}
	}
}

// TestAskFreed_NoExecWithoutAskRefs: a store whose external deps are all
// non-ask refs never runs ask, and one ask shared by two tickets is read
// once per invocation.
func TestAskFreed_NoExecWithoutAskRefs(t *testing.T) {
	fake := askFreedInstallFake(t)
	root, id := makeUpdateRepoWithIssue(t)
	addExt(t, root, id, "linear:ENG-1")
	askFreedReadyRow(t, root, id)
	if n := fake.callCount(); n != 0 {
		t.Fatalf("ask exec'd %d time(s) for a store with no ask refs", n)
	}

	a := mustCreate(t, root, "a")
	b := mustCreate(t, root, "b")
	addExt(t, root, a, "ask-7777")
	addExt(t, root, b, "ask:ask-7777")
	fake.set(root, "ask-7777", askFreedItem("ask-7777", "resolved", "", askFreedResolvedAt))
	if askFreedReadyRow(t, root, a) == nil {
		t.Fatalf("a not ready")
	}
	if n := fake.callCount(); n != 1 {
		t.Errorf("one ready pass exec'd ask %d times for one shared ask, want 1", n)
	}
}

// TestDocClaim_AskFreed_GatesAgreeWithReady pins the docs/spec.md claim that
// `act update --claim` and `act close` apply the same rule as ready: an
// answered ask dep does not trip blocked_by_external_dep (so `act next`
// can claim what ready offers, and a moot ticket closes without --force),
// an open one still does, and the envelope lists only what still blocks.
func TestDocClaim_AskFreed_GatesAgreeWithReady(t *testing.T) {
	fake := askFreedInstallFake(t)
	root, freed := makeUpdateRepoWithIssue(t)
	held := mustCreate(t, root, "held")
	addExt(t, root, freed, "ask-1111")
	addExt(t, root, held, "ask-2222", "ask-1111")
	fake.set(root, "ask-1111", askFreedItem("ask-1111", "resolved", "moot now", askFreedResolvedAt))
	fake.set(root, "ask-2222", askFreedItem("ask-2222", "open", "", ""))

	out, code := RunUpdate(root, UpdateOptions{ID: held, Claim: true, Isolated: true})
	if code != 2 {
		t.Fatalf("claim of a ticket with an open ask dep: code = %d, want 2; out=%+v", code, out)
	}
	deps, _ := out.(UpdateErrorOutput).Details["external_deps"].([]string)
	if len(deps) != 1 || deps[0] != "ask-2222" {
		t.Errorf("blocked envelope external_deps = %v, want only the open ask [ask-2222]", deps)
	}
	if _, code := RunClose(root, CloseOptions{ID: held, Isolated: true}); code != 2 {
		t.Errorf("close of a ticket with an open ask dep: code = %d, want 2", code)
	}

	if out, code := RunUpdate(root, UpdateOptions{ID: freed, Claim: true, Isolated: true}); code != 0 {
		t.Fatalf("claim of an ask-freed ticket: code = %d, want 0; out=%+v", code, out)
	}
	if out, code := RunClose(root, CloseOptions{ID: freed, Reason: "moot: answered in ask-1111", Isolated: true}); code != 0 {
		t.Fatalf("close of an ask-freed ticket without --force: code = %d, want 0; out=%+v", code, out)
	}
}

func TestAskFreed_MainRootFromWorktree(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "proj")
	wt := filepath.Join(main, ".claude", "worktrees", "w1")
	if err := os.MkdirAll(filepath.Join(main, ".git", "worktrees", "w1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(main, ".git", "worktrees", "w1")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := askFreedMainRoot(wt); got != main {
		t.Errorf("worktree main root = %q, want %q", got, main)
	}
	if got := askFreedMainRoot(main); got != main {
		t.Errorf("ordinary checkout main root = %q, want itself", got)
	}

	// Bare ask ids from a worktree with no .ask/ of its own read the main
	// checkout's store; sibling ids resolve beside the main checkout.
	a := newAskFreedResolver(wt)
	if got := a.storeDir(askFreedRef{id: "ask-1a2b"}); got != main {
		t.Errorf("worktree store dir = %q, want main checkout %q", got, main)
	}
	if got := a.storeDir(askFreedRef{id: "ask-1a2b", repo: "other"}); got != filepath.Join(base, "other") {
		t.Errorf("sibling store dir = %q, want %q", got, filepath.Join(base, "other"))
	}
	if err := os.MkdirAll(filepath.Join(wt, ".ask"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := newAskFreedResolver(wt).storeDir(askFreedRef{id: "ask-1a2b"}); got != wt {
		t.Errorf("worktree with its own .ask/: store dir = %q, want %q", got, wt)
	}
}

// TestDocClaim_AskFreed_CLINamesFreeingAsk pins the user-visible surfaces
// at the binary boundary, with a stand-in `ask` on PATH: `act ready --json`
// rows carry freed_by with the answer; the human row is tagged and stderr
// prints the answer and both ways on; `act next` hands the answer over
// with the claim; and the freed ticket closes as moot without --force.
func TestDocClaim_AskFreed_CLINamesFreeingAsk(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")
	t.Setenv("ACT_MACHINE", "")

	// Stand-in ask (askBatchStandIn): answers `ask list --all --json` and
	// `ask show <id> --json` from fixtures when run in the site folder.
	writeItem, _ := askBatchStandIn(t, site)

	createOut, _ := mustRunAct(t, site, 0, "create", "needs the key", "--json")
	id := pickIDFromJSON(t, createOut)
	mustRunAct(t, site, 0, "dep", "add", id, "--external", "ask-1a2b")

	writeItem("ask-1a2b", "open", "", "")
	out, _ := mustRunAct(t, site, 0, "ready", "--limit", "0", "--json")
	if strings.Contains(out, id) {
		t.Fatalf("ticket is ready while its ask is open:\n%s", out)
	}

	writeItem("ask-1a2b", "resolved", "Use the staging key.\nIt is in 1Password.", askFreedResolvedAt)
	out, stderr := mustRunAct(t, site, 0, "ready", "--limit", "0", "--json")
	var res struct {
		Ready []struct {
			ID      string     `json:"id"`
			FreedBy []AskFreed `json:"freed_by"`
		} `json:"ready"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("ready --json: %v\n%s", err, out)
	}
	if len(res.Ready) != 1 || res.Ready[0].ID != id {
		t.Fatalf("ready --json after resolve = %s, want exactly %s", out, id)
	}
	fb := res.Ready[0].FreedBy
	if len(fb) != 1 || fb[0].AskID != "ask-1a2b" || fb[0].Ref != "ask-1a2b" || fb[0].Status != "resolved" ||
		fb[0].Resolution != "Use the staging key.\nIt is in 1Password." {
		t.Errorf("freed_by = %+v, want ask-1a2b resolved with its resolution text", fb)
	}
	for _, want := range []string{"ask-1a2b", "Use the staging key.", "It is in 1Password.", "--ext-rm", "act close"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("ready --json stderr missing %q:\n%s", want, stderr)
		}
	}

	text, stderr := mustRunAct(t, site, 0, "ready")
	if !strings.Contains(text, "[freed: ask-1a2b] needs the key") {
		t.Errorf("human ready row does not name the freeing ask:\n%s", text)
	}
	if strings.Contains(text, "staging key") {
		t.Errorf("human ready stdout carries the answer; it belongs on stderr so rows stay one per line:\n%s", text)
	}
	if !strings.Contains(stderr, "answer: Use the staging key.") {
		t.Errorf("human ready stderr missing the answer:\n%s", stderr)
	}

	peek, _ := mustRunAct(t, site, 0, "next", "--peek", "--json")
	if !strings.Contains(peek, `"freed_by"`) || !strings.Contains(peek, "staging key") {
		t.Errorf("next --peek --json missing freed_by:\n%s", peek)
	}
	claim, _ := mustRunAct(t, site, 0, "next", "--isolated", "--json")
	if !strings.Contains(claim, `"claimed":true`) || !strings.Contains(claim, `"freed_by"`) {
		t.Errorf("next --json did not claim the freed ticket with freed_by:\n%s", claim)
	}
	mustRunAct(t, site, 0, "close", id, "--reason", "moot: answered by ask-1a2b", "--isolated", "--json")

	// The act_next MCP tool hands the answer over the same way.
	createOut, _ = mustRunAct(t, site, 0, "create", "second, same ask", "--json")
	id2 := pickIDFromJSON(t, createOut)
	mustRunAct(t, site, 0, "dep", "add", id2, "--external", "ask:ask-1a2b")
	cmd := exec.Command(actBinaryPath, "mcp")
	cmd.Dir = site
	cmd.Stdin = strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"act_next","arguments":{"isolated":true}}}` + "\n")
	mcpOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("act mcp: %v\n%s", err, mcpOut)
	}
	for _, want := range []string{id2, `freed_by`, `ask:ask-1a2b`, `staging key`} {
		if !strings.Contains(string(mcpOut), want) {
			t.Errorf("act_next result missing %q:\n%s", want, mcpOut)
		}
	}
}
