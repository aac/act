package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for batched ask lookups (act-a1a83a): one `ask list --all --json`
// per store per invocation, `ask show` only for ids the list lacked, and a
// failed list keeping every ref in its store blocking.

// askBatchCalls splits the fake's call log into list calls (by dir) and
// show calls.
func askBatchCalls(f *askFreedFakeStore) (lists []string, shows []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if dir, ok := strings.CutPrefix(c, "list\x00"); ok {
			lists = append(lists, dir)
			continue
		}
		shows = append(shows, c)
	}
	return lists, shows
}

// TestAskBatch_OneListPerStore: N ask-form refs in this repo's store and
// two in a sibling store, every form represented, cost exactly one list
// call per store and no show calls, and every resolved ask still frees
// its ticket while the open ones still block.
func TestAskBatch_OneListPerStore(t *testing.T) {
	fake := askFreedInstallFake(t)
	root, _ := makeUpdateRepoWithIssue(t)
	sibDir := filepath.Join(filepath.Dir(root), "askbatch-sib")
	if err := os.MkdirAll(sibDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const n = 6
	want := map[string]bool{} // ticket id → should be ready
	for i := 0; i < n; i++ {
		askID := fmt.Sprintf("ask-b%03x", i)
		ref := askID
		if i%2 == 1 {
			ref = "ask:" + askID
		}
		id := mustCreate(t, root, "local "+askID)
		addExt(t, root, id, ref)
		if i%3 == 2 {
			fake.set(root, askID, askFreedItem(askID, "open", "", ""))
			want[id] = false
		} else {
			fake.set(root, askID, askFreedItem(askID, "resolved", "answer "+askID, askFreedResolvedAt))
			want[id] = true
		}
	}
	for i, status := range []string{"resolved", "open"} {
		askID := fmt.Sprintf("ask-c%03x", i)
		id := mustCreate(t, root, "sibling "+askID)
		addExt(t, root, id, "askbatch-sib:"+askID)
		resolvedAt := ""
		if status == "resolved" {
			resolvedAt = askFreedResolvedAt
		}
		fake.set(sibDir, askID, askFreedItem(askID, status, "sib answer", resolvedAt))
		want[id] = status == "resolved"
	}

	out, code := RunReady(root, ReadyOptions{NoFetch: true, Limit: 0})
	if code != 0 {
		t.Fatalf("ready: code = %d, out=%+v", code, out)
	}
	ready := map[string]ReadyIssue{}
	for _, r := range out.(ReadyResult).Ready {
		ready[r.ID] = r
	}
	for id, w := range want {
		r, got := ready[id]
		if got != w {
			t.Errorf("%s ready = %v, want %v", id, got, w)
		}
		if got && (len(r.FreedBy) != 1 || r.FreedBy[0].Resolution == "") {
			t.Errorf("%s freed_by = %+v, want one answer", id, r.FreedBy)
		}
	}

	lists, shows := askBatchCalls(fake)
	if len(lists) != 2 || lists[0] == lists[1] {
		t.Errorf("list calls = %q, want exactly one per store (2 stores)", lists)
	}
	for _, d := range lists {
		if d != root && d != sibDir {
			t.Errorf("list call in unexpected dir %q", d)
		}
	}
	if len(shows) != 0 {
		t.Errorf("show calls = %q, want none: every id was in its store's list", shows)
	}
}

// TestAskBatch_ShowOnlyForIDsTheListLacked: an id missing from a good list
// is looked up with exactly one `ask show`, and a resolved answer there
// still frees the ticket.
func TestAskBatch_ShowOnlyForIDsTheListLacked(t *testing.T) {
	fake := askFreedInstallFake(t)
	root, listed := makeUpdateRepoWithIssue(t)
	missing := mustCreate(t, root, "not in the list")
	addExt(t, root, listed, "ask-d001")
	addExt(t, root, missing, "ask-d002")
	fake.set(root, "ask-d001", askFreedItem("ask-d001", "resolved", "x", askFreedResolvedAt))

	// Stand in for a store whose list omits ask-d002 but whose show has
	// it: install a list that returns only ask-d001.
	prevList := askFreedList
	askFreedList = func(dir string) ([]byte, error) {
		fake.mu.Lock()
		fake.calls = append(fake.calls, "list\x00"+dir)
		fake.mu.Unlock()
		return []byte("[" + askFreedItem("ask-d001", "resolved", "x", askFreedResolvedAt) + "]"), nil
	}
	t.Cleanup(func() { askFreedList = prevList })
	fake.set(root, "ask-d002", askFreedItem("ask-d002", "resolved", "via show", askFreedResolvedAt))

	if askFreedReadyRow(t, root, listed) == nil || askFreedReadyRow(t, root, missing) == nil {
		t.Fatalf("both tickets should be ready: one answered in the list, one by show")
	}
	// Two ready passes above: one list each, and one show each, for d002.
	lists, shows := askBatchCalls(fake)
	if len(lists) != 2 {
		t.Errorf("list calls = %d over two ready passes, want 2", len(lists))
	}
	if len(shows) != 2 || !strings.HasSuffix(shows[0], "\x00ask-d002") || !strings.HasSuffix(shows[1], "\x00ask-d002") {
		t.Errorf("show calls = %q, want one per pass, for ask-d002 only", shows)
	}
}

// TestDocClaim_AskFreed_ListFailureKeepsBlocking: a list that fails (error or
// timeout) keeps every ref in that store blocking, with no per-id show
// fallback, so one slow store costs one bounded call.
func TestDocClaim_AskFreed_ListFailureKeepsBlocking(t *testing.T) {
	fake := askFreedInstallFake(t)
	fake.listErr = true
	root, a := makeUpdateRepoWithIssue(t)
	b := mustCreate(t, root, "b")
	addExt(t, root, a, "ask-e001")
	addExt(t, root, b, "ask:ask-e002")
	fake.set(root, "ask-e001", askFreedItem("ask-e001", "resolved", "x", askFreedResolvedAt))
	fake.set(root, "ask-e002", askFreedItem("ask-e002", "resolved", "y", askFreedResolvedAt))

	out, code := RunReady(root, ReadyOptions{NoFetch: true})
	if code != 0 {
		t.Fatalf("ready: code = %d", code)
	}
	for _, r := range out.(ReadyResult).Ready {
		if r.ID == a || r.ID == b {
			t.Errorf("%s is ready although its store's list failed", r.ID)
		}
	}
	lists, shows := askBatchCalls(fake)
	if len(lists) != 1 || len(shows) != 0 {
		t.Errorf("after a failed list: %d list call(s), %d show call(s); want 1 and 0", len(lists), len(shows))
	}
}

// TestDocClaim_AskFreed_BatchedLookup pins the docs/spec.md batching claim
// at the binary boundary, with a stand-in `ask` that logs every exec: one
// `act ready` over several ask-form refs in one store runs `ask list --all
// --json` exactly once and `ask show` not at all, still freeing the
// resolved asks and holding the open one.
func TestDocClaim_AskFreed_BatchedLookup(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")
	t.Setenv("ACT_MACHINE", "")
	writeItem, callLog := askBatchStandIn(t, site)

	refs := []struct{ ref, ask, status string }{
		{"ask-f001", "ask-f001", "resolved"},
		{"ask:ask-f002", "ask-f002", "resolved"},
		{"ask-f003", "ask-f003", "open"},
		{"ask-f004", "ask-f004", "resolved"},
	}
	wantReady := map[string]bool{}
	for _, r := range refs {
		createOut, _ := mustRunAct(t, site, 0, "create", "on "+r.ask, "--json")
		id := pickIDFromJSON(t, createOut)
		mustRunAct(t, site, 0, "dep", "add", id, "--external", r.ref)
		resolvedAt := ""
		if r.status == "resolved" {
			resolvedAt = askFreedResolvedAt
		}
		writeItem(r.ask, r.status, "answer for "+r.ask, resolvedAt)
		wantReady[id] = r.status == "resolved"
	}

	_ = os.Remove(callLog)
	out, _ := mustRunAct(t, site, 0, "ready", "--limit", "0", "--json")
	var res struct {
		Ready []struct {
			ID      string     `json:"id"`
			FreedBy []AskFreed `json:"freed_by"`
		} `json:"ready"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("ready --json: %v\n%s", err, out)
	}
	got := map[string]bool{}
	for _, r := range res.Ready {
		got[r.ID] = len(r.FreedBy) == 1
	}
	for id, w := range wantReady {
		if got[id] != w {
			t.Errorf("%s ready-with-freed_by = %v, want %v\n%s", id, got[id], w, out)
		}
	}

	logBytes, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	calls := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
	if len(calls) != 1 || calls[0] != "list --all --json" {
		t.Errorf("ask execs for one ready over %d ask refs = %q, want exactly [\"list --all --json\"]", len(refs), calls)
	}
}
