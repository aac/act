package redaction

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aac/act/internal/op"
)

// Quarantine directories: op files act moved aside, which can hold the same
// text as ops/. They are handled one file at a time, with no slot grouping.
const (
	opsDir        = "ops"
	failedOpsDir  = ".failed-ops"
	collisionsDir = ".collisions"
)

// Counts is what a pass did, or would do, in one issue (or in total).
type Counts struct {
	// OpsRewritten is the number of op files under ops/ written by
	// re-issue: distinct new paths, so two paths of one op that collapse
	// to one file count once.
	OpsRewritten int
	// Replacements is the number of occurrences replaced in those files.
	Replacements int
	// LeftoversRemoved is the number of superseded files deleted: dead
	// slot members under ops/ and pre-redaction copies in quarantine. The
	// originals of files re-issued in this pass are not counted.
	LeftoversRemoved int
	// QuarantinedRewritten is the number of quarantined op files
	// re-issued in place.
	QuarantinedRewritten int
}

func (c *Counts) add(o Counts) {
	c.OpsRewritten += o.OpsRewritten
	c.Replacements += o.Replacements
	c.LeftoversRemoved += o.LeftoversRemoved
	c.QuarantinedRewritten += o.QuarantinedRewritten
}

// slotKey identifies an op without any function of its content
// (docs/redaction-design.md §4): every file of one op, and every re-issue
// of it, shares it.
type slotKey struct {
	issue   string
	opType  string
	wall    int64
	logical uint32
	node    string
}

func keyOf(env op.Envelope) slotKey {
	return slotKey{env.IssueID, env.OpType, env.HLC.Wall, env.HLC.Logical, env.NodeID}
}

func (k slotKey) less(o slotKey) bool {
	if k.issue != o.issue {
		return k.issue < o.issue
	}
	if k.wall != o.wall {
		return k.wall < o.wall
	}
	if k.logical != o.logical {
		return k.logical < o.logical
	}
	if k.node != o.node {
		return k.node < o.node
	}
	return k.opType < o.opType
}

// member is one parsed op file.
type member struct {
	rel   string // slash-separated, relative to the state root
	env   op.Envelope
	body  []byte // the file's bytes
	canon string // canonical envelope bytes; equal bodies compare equal
	gen   int
}

// splitLive applies the fold's slot rule (docs/redaction-design.md §4) to
// the members of one slot: with m the highest generation present, every
// member below m is dead (superseded) when m > 0; otherwise every member
// is live. members is not modified.
//
// This is the same rule internal/fold applies in discoverAndParse; it is
// re-stated here, over the plan's own parsed members, until the fold
// package exports its slot helper (see the package follow-up note).
func splitLive(members []member) (live, dead []member, maxGen int) {
	for _, m := range members {
		if m.gen > maxGen {
			maxGen = m.gen
		}
	}
	for _, m := range members {
		if maxGen > 0 && m.gen < maxGen {
			dead = append(dead, m)
		} else {
			live = append(live, m)
		}
	}
	return live, dead, maxGen
}

func distinctBodies(ms []member) int {
	seen := map[string]bool{}
	for _, m := range ms {
		seen[m.canon] = true
	}
	return len(seen)
}

// write is one planned file write.
type write struct {
	rel  string
	body []byte
}

// Plan is one pass over a state root, decided but not carried out
// (docs/redaction-design.md §5.4). NewPlan builds it reading only; Execute
// carries it out. A Plan with residuals must not be executed by a caller
// that wants the guarantee of §5.6 — the CLI refuses on a first-round
// residual — but Execute does not check: the decision is the caller's.
type Plan struct {
	// Residuals are the paths, slash-separated and relative to the state
	// root, where a raw form of a pattern would remain after this pass: an
	// op file whose bytes still hold one once its text fields are replaced
	// (a structural field, an object key, the envelope, an op type with no
	// text fields), or any other regular file outside .git and outside the
	// derived set. Sorted, no duplicates. Each names the file as it is now.
	Residuals []string
	// Totals sums PerIssue.
	Totals Counts
	// PerIssue holds each issue's counts, for every issue the pass touches
	// under ops/ or in quarantine.
	PerIssue map[string]Counts

	stateRoot string
	own       map[string]bool
	writes    []write
	deletes   []string
	touched   map[string]bool // issues whose ops/ subtree this pass changes
}

// Changes is the number of file writes plus file deletions the plan holds.
// Zero means executing it changes nothing.
func (p *Plan) Changes() int { return len(p.writes) + len(p.deletes) }

// TouchedIssues returns, sorted, the issues whose ops/ subtree executing
// this plan changes (a file written or deleted under ops/<issue>/).
// Quarantined files are outside ops/ and never make an issue touched.
func (p *Plan) TouchedIssues() []string {
	out := make([]string, 0, len(p.touched))
	for id := range p.touched {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// NewPlan scans stateRoot (the .act/ directory) and decides one pass
// (docs/redaction-design.md §5.4). It reads only.
//
// Every *.json under ops/ is parsed with op.UnmarshalTolerant; one that will
// not parse aborts the plan (the fold is already broken, and that is
// doctor's to report). Files are grouped by slot and, per slot:
//
//  1. the members are split into live and dead by the fold's slot rule;
//  2. own-stale rule: if the live members hold two or more different
//     bodies, all marked, and some are in own (written by an earlier pass
//     of this same run) and some are not, the own ones are moved to dead —
//     the foreign body already carries another redaction this run has not
//     applied, and re-issuing from it keeps both (§6.3);
//  3. the patterns are applied to each live member; if nothing in the slot
//     changes, the dead files are deleted and nothing else is done;
//  4. otherwise every live member, changed or not, is re-issued at the
//     slot's highest generation + 1 into the directory of the file it
//     replaces, and every file of the slot that is not a new path is
//     deleted.
//
// *.json files under .failed-ops/ and .collisions/ are handled one at a
// time: one whose slot has a higher-generation member under ops/ is a
// pre-redaction copy and is deleted; one with a hit is re-issued in place at
// its own generation + 1. A quarantined file that does not parse is not
// fatal; it is scanned raw like any other file.
//
// own holds paths (slash-separated, relative to stateRoot) written by
// earlier passes of the same run; Execute adds to it. A nil own means no
// earlier pass.
func NewPlan(stateRoot string, patterns []string, own map[string]bool) (*Plan, error) {
	if len(patterns) == 0 {
		return nil, errors.New("redaction: plan: no patterns")
	}
	patterns = ordered(patterns)
	forms := rawForms(patterns)
	p := &Plan{
		PerIssue:  map[string]Counts{},
		stateRoot: stateRoot,
		own:       own,
		touched:   map[string]bool{},
	}

	slots := map[slotKey][]member{}
	var quarantined []member
	residual := map[string]bool{}

	err := walkFiles(stateRoot, func(rel, abs string) error {
		if isDerived(rel) {
			return nil
		}
		top, _, _ := strings.Cut(rel, "/")
		isJSON := strings.HasSuffix(strings.ToLower(rel), ".json")
		body, rerr := os.ReadFile(abs)
		if rerr != nil {
			return fmt.Errorf("redaction: read %s: %w", rel, rerr)
		}
		switch {
		case top == opsDir && isJSON:
			m, perr := parseMember(rel, body)
			if perr != nil {
				return perr
			}
			k := keyOf(m.env)
			slots[k] = append(slots[k], m)
			return nil
		case (top == failedOpsDir || top == collisionsDir) && isJSON:
			if m, perr := parseMember(rel, body); perr == nil {
				quarantined = append(quarantined, m)
				return nil
			}
			// Not an op file act can read: fall through to the raw scan.
		}
		if containsAny(body, forms) {
			residual[rel] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	keys := make([]slotKey, 0, len(slots))
	for k := range slots {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].less(keys[j]) })
	opsGen := map[slotKey]int{}
	for _, k := range keys {
		ms := slots[k]
		sort.Slice(ms, func(i, j int) bool { return ms[i].rel < ms[j].rel })
		_, _, g := splitLive(ms)
		opsGen[k] = g
		if err := p.planSlot(k, ms, patterns, forms, residual); err != nil {
			return nil, err
		}
	}

	sort.Slice(quarantined, func(i, j int) bool { return quarantined[i].rel < quarantined[j].rel })
	for _, q := range quarantined {
		if err := p.planQuarantined(q, opsGen, patterns, forms, residual); err != nil {
			return nil, err
		}
	}

	for rel := range residual {
		p.Residuals = append(p.Residuals, rel)
	}
	sort.Strings(p.Residuals)
	for _, c := range p.PerIssue {
		p.Totals.add(c)
	}
	return p, nil
}

func parseMember(rel string, body []byte) (member, error) {
	env, err := op.UnmarshalTolerant(body)
	if err != nil {
		return member{}, fmt.Errorf("redaction: parse %s: %w", rel, err)
	}
	canon, err := env.Marshal()
	if err != nil {
		return member{}, fmt.Errorf("redaction: parse %s: %w", rel, err)
	}
	return member{rel: rel, env: env, body: body, canon: string(canon), gen: Generation(env)}, nil
}

func (p *Plan) bump(issue string, c Counts) {
	cur := p.PerIssue[issue]
	cur.add(c)
	p.PerIssue[issue] = cur
}

func (p *Plan) planSlot(k slotKey, ms []member, patterns []string, forms [][]byte, residual map[string]bool) error {
	live, dead, maxGen := splitLive(ms)

	// Own-stale rule (§5.4 step 2, §6.3).
	if maxGen > 0 && distinctBodies(live) >= 2 {
		var mine, foreign []member
		for _, m := range live {
			if p.own[m.rel] {
				mine = append(mine, m)
			} else {
				foreign = append(foreign, m)
			}
		}
		if len(mine) > 0 && len(foreign) > 0 {
			live = foreign
			dead = append(dead, mine...)
		}
	}

	type applied struct {
		m   member
		out op.Envelope
		n   int
	}
	results := make([]applied, 0, len(live))
	changed := false
	for _, m := range live {
		out, n, err := apply(m.env, patterns)
		if err != nil {
			return fmt.Errorf("redaction: %s: %w", m.rel, err)
		}
		if n > 0 {
			changed = true
		}
		results = append(results, applied{m, out, n})
	}

	if !changed {
		for _, d := range dead {
			p.deletes = append(p.deletes, d.rel)
			p.touched[k.issue] = true
			p.bump(k.issue, Counts{LeftoversRemoved: 1})
		}
		for _, r := range results {
			if containsAny(r.m.body, forms) {
				residual[r.m.rel] = true
			}
		}
		return nil
	}

	gen := maxGen + 1
	newRels := map[string]bool{}
	for _, r := range results {
		re, err := Reissue(r.out, gen)
		if err != nil {
			return fmt.Errorf("redaction: %s: %w", r.m.rel, err)
		}
		body, err := re.Marshal()
		if err != nil {
			return fmt.Errorf("redaction: %s: %w", r.m.rel, err)
		}
		if containsAny(body, forms) {
			residual[r.m.rel] = true
		}
		rel := path.Join(path.Dir(r.m.rel), op.Filename(re))
		if newRels[rel] {
			// Two paths of one op (or two bodies the patterns made equal)
			// collapse to one file here.
			continue
		}
		newRels[rel] = true
		p.writes = append(p.writes, write{rel: rel, body: body})
		p.touched[k.issue] = true
		p.bump(k.issue, Counts{OpsRewritten: 1, Replacements: r.n})
	}
	for _, m := range ms {
		if newRels[m.rel] {
			continue
		}
		p.deletes = append(p.deletes, m.rel)
		p.touched[k.issue] = true
	}
	for _, d := range dead {
		if !newRels[d.rel] {
			p.bump(k.issue, Counts{LeftoversRemoved: 1})
		}
	}
	return nil
}

func (p *Plan) planQuarantined(q member, opsGen map[slotKey]int, patterns []string, forms [][]byte, residual map[string]bool) error {
	issue := q.env.IssueID
	if g, ok := opsGen[keyOf(q.env)]; ok && g > q.gen {
		p.deletes = append(p.deletes, q.rel)
		p.bump(issue, Counts{LeftoversRemoved: 1})
		return nil
	}
	out, n, err := apply(q.env, patterns)
	if err != nil {
		return fmt.Errorf("redaction: %s: %w", q.rel, err)
	}
	if n == 0 {
		if containsAny(q.body, forms) {
			residual[q.rel] = true
		}
		return nil
	}
	re, err := Reissue(out, q.gen+1)
	if err != nil {
		return fmt.Errorf("redaction: %s: %w", q.rel, err)
	}
	body, err := re.Marshal()
	if err != nil {
		return fmt.Errorf("redaction: %s: %w", q.rel, err)
	}
	if containsAny(body, forms) {
		residual[q.rel] = true
	}
	rel := path.Join(path.Dir(q.rel), op.Filename(re))
	p.writes = append(p.writes, write{rel: rel, body: body})
	if rel != q.rel {
		p.deletes = append(p.deletes, q.rel)
	}
	p.bump(issue, Counts{QuarantinedRewritten: 1})
	return nil
}

// removeFile is os.Remove; a test seam so the write-before-delete order can
// be exercised by failing the delete step.
var removeFile = os.Remove

// Execute carries the plan out: every write first, then every deletion, so
// at every instant the tree folds correctly — with a re-issued file and its
// original both present the fold's slot rule already prefers the re-issued
// one, and a pass killed in the middle leaves a tree that reads as redacted
// and that the next pass finishes (its plan finds the originals as dead
// members).
//
// Each write goes through op.WriteAtomic (fsync, rename, read-back) into
// the directory of the file it replaces. A target that already holds
// identical bytes is that file and is left alone; one that holds different
// bytes is an error. Each path written is added to the plan's own map.
// Deleting a file that is already gone is not an error.
//
// Execute writes no record op and stages nothing: that is the caller's.
// It returns the issues whose ops/ subtree changed, sorted, and the
// per-issue counts.
func (p *Plan) Execute() (touchedIssues []string, perIssue map[string]Counts, err error) {
	for _, w := range p.writes {
		abs := filepath.Join(p.stateRoot, filepath.FromSlash(w.rel))
		existing, rerr := os.ReadFile(abs)
		switch {
		case rerr == nil:
			if bytes.Equal(existing, w.body) {
				continue
			}
			return nil, nil, fmt.Errorf("redaction: write %s: a different file already holds that name", w.rel)
		case !errors.Is(rerr, fs.ErrNotExist):
			return nil, nil, fmt.Errorf("redaction: write %s: %w", w.rel, rerr)
		}
		if err := op.WriteAtomic(filepath.Dir(abs), abs, w.body); err != nil {
			return nil, nil, fmt.Errorf("redaction: write %s: %w", w.rel, err)
		}
		if p.own != nil {
			p.own[w.rel] = true
		}
	}
	for _, rel := range p.deletes {
		abs := filepath.Join(p.stateRoot, filepath.FromSlash(rel))
		if err := removeFile(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("redaction: delete %s: %w", rel, err)
		}
	}
	out := make(map[string]Counts, len(p.PerIssue))
	for k, v := range p.PerIssue {
		out[k] = v
	}
	return p.TouchedIssues(), out, nil
}
