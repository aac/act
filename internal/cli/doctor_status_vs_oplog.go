package cli

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/index"
)

// CheckStatusVsOplog is the check name for the status reconciliation
// below. Exported so callers that switch on Finding.Check (cmd/act's
// stderr echo, tests) name it rather than re-spelling the literal.
const CheckStatusVsOplog = "status-vs-oplog"

// checkStatusVsOplog answers, for every issue, the question a reader of
// `act show` actually cares about: is the status I am being told
// supported by the op log?
//
// WHY THIS EXISTS (act-fec192). On 2026-08-31 three sessions read
// act-3b6e58 as closed and handed that verdict to each other as
// verification; sixteen minutes later the same store read it as open,
// and the operator's check — `git log --oneline | grep <id>` — found no
// close op and concluded the index had invented a status. It had not.
// A real close op had been written at 05:06:11.103Z and was sitting in
// ops/ uncommitted while its multi-minute pre-close gate ran; the gate
// then failed, the op file was removed, and an act-sync sweep committed
// the deletion under a subject naming no issue. Every read was faithful
// to the op log it could see. What nobody could see was that the two op
// logs — the files on disk and the history in the nested repo —
// disagreed, and which one their answer had come from.
//
// So this check compares three views per issue and reports where they
// part company:
//
//   - the derived index (.act/index.db), which is what act answers from;
//   - the op files on disk under .act/ops/, which is what the fold reads;
//   - the committed op log at HEAD of the nested .act/ repo, which is
//     the only view that survives a machine, a sweep, or a session.
//
// WHY NOT JUST index-divergence. That check already diffs the index
// against a rebuild from ops/ ON DISK, and it was silent through this
// entire incident — correctly, because the index and the disk agreed at
// every moment. Both were wrong about durability, and neither view can
// tell you so. The committed history is the third view that can.
//
// SEVERITY IS SPLIT ON PURPOSE, because the two directions mean
// opposite things:
//
//   - ops on disk that HEAD does not have (warn): a write in flight, or
//     one whose commit never landed. Legitimate for the moment it takes
//     act to commit an op, and NOT legitimate for the minutes a slow
//     pre-close gate runs — which is exactly the window three readers
//     were misled in. Reporting it says "this answer is not durable
//     yet", which is true in both cases.
//   - ops at HEAD that the working tree no longer has (error): an op
//     that was published and then retracted with no record of the
//     retraction. That is loss, and it is what actually cost this store
//     a close.
//
// A mismatch with no file-level explanation is a stale or corrupt index
// and is also an error; `act doctor --fix` rebuilds it.
func checkStatusVsOplog(paths config.LayoutPaths, foldRes *fold.FoldResult) []Finding {
	if _, err := os.Stat(paths.IndexDB); err != nil {
		// No index to compare. Nothing derived, nothing to diverge.
		return nil
	}
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		// index-malformed / index-divergence own the broken-image
		// surface; staying quiet here keeps one finding per fault.
		return nil
	}
	if icErr := idx.IntegrityCheck(); icErr != nil {
		_ = idx.Close()
		return nil
	}
	defer func() { _ = idx.Close() }()

	rows, err := idx.ListAll(index.Filter{})
	if err != nil {
		return []Finding{{
			Check:    CheckStatusVsOplog,
			Severity: "error",
			Message:  fmt.Sprintf("could not read the index: %v", err),
		}}
	}
	indexStatus := make(map[string]string, len(rows))
	for _, r := range rows {
		indexStatus[r.ID] = r.Status
	}

	diskStatus := map[string]string{}
	if foldRes != nil {
		for id, st := range foldRes.Issues {
			if st.Tombstoned {
				continue
			}
			s, _ := fold.ResolveStatus(st)
			diskStatus[id] = s
		}
	}

	var findings []Finding

	// Layer 1: the index against the op files on disk. Same oracle
	// index-divergence uses, reported per issue and per status so the
	// finding names the issue a reader was misled about.
	//
	// A row MISSING from the index is deliberately NOT a finding. The
	// index is a cache act rebuilds before it is read (`act list` calls
	// index.Rebuild unconditionally; `act show` folds ops/ and never
	// consults the index for status), so an absent row cannot reach a
	// reader as a wrong status — and it is the normal state of a store
	// whose index was just deleted or freshly created. An earlier draft
	// reported it and produced one error per issue on a half-built
	// index, which is exactly the noise that gets a check ignored.
	// Structural index drift is index-divergence's job; this check
	// reports statements, not silences.
	for _, id := range sortedUnion(indexStatus, diskStatus) {
		ix, inIndex := indexStatus[id]
		if !inIndex {
			continue
		}
		dk, onDisk := diskStatus[id]
		switch {
		case onDisk && ix != dk:
			findings = append(findings, Finding{
				Check: CheckStatusVsOplog, Severity: "error", IssueID: id,
				Message: fmt.Sprintf(
					"index says %q; the op log on disk says %q — the index is stale, run `act doctor --fix`",
					ix, dk),
			})
		case !onDisk:
			findings = append(findings, Finding{
				Check: CheckStatusVsOplog, Severity: "error", IssueID: id,
				Message: fmt.Sprintf(
					"index says %q; the op log on disk has no ops for this issue — the index is stale, run `act doctor --fix`",
					ix),
			})
		}
	}

	// Layer 2: what act actually ANSWERS WITH against the COMMITTED op
	// log. The oracle here is diskStatus, not the index, because that is
	// the surface a reader gets: `act show` folds ops/ directly and
	// `act list` rebuilds the index from ops/ before querying it. So the
	// question this layer asks is the one three sessions needed answered
	// — "is the status I was just told durable?" — and the index is not
	// part of it.
	//
	// Only issues whose op-file set differs between HEAD and the working
	// tree can diverge here, so the expensive part (materialising and
	// folding HEAD's ops) runs for those issues and no others —
	// normally none.
	findings = append(findings, checkStatusVsCommittedOplog(paths, diskStatus)...)

	return findings
}

// checkStatusVsCommittedOplog is layer 2 of checkStatusVsOplog: the
// comparison between what act answers with (the fold of ops/ on disk)
// and the nested repo's committed history.
func checkStatusVsCommittedOplog(paths config.LayoutPaths, diskStatus map[string]string) []Finding {
	gops := gitops.NewActGitOps(paths.Root)
	if _, err := gops.RunGit("rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		// No nested repo, or an unborn HEAD: there is no committed op
		// log yet, so there is nothing for the index to disagree with.
		return nil
	}

	committed, err := committedOpPaths(gops)
	if err != nil {
		return []Finding{{
			Check:    CheckStatusVsOplog,
			Severity: "warn",
			Message:  fmt.Sprintf("could not read the committed op log: %v", err),
		}}
	}
	onDisk, err := diskOpPaths(paths)
	if err != nil {
		return []Finding{{
			Check:    CheckStatusVsOplog,
			Severity: "warn",
			Message:  fmt.Sprintf("could not read the op files on disk: %v", err),
		}}
	}

	// Bucket the differing paths by issue.
	uncommitted := map[string][]string{} // on disk, not at HEAD
	retracted := map[string][]string{}   // at HEAD, not on disk
	for p := range onDisk {
		if !committed[p] {
			if id := issueIDFromOpPath(p); id != "" {
				uncommitted[id] = append(uncommitted[id], filepath.Base(p))
			}
		}
	}
	for p := range committed {
		if !onDisk[p] {
			if id := issueIDFromOpPath(p); id != "" {
				retracted[id] = append(retracted[id], filepath.Base(p))
			}
		}
	}

	var findings []Finding
	for _, id := range sortedUnion(uncommitted, retracted) {
		committedState, err := foldCommittedIssue(gops, id)
		if err != nil {
			findings = append(findings, Finding{
				Check: CheckStatusVsOplog, Severity: "warn", IssueID: id,
				Message: fmt.Sprintf("could not fold the committed op log for this issue: %v", err),
			})
			continue
		}
		committedStat, _ := fold.ResolveStatus(committedState)
		if committedState == nil {
			committedStat = "(no ops)"
		}
		answered, known := diskStatus[id]
		if !known {
			answered = "(no ops)"
		}
		if answered == committedStat {
			// The differing op files did not move the status. Not this
			// check's business — it reports status, not file drift.
			continue
		}

		lost := retracted[id]
		pending := uncommitted[id]
		sort.Strings(lost)
		sort.Strings(pending)

		if len(lost) > 0 {
			findings = append(findings, Finding{
				Check: CheckStatusVsOplog, Severity: "error", IssueID: id,
				Message: fmt.Sprintf(
					"act reports %q; the committed op log says %q — %s committed at HEAD %s missing from ops/ (%s). An op was retracted with no record of the retraction; recover it with `git -C .act show HEAD:ops/%s/<shard>/<file>`",
					answered, committedStat,
					countNoun(len(lost), "op file", "op files"), wasWere(len(lost)),
					strings.Join(lost, ", "), id),
			})
			continue
		}
		findings = append(findings, Finding{
			Check: CheckStatusVsOplog, Severity: "warn", IssueID: id,
			Message: fmt.Sprintf(
				"act reports %q; the committed op log says %q — %s in ops/ %s written but not committed (%s). This status is not durable yet: it is invisible to every other machine and a rollback can still take it back",
				answered, committedStat,
				countNoun(len(pending), "op file", "op files"), wasWere(len(pending)),
				strings.Join(pending, ", ")),
		})
	}
	return findings
}

// committedOpPaths returns the set of op-file paths tracked at HEAD,
// relative to the nested repo root (e.g. "ops/act-3b6e58/2026-09/x.json").
func committedOpPaths(gops *gitops.ActGitOps) (map[string]bool, error) {
	out, err := gops.RunGit("ls-tree", "-r", "--name-only", "HEAD", "--", "ops")
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ".json") {
			set[line] = true
		}
	}
	return set, nil
}

// diskOpPaths returns the same set for the working tree, in the same
// repo-relative form so the two are directly comparable.
func diskOpPaths(paths config.LayoutPaths) (map[string]bool, error) {
	set := map[string]bool{}
	entries, err := os.ReadDir(paths.Ops)
	if err != nil {
		if os.IsNotExist(err) {
			return set, nil
		}
		return nil, err
	}
	for _, issueDir := range entries {
		if !issueDir.IsDir() {
			continue
		}
		shards, err := os.ReadDir(filepath.Join(paths.Ops, issueDir.Name()))
		if err != nil {
			return nil, err
		}
		for _, shard := range shards {
			if !shard.IsDir() {
				continue
			}
			files, err := os.ReadDir(filepath.Join(paths.Ops, issueDir.Name(), shard.Name()))
			if err != nil {
				return nil, err
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
					continue
				}
				set[path3("ops", issueDir.Name(), shard.Name(), f.Name())] = true
			}
		}
	}
	return set, nil
}

// foldCommittedIssue materialises one issue's ops as HEAD has them into a
// scratch directory and folds them, returning the state the committed op
// log supports. A nil state (with a nil error) means HEAD carries no ops
// for this issue at all.
//
// The working tree is never touched: `git archive` writes a tar to
// stdout, which is unpacked under t.TempDir()-style scratch space. Using
// the real fold rather than a bespoke "look for a close op" scan is what
// keeps this check honest when reopen, LWW ties, or a future op type
// change what a sequence of ops means.
func foldCommittedIssue(gops *gitops.ActGitOps, issueID string) (*fold.IssueState, error) {
	rel := path3("ops", issueID)
	tarBytes, err := gops.RunGit("archive", "--format=tar", "HEAD", "--", rel)
	if err != nil {
		// The pathspec matching nothing means HEAD has no ops here.
		return nil, nil //nolint:nilerr // absence is an answer, not a failure
	}

	scratch, err := os.MkdirTemp("", "act-doctor-oplog-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	if err := untarInto(strings.NewReader(tarBytes), scratch); err != nil {
		return nil, err
	}
	opsRoot := filepath.Join(scratch, "ops")
	if _, err := os.Stat(opsRoot); err != nil {
		return nil, nil
	}
	return fold.FoldIssue(opsRoot, issueID, fold.ApplyDispatch)
}

// untarInto unpacks a git-archive tar stream under dest. Only regular
// files under the archive's own relative paths are written; anything
// that would escape dest is refused rather than skipped, because a
// diagnostic that silently drops entries would report the wrong status.
func untarInto(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		target := filepath.Join(dest, filepath.Clean("/"+hdr.Name))
		if !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry %q escapes the scratch dir", hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // git-archive output, bounded by the repo
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
}

// issueIDFromOpPath pulls the issue id out of "ops/<id>/<shard>/<file>".
func issueIDFromOpPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) < 2 || parts[0] != "ops" {
		return ""
	}
	return parts[1]
}

// path3 joins path segments with "/" regardless of host separator: these
// are git paths, which are always slash-separated.
func path3(parts ...string) string { return strings.Join(parts, "/") }

// sortedUnion returns the sorted union of two maps' keys, so findings
// come out in a stable order across runs.
func sortedUnion[A any, B any](a map[string]A, b map[string]B) []string {
	seen := map[string]struct{}{}
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// countNoun renders "1 op file" / "3 op files".
func countNoun(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// wasWere agrees the verb with countNoun's number.
func wasWere(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
