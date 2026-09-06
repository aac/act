package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
)

// DurabilityInfo reports whether the status `act show` just answered with
// rests on ops that are actually in the nested .act/ repo's history.
//
// WHY THIS EXISTS (act-fec192). `act show` folds the op FILES under
// .act/ops/, which is correct and is also why it can answer with a status
// nothing durable supports: an op file exists from the moment it is
// written, and act commits it a step later. Normally that gap is
// milliseconds. With a pre-close gate that runs a test suite it is
// minutes, and on 2026-08-31 three sessions read a close inside that
// window, agreed with each other, and handed the verdict on as
// verification. The gate then failed, the op file was withdrawn, and the
// store disagreed with all three. Nothing on any surface had said the
// answer was provisional.
//
// So show says it. The check is one `git ls-tree` scoped to this issue's
// op directory plus a directory listing — it does not fold anything and
// does not touch the rest of the store.
//
// Both directions are reported because both mean the answer is not the
// durable one:
//
//   - Uncommitted: op files exist that HEAD does not have. The status
//     you were shown is ahead of the record and can still be taken back.
//   - Missing: HEAD has op files the working tree does not. The status
//     you were shown is BEHIND the record — an op was retracted, and
//     `act doctor --check status-vs-oplog` names the recovery command.
type DurabilityInfo struct {
	// Uncommitted names op files present in .act/ops/ for this issue
	// that HEAD does not track, base names only, sorted.
	Uncommitted []string `json:"uncommitted,omitempty"`
	// Missing names op files HEAD tracks for this issue that are absent
	// from .act/ops/, base names only, sorted.
	Missing []string `json:"missing,omitempty"`
}

// Clean reports whether there is nothing to warn about.
func (d *DurabilityInfo) Clean() bool {
	return d == nil || (len(d.Uncommitted) == 0 && len(d.Missing) == 0)
}

// issueDurability compares one issue's op files on disk against the same
// paths at HEAD. It returns nil whenever the question cannot be answered
// (no nested repo, unborn HEAD, git unavailable) — an unanswerable
// durability question must never turn a working `act show` into a
// failure, and a false "not durable" would be worse than silence.
func issueDurability(paths config.LayoutPaths, issueID string) *DurabilityInfo {
	gops := gitops.NewActGitOps(paths.Root)
	if _, err := gops.RunGit("rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return nil
	}
	rel := "ops/" + issueID
	out, err := gops.RunGit("ls-tree", "-r", "--name-only", "HEAD", "--", rel)
	if err != nil {
		return nil
	}
	head := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ".json") {
			head[filepath.Base(line)] = true
		}
	}

	disk := map[string]bool{}
	issueDir := filepath.Join(paths.Ops, issueID)
	shards, err := os.ReadDir(issueDir)
	if err != nil && !os.IsNotExist(err) {
		return nil
	}
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(issueDir, shard.Name()))
		if err != nil {
			return nil
		}
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".json") {
				disk[f.Name()] = true
			}
		}
	}

	info := &DurabilityInfo{}
	for name := range disk {
		if !head[name] {
			info.Uncommitted = append(info.Uncommitted, name)
		}
	}
	for name := range head {
		if !disk[name] {
			info.Missing = append(info.Missing, name)
		}
	}
	sort.Strings(info.Uncommitted)
	sort.Strings(info.Missing)
	if info.Clean() {
		return nil
	}
	return info
}

// FormatDurabilityWarning renders the stderr WARNING for a status that
// the committed op log does not (yet) support, or "" when there is
// nothing to say.
//
// Like FormatRefreshWarning it goes to stderr in BOTH human and --json
// modes: stderr cannot corrupt the JSON document, and the agent most
// likely to act on a provisional close is the one reading `--json`.
func FormatDurabilityWarning(status string, info *DurabilityInfo) string {
	if info.Clean() {
		return ""
	}
	var b strings.Builder
	if len(info.Missing) > 0 {
		fmt.Fprintf(&b, "WARNING: this status (%s) is BEHIND the committed op log: %s HEAD tracks (%s) %s missing from .act/ops/. An op was retracted; run `act doctor --check status-vs-oplog` for the recovery command.\n",
			status,
			countNoun(len(info.Missing), "op file", "op files"),
			strings.Join(info.Missing, ", "),
			wasWere(len(info.Missing)))
	}
	if len(info.Uncommitted) > 0 {
		fmt.Fprintf(&b, "WARNING: this status (%s) is NOT DURABLE yet: %s in .act/ops/ (%s) %s not committed. It is invisible to every other machine, and a failed gate or rollback can still take it back — do not report it as verified.\n",
			status,
			countNoun(len(info.Uncommitted), "op file", "op files"),
			strings.Join(info.Uncommitted, ", "),
			wasWere(len(info.Uncommitted)))
	}
	return b.String()
}
