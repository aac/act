package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/index"
)

// BlocksErrorOutput is the error envelope shared by `act blocks` and
// `act blocked-by`. Both are read-only queries over the blocks-edge graph;
// their only failure modes are a missing .act/, an index error, and id
// resolution (not-found / ambiguous prefix).
type BlocksErrorOutput struct {
	Error      string   `json:"error"`
	Message    string   `json:"message"`
	Candidates []string `json:"candidates,omitempty"`
}

// blocksLoad opens+rebuilds the index, pulls every non-tombstoned row, and
// resolves id (full or unique prefix). Shared by RunBlocks/RunBlockedBy so
// the two queries have identical repo-state and id-resolution semantics.
func blocksLoad(repoRoot, cmd, id string) (rows []index.Row, byID map[string]index.Row, full string, errOut *BlocksErrorOutput, code int) {
	paths := config.Layout(repoRoot)
	if _, err := os.Stat(paths.Root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, "", &BlocksErrorOutput{Error: "no_repo", Message: fmt.Sprintf("act %s: %s/.act not found; run `act init` first", cmd, repoRoot)}, 3
		}
		return nil, nil, "", &BlocksErrorOutput{Error: "no_repo", Message: fmt.Sprintf("act %s: stat %s: %v", cmd, paths.Root, err)}, 3
	}
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		return nil, nil, "", &BlocksErrorOutput{Error: "index_open_failed", Message: err.Error()}, 1
	}
	defer func() { _ = idx.Close() }()
	if err := idx.Rebuild(paths.Ops); err != nil {
		return nil, nil, "", &BlocksErrorOutput{Error: "index_rebuild_failed", Message: err.Error()}, 1
	}
	rows, err = idx.ListAll(index.Filter{})
	if err != nil {
		return nil, nil, "", &BlocksErrorOutput{Error: "index_query_failed", Message: err.Error()}, 1
	}
	byID = make(map[string]index.Row, len(rows))
	knownIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
		knownIDs = append(knownIDs, r.ID)
	}
	var rerr error
	full, rerr = ids.Resolve(id, knownIDs)
	if rerr != nil {
		if errors.Is(rerr, ids.ErrNotFound) {
			return nil, nil, "", &BlocksErrorOutput{Error: "issue_not_found", Message: fmt.Sprintf("act %s: %q: no matching id", cmd, id)}, 3
		}
		var amb *ids.ErrAmbiguousID
		if errors.As(rerr, &amb) {
			cands := amb.Candidates()
			return nil, nil, "", &BlocksErrorOutput{Error: "id_ambiguous", Message: fmt.Sprintf("act %s: prefix %q matches %d issues", cmd, id, len(cands)), Candidates: cands}, 2
		}
		return nil, nil, "", &BlocksErrorOutput{Error: "issue_not_found", Message: rerr.Error()}, 3
	}
	return rows, byID, full, nil, 0
}

// RunBlockedBy returns the ids that block <id> — the parents of <id>'s own
// `blocks` dep edges. Direction is "child IS BLOCKED BY parent" (see
// `act help workflow`): an edge stored on <id> as {parent: B, blocks} means
// B blocks <id>, so B is returned. Dangling parents (tombstoned / unknown)
// are dropped — they cannot block. The result is deduped and sorted so the
// output is stable for shell pipelines.
func RunBlockedBy(repoRoot, id string) (out []string, errOut *BlocksErrorOutput, code int) {
	_, byID, full, e, c := blocksLoad(repoRoot, "blocked-by", id)
	if e != nil {
		return nil, e, c
	}
	seen := map[string]bool{}
	for _, d := range byID[full].Deps {
		if d.EdgeType != "blocks" {
			continue
		}
		if _, ok := byID[d.Parent]; !ok {
			continue // dangling parent cannot block
		}
		if !seen[d.Parent] {
			seen[d.Parent] = true
			out = append(out, d.Parent)
		}
	}
	sort.Strings(out)
	return out, nil, 0
}

// RunBlocks returns the ids that <id> blocks — every issue carrying <id> as
// the parent of a `blocks` edge. The reverse-direction companion to
// RunBlockedBy. Deduped and sorted.
func RunBlocks(repoRoot, id string) (out []string, errOut *BlocksErrorOutput, code int) {
	rows, _, full, e, c := blocksLoad(repoRoot, "blocks", id)
	if e != nil {
		return nil, e, c
	}
	seen := map[string]bool{}
	for _, r := range rows {
		for _, d := range r.Deps {
			if d.EdgeType == "blocks" && d.Parent == full {
				if !seen[r.ID] {
					seen[r.ID] = true
					out = append(out, r.ID)
				}
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil, 0
}
