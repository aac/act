# act — project setup & install

Read this when bringing act into a new project, or when act isn't yet installed in your environment. Once a project is configured, none of this content needs to be in context — the main SKILL.md handles the per-iteration loop.

## Finding (or installing) the binary

If the project has `.mcp.json` wiring `act_next` / `act_finish` / etc. as MCP tools, prefer those — they compose canonical-loop steps into single tool calls and return `commit_marker` for free.

Otherwise look for the CLI:

- `which act` — installed via brew tap or curl install
- `./bin/act` — project-local build (common during act development)
- if nothing found and you're in the `act` repo itself: `go build -o bin/act ./cmd/act`
- if nothing found and you're not in the act repo: surface to the human; act needs to be installed before the work loop can run.

Run `act help` once at the start of any session to absorb the mechanics. That's the canonical reference; this skill assumes you've read it.

## Claude Code auto-mode permissions

If the harness is in **auto mode**, it may block several canonical-loop steps as "PR-bypass" risks: `git push origin <default-branch>` (loop step 7), the `git merge --ff-only` of worktree branches back to main (integrator step), and even `git checkout main` between iterations. Each is loop-authorized but trips the classifier individually. To opt out for projects using act, drop this in the project's `.claude/settings.json`:

```json
{
  "permissions": {
    "allow": [
      "Bash(git push origin main:*)",
      "Bash(git push origin master:*)",
      "Bash(git merge --ff-only origin/worktree-*:*)",
      "Bash(git checkout main:*)",
      "Bash(git checkout master:*)"
    ]
  }
}
```

(Adjust branch names if the project uses something other than `main`/`master`.) The trade-off: removing these safety nets means the agent's per-issue merge+push proceeds without confirmation. The rationale for accepting that trade in act-using projects: the loop already gates the close+commit cycle behind in-session review, and the value of immediate visibility-to-other-agents (the whole multi-writer story) depends on those merges and pushes landing. Discovered during the aac-website dogfood (2026-05-10): only the `git push` rule was in the original carveout, and the merge step had to be unblocked mid-loop.

If you're not in auto mode, this section is irrelevant — confirms happen normally.

## Bundle strategy (`.act/config.json`)

act's default `bundle_strategy=per_op` auto-commits each op standalone. For a project that mixes code commits with many act ops — the typical case — set `bundle_strategy=per_session` in `.act/config.json`. Under `per_session`, close ops stage and get subsumed by the next `git commit -am '... (act-XXXX)'`, collapsing a typical lifecycle from claim+work+close into claim+work-with-close. The act repo itself runs `per_op` to keep dogfood visibility into every op; set `per_session` in any other project to keep git history readable.
