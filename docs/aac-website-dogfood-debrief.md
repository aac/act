# aac-website dogfood debrief

20 iterations, 23 closes (incl. 3 dupes + 1 obsolete). Below is the meta — not the work log.

## 1. Where the skill creaked

- **Worktree isolation doesn't extend to `act` ops.** act-8808's subagent ran `act create --push` three times; those commits landed on `origin/main`, not the worktree branch. The skill says "push to your worktree branch" but `act create --push` ignores that — it pushes the op's auto-commit against whatever the worktree's tracking config says, which was main. Net: I had to cherry-pick the work commit *separately* from the create-op commits because they'd already merged into main. Either `act --push` needs to respect current branch, or the skill needs to flag this trap when briefing worktree subagents.
- **Close-reason 500-byte cap.** Hit it on 5 of ~12 close commands. The cap is in `act help close` but I never internalized it until each rejection. Friction every time. Auto-truncate with a warning, or surface the limit when the close --reason is being read.
- **Auto-commit-per-op cadence.** ~60 `act-op:` micro-commits in the git log for one drain. Functional but ugly; mixing them with work commits makes `git log --oneline` hard to scan. The skill says "let the auto-commit per `act` op stand on its own" — that's the right default, but the result is noisy at scale.
- **Rule I bent:** the skill says "push to origin (step 7) — part of the loop, not a halt." The harness disagreed on the first ff-merge into main. I had to halt and ask Andrew to grant permission. The skill's auto-mode caveat covers `git push origin main` but not the `git merge --ff-only origin/worktree-*` step that precedes it. Both need the carve-out, and the skill should say so explicitly.

## 2. Docs-triage (act-8808) — does the loop generalize?

Yes — but with one wrinkle. Worktree isolation made sense (clean `git mv`s, no conflicts). The (act-8808) marker on the work commit still served its purpose: `act doctor` can correlate by message even though no code shipped. Review was correctly skipped. The wrinkle: the subagent was told to file 3 follow-ups from `to-do-list.md` and didn't grep the backlog first — duplicates. **The skill should call out: "if an issue asks the agent to file new issues from any external source, require backlog-check FIRST."** This isn't code-specific; it's an orchestration discipline that applies anywhere agents create issues. The non-code shape of act-8808 exposed it because that's the kind of issue most likely to spawn many derivative issues.

## 3. Review subagents — signal vs ceremony

- **Real save:** act-c500's reviewer caught that the PROD guards became load-bearing security with nothing enforcing their presence. That was a regression-class hole; the validator I added in response is the value. Worth the opus cost.
- **Mostly noise:** act-a9d0's review couldn't read the worktree blobs and speculated — concerns at 80%+ confidence that were already handled by the actual code (Dropdown ID placement, async/await preservation, bubbles:true). The reviewer's confidence calibration was off because they were reasoning from absence of evidence.
- **>70% confidence floor:** correct number empirically. Below that, signal-to-noise collapses. But confidence ≠ accuracy when the reviewer can't see the code (a9d0). The skill should add: "if the reviewer can't actually read the diff, their confidence numbers are unreliable — verify or re-spawn with explicit file paths."
- **Opus for infra:** justified for c500 (genuinely security-shaped). Would've been overkill for a9d0/bcd9 (UI mechanics, dev-only). Heuristic: opus when the change affects production safety boundaries; sonnet for everything else, even if the LOC is large.

## 4. Near-misses beyond the 3 saved memories

- **Edge cache lag != component broken.** act-4141's PostLinkCard didn't appear on aac.social when I Playwright-checked it; almost concluded the component was broken. Saved by checking local `dist/` — it rendered fine, Cloudflare edge just hadn't refreshed. Worth a memory: **always verify against local `dist/` before trusting Playwright on the live CDN.**
- **Stale "known fix" branches.** act-9af8's `claude/fix-book-reviews-display-Lw97O` branch predated the validator that codifies *why* mask-image is wrong. I almost merged it. The `validate-build.mjs as institutional history` memory captures the pattern but the near-miss specifically was: trusting "Andrew said it's done" without re-verifying against current main.

## 5. Wrong / stale in the skill

- The skill says "Pushing same-branch commits to origin is part of the loop, not an obligation to halt on." In Claude Code auto-mode that statement is technically true but operationally misleading — the merge that precedes the push *is* blocked. The skill needs to either: (a) update the auto-mode caveat to cover the merge, or (b) acknowledge that the integrator step trips a separate classifier.

## 6. One change to the canonical loop

**Decouple op-writing from current working tree.** Make `act update --claim`, `act close`, and `act create` accept an optional `--branch <ref>` so the op file is committed against the named branch, not whatever HEAD happens to be. This would fix the worktree-subagent-pushes-to-main bug (subagent passes `--branch worktree-XXX`) and make integrator workflows much cleaner. Right now the loop's correctness depends on CWD discipline, which is fragile.

## 7. CLI DX friction

- **`act show` truncates** with "see --json for full text" — but I never wanted JSON, just longer text. Add `--full` or just stop truncating.
- **Compose target:** I ran `act close <id> --reason "..."` then `git commit -m "close <id> (act-XXXX)"` 15+ times. Folding the commit into the close (with a `--commit-message` flag or implicit standalone commit) would be a clear win.
- **`act ready` columns:** missing `assignee` / `claimed_at`. For single-user me, OK; for multi-agent setups, near-essential.
- **No `act log` filtering:** `--since`, `--by-issue`, or `--type close` would all be useful for retrospectives like this one. Right now the op log is a haystack.
- **No `act doctor` run as part of close.** If commit-marker correlation is a discipline, why isn't `act doctor` invoked automatically on close to verify the marker actually appears in the linked work commit? Would have caught my one bare-id-slicing error if I'd ever made it.
