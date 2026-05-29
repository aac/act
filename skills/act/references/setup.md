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

## Codex sandbox approvals

If the harness is **Codex** (container-based sandbox via `spawn_agent`), several canonical-loop operations may need explicit operator approval or may be blocked by the sandbox policy. Full details are in `SKILL.md` ("Codex sandbox approvals"); the short version for setup:

**Operations that need approval or design decisions at dispatch time:**

- **`git push`** (loop step 5 / `act_finish`) reaches the network. If the sandbox has no outbound access, omit the push from the agent's loop and defer it to the integrator — the same offline/harvest pattern as Phase 1.5. See SKILL.md for the two-option trade-off.
- **Merge flows** (`git merge --ff-only`) need write access to the target working tree. In Codex, merges belong in a container that has write access to the appropriate path; the sub-agent container typically does not.
- **`.act/` within the writable root** — act's nested `.act/` repo must fall inside the container's declared writable root. If `.act/` is mounted separately or at a non-standard path, confirm write access before running any act commands.

**Setup checklist (run once per Codex project configuration):**

1. Confirm `.act/` is within the container's writable root.
2. Decide whether `git push` is in-loop (network-enabled container) or deferred to integrator (offline mode, harvest at teardown).
3. If spawning sub-agents via `spawn_agent`, remember each container gets its own writable filesystem — `act bootstrap-worker` still applies for seeding `.act/` state into each sub-agent.

There is no `permissions.allow` JSON block for Codex; sandbox policy is set at the platform level, not in `.claude/settings.json`. If you're on Claude Code, the section above is the right reference.
