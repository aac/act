// Package skill embeds the canonical act skill file (and its reference
// docs) at build time so the act binary can install them into a user's
// Claude Code skills directory via `act install-skill`.
//
// The skill is the runtime workflow document agents read whenever they
// land in a repo using act. Today the canonical copy lives at
// ~/.claude/skills/act/SKILL.md, which is not itself under version
// control. Embedding it in the binary makes the act repo the single
// source of truth: edits land here, ship with the next release, and
// install-skill propagates them to every machine that has the binary.
//
// The embedded file set mirrors the on-disk skill layout exactly:
//
//	SKILL.md                                # required
//	references/setup.md                     # read once per project
//	references/worktree-subagents.md        # read before dispatching sub-agents
//
// New reference files added to ./references/ are automatically included
// in the embed because the directive uses a glob.
package skill

import (
	"embed"
	"io/fs"
)

//go:embed SKILL.md references/*.md
var files embed.FS

// FS returns the embedded skill file tree rooted at the package
// directory. Callers walk it with fs.WalkDir to copy each entry to the
// destination skills dir. The returned FS is read-only.
func FS() fs.FS {
	return files
}

// SkillMD returns the bytes of the top-level SKILL.md. Provided as a
// convenience for callers that only need the canonical workflow file
// (e.g. a future hypothetical `act help skill` that prints it).
func SkillMD() ([]byte, error) {
	return files.ReadFile("SKILL.md")
}
