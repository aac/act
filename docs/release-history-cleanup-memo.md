---
date: 2026-05-27
ticket: act-cb9750
topic: commit-history cleanup for public release
---

# Release history cleanup memo

## 1. Survey of the current log shape

**Volume.** 259 commits spanning 2026-04-29 through 2026-05-27 (~4 weeks). Three burst days dominate: 2026-04-29 (68 commits, pre-v0.1 build pipeline), 2026-05-10 (61 commits, v0.2 dogfood grind), and 2026-05-19 (58 commits, Phase 2 coordination-plane work).

**Era breakdown.**

| Era | Range | Commits | Character |
|-----|-------|---------|-----------|
| Pre-v0.1 | root → v0.1.0 | 82 | Scaffolding, spec review, stage N orchestration |
| v0.1.0 → v0.2.0 | tag range | 70 | Active feature build + dogfood fixes |
| Post-v0.2.0 | tag → HEAD | 107 | Phase 1/2 implementation, release prep |

**Commit categories (all 259).**

- **Implementation/features** (~135 commits, ~52 %): Substantive code changes — `feat:`, `fix:`, `ci:`, `test:`, `chore:`, `phase 1/2 ticket N`, `act-XXXX:`, `mcp:`, `cli:`, `gitops:`, `remote-enable:`, etc. These carry the product narrative.
- **docs: handoff/session docs** (~22 commits, ~9 %): Pure `docs: refresh session-handoff.md` or `docs: session handoff …`. Agent bookkeeping. Zero product content.
- **docs: design/plan/brief/review artifacts** (~20 commits, ~8 %): Phase 2 briefs, coordination-plane designs, review synthesis docs. These *are* substantive — they show the design reasoning for Phase 1/2 architecture. Reasonable to preserve.
- **docs: other** (~13 commits, ~5 %): README rewrites, spec clarifications, orchestration design, evaluation. Substantive.
- **review: commits** (6 commits, ~2 %): `review: phase 2 plan v1 — architect`, etc. Parallel-reviewer synthesis artifacts. Interesting process artifact; non-essential to the code narrative.
- **Pre-v0.1 scaffolding** (~33 commits, ~13 %): `Initial scaffold`, `stage 1` through `stage 8`, `dogfood report`, `usability review`, `surface gap analysis`. These document the dispatcher-pipeline build process that created v0.1.0. Noisy to an outside reader but historically interesting.
- **fmt: / merge:/ triage: / misc** (~30 commits, ~12 %): gofmt fixups, merge commits that resolved worktree conflicts, triage notes.

**Phase 1 migration of op-commits.** The nested `.act/.git` repo now holds all tracker op-commits. The *host* log is clean of op-commits — no `act-op:` commits appear in `git log`. Phase 1 did exactly what it said: the host history is free of tracking noise.

**What a cold reader sees.** The history tells a clear story in three acts: (1) a multi-week scaffolded build pipeline that created v0.1.0, (2) a v0.1→v0.2 dogfood grind that produced the canonical workflow and MCP layer, (3) a Phase 1/2 coordination-plane project that built the nested-repo and remote-sync architecture. The 22 session-handoff docs commits are obvious bookkeeping ("refresh session-handoff.md") and don't obscure the narrative, but they add noise. The `stage N` commits from the pre-v0.1 era read like scaffolding notes to anyone unfamiliar with the dispatcher pipeline — curiosity-inducing but not confusing.

**No op-commits in host repo.** Confirmed: zero commits match `act-op:` prefix. Phase 1 delivered a clean host log.

---

## 2. Options analysis

### (a) Leave as-is

Accept the log as part of the "built in the open via agents" character.

**What you get:** A complete record of how the tool was built — including the dispatcher pipeline, parallel review passes, and session-handoff cadence. The log is readable cold: the major features are all labeled conventionally (`feat:`, `fix:`, `phase 1/2 ticket N`), the session-handoff commits are identifiable as such, and the pre-v0.1 `stage N` commits cluster at the bottom of the log.

**What an outside reader sees:** Moderate noise. The 22 `docs: refresh session-handoff.md` commits and 33 pre-v0.1 `stage N / Initial scaffold` commits (total ~55) add no code content. They're not confusing but they pad the log.

**Blast radius:** None. No URLs break. No tags change. Full reversibility — it's the status quo.

**Effort:** Zero.

**Read-cold experience after change:** Unchanged from today. The log has a clear three-act structure with visible noise.

---

### (b) Squash-merge baseline

Rewrite history to a small number of curated commits (e.g., one per major feature or phase) before the public flip; preserve current history in a `pre-squash` tag.

**What you get:** A clean, curated log. An outside reader would see something like 20–30 commits representing major milestones.

**What gets lost:**
- Bisect granularity — finding the commit that introduced a specific behavior becomes harder.
- The "built in the open" documentary record. The `Act-Id:` trailer correlation between host commits and tracker ops breaks for everything pre-squash.
- All 64 `Act-Id:` trailer work-commit correlations that `act doctor` uses would silently vanish from the rewritten commits (unless painstakingly re-added to each squash commit message).

**Who notices:** Anyone who pinned a specific commit hash (unlikely pre-release). `act doctor` cross-references will silently fail for closed tickets pointing at rewritten hashes.

**Reversibility:** Partial. The `pre-squash` tag preserves the old history but GitHub/remotes that already have the branch would diverge.

**Effort:** Medium. Deciding which commits form each squash boundary is judgment-intensive. The `Act-Id:` trailer preservation question alone is significant work.

**Read-cold experience:** Very clean, but loses the process narrative that differentiates this project from a tool built in private.

---

### (c) Filter-repo cleanup

Drop obviously-noisy commits (pure session-handoff bookkeeping, `fmt:` fixups that contain no logic), keep everything substantive. Use `git filter-repo` to drop or reword specific commits.

**What you get:** Roughly 55 fewer commits (~21 % reduction). The remaining log would be ~204 commits, still telling the complete product narrative without the handoff and gofmt noise.

**What gets lost:**
- The `docs: refresh session-handoff.md` commits (22). These contain no code or product content — their removal loses nothing externally visible.
- The `fmt:` fixups (4). These are pure gofmt one-liners; the diff is absorbed by the adjacent work commit they follow.
- Optionally: the 6 `review:` commits (parallel-reviewer synthesis). These are interesting process artifacts but don't change any code.

**Who notices:** Nobody external (pre-release). `act doctor` `Act-Id:` correlations are unaffected if the non-dropped commits are left intact.

**Reversibility:** None after push. The original history would need to be preserved in a tag (`pre-filter-v0.3.0` or similar) before rewrite.

**Effort:** Low-medium. The `git filter-repo` invocation is mechanical:

```
# Preserve current history in a tag first
git tag pre-filter-backup

# Drop specific commit subjects (dry-run: review output before executing)
git filter-repo --commit-callback '
  msg = commit.message.decode("utf-8")
  skip_patterns = [
      "docs: refresh session-handoff",
      "docs: session handoff for",
      "docs: handoff ",
      "docs: refresh handoff",
      "fmt: gofmt",
  ]
  if any(msg.strip().startswith(p) for p in skip_patterns):
      commit.skip()
'
```

This drops ~26–30 commits (the 22 handoff docs + 4 fmt fixups + the "docs: compound candidates" scratchpad commit). Review the `--dry-run` output before executing.

**Read-cold experience:** Noticeably cleaner. The remaining docs commits (`docs: rewrite README`, `docs: add Documentation discipline`, `docs: coordination-plane design brief`, etc.) are all substantive. The pre-v0.1 `stage N` era remains, which still signals "pipeline-built" without the session-diary noise.

---

### (d) Hybrid: squash old prehistory, keep recent history intact

Squash everything before `v0.1.0` into a handful of milestone commits; keep v0.1.0 onward intact. Preserves the observable development record post-v0.1 while collapsing the scaffolded build pipeline into something like "build: initial v0.1.0 via dispatcher pipeline (82 commits)".

**What you get:** 82 pre-v0.1 commits collapse to ~5–10 named milestones. Post-v0.1 history (177 commits) is preserved verbatim.

**What gets lost:**
- The individual `stage N` / `act-XXXX:` pre-v0.1 implementation commits. The `Act-Id:` correlations for the 40 v0.1 issues would need to be reconstructed in squash messages if `act doctor` coverage over the pre-v0.1 era matters.
- Bisect granularity within the pre-v0.1 build.

**Reversibility:** Partial. Tag `pre-v0.1-verbatim` before squash to preserve.

**Effort:** Medium. Squash boundaries require judgment; the pre-v0.1 `act-XXXX:` commit-marker trailer mapping to closed issues needs to be confirmed as non-critical before dropping.

**Read-cold experience:** Good. The log opens with a handful of "this is what v0.1.0 consisted of" commits, then transitions to a conventional commit log for v0.1.0 onward.

---

## 3. Cross-tool consistency note

The four tools have different history characters:

- **act**: 259 commits, 4 weeks, includes the full pre-v0.1 pipeline + dogfood history. The richest and noisiest.
- **ask**: Similar Go-binary profile. Skewing toward cleaner per-commit discipline (likely similar handoff docs noise).
- **poke**, **reach**: Skill-only packages. Simpler histories; the scaffolding problem likely doesn't apply.

**Recommended cross-tool position:** Apply option (a) — leave as-is — uniformly across all four tools at release. Here's why:

1. The "built in the open via agents" character is a *feature*, not a bug. The commit log is the most transparent possible record of agent-driven development. Cleaning it up before public exposure would erase the most differentiated thing about this project's creation story.
2. The tools are pre-v1 and have no external users. No URLs break; no integrations depend on commit stability.
3. Each tool should own its own history decision — but a blanket "clean everything before we release" mandate imposes cleanup cost on tools (poke, reach) where the history may already be clean.

If any individual tool's log contains something genuinely embarrassing (credentials accidentally committed, a profanity-heavy commit message, etc.), address that tool specifically. A uniform cleanup mandate isn't warranted.

---

## 4. Recommendation: leave as-is (option a)

**Rationale.** The host log is already clean — Phase 1's nested `.act/.git` migration moved all op-commits out. What remains is implementation history plus agent bookkeeping (session-handoff docs, fmt fixups). The session-handoff commits are identifiable as such and don't mislead a cold reader; they're the agent's session diary, and that diary is part of what makes this project's development story interesting. The pre-v0.1 `stage N` scaffolding commits tell a clear story: "this tool was built using a dispatcher pipeline." That's not embarrassing — it's the point.

The one credible argument for option (c) (filter-repo cleanup) is that 22 `docs: refresh session-handoff.md` commits pad the log with zero-content noise. They're not confusing but they're not interesting either. However, the effort of rewriting history (coordinating with any in-flight branches, invalidating any tooling that cross-references commits) is not justified for a pre-release repo with a single user.

**Specific commit ranges if rewriting were chosen.** The session-handoff noise is confined to commits with subjects matching `docs: refresh session-handoff`, `docs: session handoff for`, `docs: handoff `, and `docs: refresh handoff`. The `git filter-repo` invocation in option (c) above is the concrete recipe. The `pre-filter-backup` tag preserves full recovery. But this is not the recommended path.

**Recommendation for sibling tools: apply same leave-as-is stance.** See section 3.

---

## 5. Cross-reference: agent-tools-release.md updated

The chosen approach (leave as-is) has been recorded in `~/Workspace/knowledge/projects/agent-tools-release/agent-tools-release.md` under a new "Commit history" section.
