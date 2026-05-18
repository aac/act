# act — worktree subagent dispatch

Read this when about to spawn a sub-agent for act work. The high-level rules (`isolation: "worktree"` default, override the loop in the prompt so worktree agents push to their branch not main) live in the main SKILL.md. This file holds the specific traps and rationale.

## The `--push` trap (until act-5d6a lands)

When a worktree subagent calls `act create --push`, `act update --push`, or `act close --push`, the auto-commit lands on the worktree branch but `--push` follows the branch's tracking config — often `origin/main` rather than the worktree branch. The act-op commit jumps ahead of work commits onto main.

Real damage in the aac-website dogfood (act-8808): three `act create --push` calls from a worktree subagent committed to origin/main and required cherry-picking the work commit separately.

Until `--branch <ref>` ships, worktree dispatchers should either:

- (a) configure `git config push.default upstream` in the worktree before any act op,
- (b) omit `--push` on op commands and let the integrator push, or
- (c) explicitly `git push origin HEAD:<worktree-branch>` after each op.

## Parallelism vs isolation

Parallelism is a separate concern from isolation. Default serial when issues touch overlapping files. Spawn parallel only when issues are provably disjoint.
