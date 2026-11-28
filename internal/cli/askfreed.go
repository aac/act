package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Ask-freed external deps (act-3731ab).
//
// THE PROBLEM. `ask` (the sibling human-inbox tool, one store per repo at
// <repo>/.ask/) is how an agent parks a ticket on a human: it files an ask
// and attaches it to the ticket with `act dep add <id> --external ask-XXXX`.
// Resolving the ask never touched the ticket, so the dep stayed and `act
// ready` hid the ticket forever — nothing in the loop owned the clear.
//
// THE DESIGN. Read-time derivation: when act evaluates an external dep that
// is a whole-ref ask form, it asks the store, and a RESOLVED ask counts as
// cleared. act never writes, mutates or removes the dep — the op log is
// unchanged, and `act show` still lists the ref. The ticket that this frees
// carries `freed_by` on its ready row so the claimer reads the answer before
// working: an answer can make the ticket moot, in which case the right move
// is to close it, not to do it.
//
// AN UNKNOWN IS NOT A YES. Every failure to learn the ask's state keeps the
// dep blocking: no `ask` binary, a timeout, a non-zero exit (no store, id not
// found), output that does not parse, an id that does not match the one
// asked for, a status other than resolved. An ask closed WITHOUT having been
// resolved (cancelled or dismissed from open; resolved_at is null) also keeps
// blocking — the human never answered. An ask that was resolved and then
// closed (ask's "verified, done" transition; resolved_at is kept) frees the
// ticket like a resolved one: blocking on it again would re-create exactly
// the stuck ticket this exists to end. ask clears resolved_at on reopen, so a
// reopened-then-cancelled ask reads as never answered.
//
// RECOGNISED FORMS, whole ref only:
//
//	ask-XXXX          this repo's store
//	ask:ask-XXXX      this repo's store, explicit
//	<repo>:ask-XXXX   the store of <repo>, a sibling folder of this repo
//
// Free text that merely mentions an ask ("waiting on Andrew (ask-1a2b)") and
// every other ref (`linear:ENG-1`) stay opaque and blocking, as before.

// askFreedIDPattern is ask's id shape: "ask-" + lowercase hex (4 today).
const askFreedIDPattern = `ask-[0-9a-f]{4,16}`

var (
	// askFreedBareRE: `ask-XXXX`.
	askFreedBareRE = regexp.MustCompile(`^` + askFreedIDPattern + `$`)
	// askFreedQualifiedRE: `<qualifier>:ask-XXXX`, qualifier "ask" or a
	// single folder name (no separators, so it cannot climb out).
	askFreedQualifiedRE = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*):(` + askFreedIDPattern + `)$`)
)

// askFreedShowTimeout bounds one `ask show`. It is short on purpose: it sits
// on `act ready`, the most frequent read in the loop, and a slow answer is
// treated exactly like no answer — the dep keeps blocking.
const askFreedShowTimeout = 3 * time.Second

// askFreedShow runs `ask show <id> --json` with dir as the working
// directory and returns stdout. It is a package variable so tests can
// substitute a fake; production code never reassigns it.
var askFreedShow = askFreedShowExec

func askFreedShowExec(dir, id string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), askFreedShowTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ask", "show", id, "--json")
	cmd.Dir = dir
	// The forwarder `ask` is a shell script that may ssh; if the context
	// kills the script while a child still holds stdout, WaitDelay stops
	// Wait from blocking on the orphaned pipe.
	cmd.WaitDelay = 500 * time.Millisecond
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// AskFreed describes one resolved ask that cleared an external dep on a
// ready row. It rides the row as `freed_by` so the claimer reads the answer
// before working the ticket.
type AskFreed struct {
	// Ref is the external dep exactly as attached; it is still on the
	// ticket, and is what `act update --ext-rm` takes.
	Ref string `json:"ref"`
	// AskID is the ask the ref names.
	AskID string `json:"ask_id"`
	// Status is the ask's status: "resolved", or "closed" for one that was
	// resolved and then closed.
	Status string `json:"status"`
	// Resolution is the ask's resolution_note — the answer. Empty when the
	// ask was resolved without a note.
	Resolution string `json:"resolution"`
	// ResolvedAt is the ask's resolved_at stamp.
	ResolvedAt string `json:"resolved_at,omitempty"`
}

// askFreedRef is a parsed ask-form external dep.
type askFreedRef struct {
	id   string
	repo string // "" = this repo's store
}

// askFreedParseRef recognises the whole-ref ask forms. ok is false for
// anything else, which then stays an opaque, blocking ref.
func askFreedParseRef(ref string) (askFreedRef, bool) {
	if askFreedBareRE.MatchString(ref) {
		return askFreedRef{id: ref}, true
	}
	m := askFreedQualifiedRE.FindStringSubmatch(ref)
	if m == nil {
		return askFreedRef{}, false
	}
	q, id := m[1], m[2]
	if q == "ask" {
		return askFreedRef{id: id}, true
	}
	if q == "." || q == ".." {
		return askFreedRef{}, false
	}
	return askFreedRef{id: id, repo: q}, true
}

// askFreedMainRoot returns the main checkout of the repo whose working tree
// is root: root itself, or — when root is a linked worktree, whose `.git` is
// a `gitdir: <main>/.git/worktrees/<name>` pointer — <main>. Read from the
// filesystem, not git: it runs on the ready path only when an ask-form dep
// exists, and must not add a git exec there. Anything unexpected answers
// root.
func askFreedMainRoot(root string) string {
	gitPath := filepath.Join(root, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil || info.IsDir() {
		return root
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return root
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return root
	}
	gd := strings.TrimSpace(line[len(prefix):])
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(root, gd)
	}
	gd = filepath.Clean(gd)
	wts := filepath.Dir(gd)
	if filepath.Base(wts) != "worktrees" || filepath.Base(filepath.Dir(wts)) != ".git" {
		return root
	}
	return filepath.Dir(filepath.Dir(wts))
}

// askFreedResolver answers "does this external dep still block?" for one
// invocation, caching each ask's answer so a ready sweep execs `ask` at
// most once per (store, id).
type askFreedResolver struct {
	repoRoot string
	mainRoot string // lazily computed
	cache    map[string]*AskFreed
	cached   map[string]bool
}

func newAskFreedResolver(repoRoot string) *askFreedResolver {
	return &askFreedResolver{repoRoot: repoRoot, cache: map[string]*AskFreed{}, cached: map[string]bool{}}
}

// storeDir is the directory `ask` runs in for r.
//
// This repo's store is <repoRoot>/.ask when it exists there, else the main
// checkout's (a linked worktree has no .ask/ of its own: ask's store is
// gitignored, and ask resolves it from its working directory only). The
// existence test is a preference, not a requirement: on a machine whose
// `ask` forwards to another host the local folder may hold no .ask/ at all,
// and the forwarder still answers.
//
// <repo>:ask-XXXX names <repo> as a sibling folder of the main checkout —
// act has no registry of repos, and siblings under one workspace folder is
// how the stores are laid out.
func (a *askFreedResolver) storeDir(r askFreedRef) string {
	if a.mainRoot == "" {
		a.mainRoot = askFreedMainRoot(a.repoRoot)
	}
	if r.repo != "" {
		return filepath.Join(filepath.Dir(a.mainRoot), r.repo)
	}
	if _, err := os.Stat(filepath.Join(a.repoRoot, ".ask")); err == nil {
		return a.repoRoot
	}
	return a.mainRoot
}

// freed returns the AskFreed for ref when ref is an ask form whose ask is
// answered, or nil when ref still blocks.
func (a *askFreedResolver) freed(ref string) *AskFreed {
	r, ok := askFreedParseRef(ref)
	if !ok {
		return nil
	}
	dir := a.storeDir(r)
	key := dir + "\x00" + r.id
	if !a.cached[key] {
		a.cache[key] = askFreedLookup(dir, r.id)
		a.cached[key] = true
	}
	hit := a.cache[key]
	if hit == nil {
		return nil
	}
	out := *hit
	out.Ref = ref
	return &out
}

// askFreedLookup reads one ask. nil means "still blocking", for every
// reason an answer could not be established.
func askFreedLookup(dir, id string) *AskFreed {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil
	}
	raw, err := askFreedShow(dir, id)
	if err != nil {
		return nil
	}
	var item struct {
		ID             string  `json:"id"`
		Status         string  `json:"status"`
		ResolutionNote *string `json:"resolution_note"`
		ResolvedAt     *string `json:"resolved_at"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &item); err != nil {
		return nil
	}
	// `ask show` accepts a prefix; insist the store answered about the
	// ask the ref names and nothing else.
	if item.ID != id {
		return nil
	}
	answered := item.ResolvedAt != nil && *item.ResolvedAt != ""
	switch item.Status {
	case "resolved":
	case "closed":
		if !answered {
			return nil // closed without an answer: cancelled or dismissed
		}
	default:
		return nil
	}
	f := &AskFreed{AskID: id, Status: item.Status}
	if item.ResolutionNote != nil {
		f.Resolution = *item.ResolutionNote
	}
	if item.ResolvedAt != nil {
		f.ResolvedAt = *item.ResolvedAt
	}
	return f
}

// split partitions refs into those that still block and the asks that
// cleared the rest. It execs `ask` only for ask-form refs.
func (a *askFreedResolver) split(refs []string) (blocking []string, freed []AskFreed) {
	for _, ref := range refs {
		if f := a.freed(ref); f != nil {
			freed = append(freed, *f)
			continue
		}
		blocking = append(blocking, ref)
	}
	return blocking, freed
}

// FormatAskFreedNotice renders what a claimer must read about rows that are
// ready only because an ask was answered, or "" when there are none. cmd is
// the subcommand printing it ("act ready", "act next").
//
// `act ready` prints it to stderr, like its machine and truncation notices:
// the row stream on stdout is parsed positionally, and the row itself
// already names the ask ("[freed: ask-XXXX]" before the title). `act next`
// prints it on stdout beside the ticket it hands over.
func FormatAskFreedNotice(cmd string, rows []ReadyIssue) string {
	var b strings.Builder
	for _, r := range rows {
		if len(r.FreedBy) == 0 {
			continue
		}
		id := r.ShortID
		if id == "" {
			id = r.ID
		}
		for _, f := range r.FreedBy {
			fmt.Fprintf(&b, "%s: %s is ready because %s was %s — the dep is still attached.\n", cmd, id, f.AskID, f.Status)
			b.WriteString(askFreedIndent("  answer: ", f.Resolution))
			fmt.Fprintf(&b, "  read it first: if it makes the ticket moot, close it (act close %s --reason ...);\n", id)
			fmt.Fprintf(&b, "  otherwise clear the dep and work it (act update %s --ext-rm %q).\n", id, f.Ref)
		}
	}
	return b.String()
}

// askFreedIndent renders a possibly multi-line answer under a label, every
// continuation line indented to match.
func askFreedIndent(label, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return label + "(resolved with no note)\n"
	}
	pad := strings.Repeat(" ", len(label))
	lines := strings.Split(text, "\n")
	var b strings.Builder
	for i, l := range lines {
		if i == 0 {
			b.WriteString(label)
		} else {
			b.WriteString(pad)
		}
		b.WriteString(strings.TrimRight(l, " \t\r"))
		b.WriteString("\n")
	}
	return b.String()
}

// askFreedTitleTag is the marker a freed row wears before its title in
// `act ready` text output.
func askFreedTitleTag(freed []AskFreed) string {
	ids := make([]string, 0, len(freed))
	for _, f := range freed {
		ids = append(ids, f.AskID)
	}
	return "[freed: " + strings.Join(ids, ",") + "]"
}
