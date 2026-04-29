# Adversarial Review of `act` Brief (v1)

Reviewer stance: assume the brief is wrong until each load-bearing decision survives a concrete failure scenario. Numbered challenges follow; each is paired with why it matters and a concrete suggested resolution.

---

1. **Op-log creates a per-issue file-count explosion that git is bad at.** A long-lived epic with 200 ops produces 200 tiny JSON files in one directory; multiply by hundreds of issues and you get tens of thousands of objects in `.act/ops/**`.
   - **Why it matters:** git status, git gc, and `git log -- .act/ops/` all degrade superlinearly with file count in a single tree, and most filesystems (APFS, ext4 dir hashing aside) get cranky at 10k entries per directory. Compaction is described as "periodic" but is single-writer and manual via `act compact`, so in practice it won't run often enough.
   - **Suggested resolution:** (a) shard ops into `ops/<id>/<yyyy-mm>/...` to bound per-directory entries; (b) make compaction automatic on a threshold (e.g. >50 ops or >30 days since last snapshot), invoked opportunistically by any writer that holds the lock; (c) measure: budget <2k total files in `.act/` for a 1-year-old project.

2. **Op-log fold cost is unbounded and hidden behind "rebuild SQLite on demand".** Every cold start of an agent session that needs to query state must replay every op in the repo before SQLite is usable.
   - **Why it matters:** "rebuild on demand" is fine at 100 ops, painful at 10k, fatal at 100k. Agents that shell out to `act ready` per turn will pay this cost repeatedly if the cache is invalidated by any pull.
   - **Suggested resolution:** persist a content-addressed fold checkpoint keyed by the git tree-hash of `.act/ops/`; on startup, if tree-hash matches checkpoint, reuse the SQLite cache as-is; otherwise fold only ops whose paths changed since checkpoint. Document worst-case fold latency as a perf budget.

3. **Op-schema evolution has no migration story.** Ops are append-only forever. Adding a new field, renaming `closed_reason`, or changing how `deps` are encoded breaks every old binary trying to fold the log.
   - **Why it matters:** the brief explicitly chose append-only as a virtue, but that virtue is also the trap: you cannot rewrite history without breaking determinism for everyone who has the repo. Old ops in the wild will outlive any single binary version.
   - **Suggested resolution:** every op file carries an explicit `op_version` and `schema_version`; the fold function dispatches per-version with explicit upgrade rules; ship a `act migrate` that writes *new* ops representing the upgrade rather than rewriting old ones. Lock op schema as a stability surface alongside the CLI.

4. **Last-write-wins by wall-clock timestamp is unsafe across machines with skewed clocks.** Two laptops with clocks 90 seconds apart will produce non-causal ordering, and no NTP guarantee exists for the target environments (Cowork containers, CC-on-the-Web sandboxes).
   - **Why it matters:** the brief's determinism claim ("any agent can derive identical state") survives clock skew, but the *correctness* claim ("the right write won") does not. An agent on a fast clock will permanently dominate slower agents.
   - **Suggested resolution:** combine ISO timestamp with a Lamport or hybrid-logical clock (HLC) carried in the op payload; tiebreak by op-hash as already specified. Order ops by `(hlc, op_hash)`, not `(wall_time, op_hash)`. Document the bound (HLC drift cap) and refuse ops with implausible clock deltas.

5. **The "thought-I-claimed-but-didn't" failure mode is acknowledged but not engineered.** The brief says the loser "must `act show` to detect they didn't win." Agents won't reliably do that.
   - **Why it matters:** silent claim-loss leads to two agents both believing they own a task, doing duplicate work, and racing on PRs. This is the single highest-stakes failure of the multi-writer story.
   - **Suggested resolution:** make `act update --claim` blocking and verifying: write op, immediately `git pull --rebase`, re-fold the issue, and exit non-zero with a structured `{"claimed": false, "winner": "..."}` payload if the local op didn't win. Agents check the exit code. Optionally a `--wait` flag that polls for stability.

6. **Atomic claim re-check protocol is under-specified.** When does the re-check happen — before commit, after commit, after push, after pull from origin? Each choice has different failure surfaces.
   - **Why it matters:** if re-check is local-only, two agents on different machines never see each other until push. If post-push, you've already committed a losing op to history. The brief doesn't pick.
   - **Suggested resolution:** specify the protocol as: (1) write op locally, (2) `git pull --rebase` from the configured remote, (3) fold, (4) report win/loss, (5) only push on win unless `--push-always`. Make remote-awareness an explicit flag (`--isolated` for offline use).

7. **Auto-commit-on-write vs leave-to-agent should not be a coin flip; pick auto-commit with a squashable marker.** The brief lists this as an open question.
   - **Why it matters:** if the agent forgets to commit, the op only exists locally and the multi-writer story collapses. If `act` auto-commits, history bloats with one commit per op.
   - **Suggested resolution:** auto-commit by default with commit messages prefixed `act-op: <id> <op-type>`; provide `act squash` (or fold into `act compact`) that collapses a contiguous run of `act-op:` commits into one before push. Add `--no-commit` for agents that batch. Default wins because durability beats history aesthetics.

8. **`act doctor`'s `git log | grep '(act-XXXX)'` is fragile against squash, rebase, and force-push.** Squash-merge collapses commit messages; rebase rewrites them; force-push erases them.
   - **Why it matters:** "orphan close" detection is the only mechanism keeping tracker state consistent with code reality. If it false-negatives after a squash-merge, closed work looks open forever.
   - **Suggested resolution:** scan both commit messages *and* the diff of `.act/ops/**` for close-ops referenced by ID; treat any commit touching the issue's ops directory as evidence of activity. Optionally store a `closed_by_commit` reverse index in snapshots so doctor can verify symmetrically.

9. **4-hex-prefix IDs collide at a few hundred issues.** Birthday-bound: ~50% collision probability around √65536 ≈ 256 issues.
   - **Why it matters:** the brief uses `act-a1b2` as the canonical example without specifying disambiguation. A solo project could plausibly hit 256 issues; collisions across forks are inevitable.
   - **Suggested resolution:** store a full hash internally; display the shortest unique prefix per session (git-style). Define an extension protocol: when prefix collides, all displays for those IDs lengthen to 5 hex; document the rule. Reject creates that would tie an existing prefix when only the prefix has been quoted in commits.

10. **Title rename behavior is undefined.** ID is a hash — of what? If of the title, rename means new ID. If of creation time, title is just a field.
    - **Why it matters:** beads-style hashes-of-title would couple identity to mutable state, breaking every existing reference on rename.
    - **Suggested resolution:** ID is hash of `(create-op payload, including a random nonce)`, never the title. Title is a mutable field and renames are ordinary update ops. State this explicitly.

11. **Command surface is missing `act search` and `act log/history`.** With op-log as the primitive, history is *cheap*; not exposing it is leaving the killer feature on the table. Search is non-negotiable past 50 issues.
    - **Why it matters:** agents do `act list --json | jq` today, but that scales badly and re-implements search per-agent. No history command means no way to ask "who claimed this and when" without reading raw op files.
    - **Suggested resolution:** add `act search <query> [--in title|desc|all]` (FTS5 in the SQLite index) and `act log <id>` (renders the op stream). Drop `act dep rm` from v1 if the count is the constraint — dependency removal is rare and can be `act update --dep-rm`.

12. **`act compact` as a user-visible command is probably vestigial.** Compaction should be a side-effect of normal writes once a threshold is hit, not a thing humans/agents invoke.
    - **Why it matters:** every agent has to remember when to compact, and they won't. A surfaced command implies a workflow that doesn't exist.
    - **Suggested resolution:** make compaction automatic and remove the command from the v1 surface, freeing a slot for `act search` or `act log`. Keep `--compact` as a flag on `act doctor` for forced runs.

13. **MCP 1:1 with CLI is the wrong default for agent UX.** Agents pay per round-trip; the most common workflow (find ready, claim it, return context) takes three calls.
    - **Why it matters:** every extra MCP call is latency the agent and the user pay. A composed `act_next` (ready + claim + show in one call, with the claim-recheck above) is strictly better for the primary user.
    - **Suggested resolution:** keep 1:1 tools but add composed convenience tools: `act_next` (ready+claim+show), `act_finish` (close+commit-marker), `act_block` (status=blocked+add-dep). Mark composed tools as the recommended path in tool descriptions.

14. **Bun/TS vs Go is real and the brief dismisses it too fast.** Andrew's stack is TS/Python; the agents writing this code are stronger in TS than Go; Bun ships single static binaries now.
    - **Why it matters:** "Go's discipline" is a vibe, not a measured benefit. Concretely: faster iteration in TS, better SQLite ergonomics (bun:sqlite), and the MCP TS SDK is the reference impl. Go's wins are cross-compilation maturity and fewer runtime surprises.
    - **Suggested resolution:** stay with Go *only if* the cross-compile matrix (5 targets) and op-fold determinism are easier to lock down there. Otherwise default to Bun + TS and revisit if static-binary distribution bites. Decision should be made on a 1-day spike, not asserted.

15. **Distribution via brew + curl + GH Releases ignores op-schema version skew.** A user on macOS with `brew` and a Cowork container with a pinned binary will diverge.
    - **Why it matters:** if the Cowork container ships v0.3 and the laptop has v0.5, ops written by v0.5 may be unreadable by v0.3, and the shared repo silently breaks.
    - **Suggested resolution:** every op carries `writer_version`; on read, if any op's writer_version is newer than the reader, exit with a clear "upgrade required" error and a one-line install command. Pin the Cowork plugin manifest to a specific binary version. Add `act version --check-repo` to verify compatibility against `.act/`.

16. **Deferral of LLM compaction is correct; deferral of hooks is questionable.** No event-driven hooks means external automation (CI signaling close, PR merge auto-closing) must poll.
    - **Why it matters:** the most common integration pattern (PR merges → close issue) is where humans add value to the tracker. Without hooks, every team builds the same shim.
    - **Suggested resolution:** ship a minimal hook surface in v1: `.act/hooks/{post-create,post-close,post-claim}` as plain executables. Defer pubsub but not file hooks; the cost is ~1 day of work and removes the most common request.

17. **Deferral of search is not in the deferred list but search isn't in v1 either; that's a gap.** Either it's deferred (state it) or it's in v1 (build it).
    - **Why it matters:** brief consistency. Reviewers can't evaluate the deferral if it's invisible.
    - **Suggested resolution:** explicitly mark `search` and `log` in the deferred list with rationale, or pull them into v1 (preferred — see challenge 11).

18. **The bootstrap is incoherent on its face.** `act` is supposed to track its own build, but `act` doesn't exist yet, so build issues live in `_generated/projects/act/` as markdown.
    - **Why it matters:** if markdown-as-tracker is good enough to bootstrap, it's evidence `act` is over-engineered. If it's not good enough, the bootstrap will be painful and you'll learn the wrong lessons because you're not dogfooding.
    - **Suggested resolution:** bootstrap with a 50-line shell script that reads/writes a single `_generated/projects/act/issues.jsonl` file — the simplest possible op-log. The first thing v0.1 of `act` does on boot is import that JSONL. This forces the import path to exist and dogfoods the op-log primitive from line one.

19. **Beads-anchoring shows in the data model and command surface.** Hash IDs, ready queue, atomic claim, JSON-everywhere — these are Beads' DNA. A fresh-eye design might look very different.
    - **Why it matters:** "minimal Beads" is a defensible scope, but the brief is not honest about what it would look like to design without Beads in the room. Specifically: do we even need a "type" field? An "epic"? Priority 0–3 or just a boolean? The Beads taxonomy was earned for human PMs.
    - **Suggested resolution:** spec a parallel "v0 fresh-eye" with: only `(id, title, body, status, deps, claim)`; no type, no priority, no parent (deps subsume parent). Compare against the current brief on a concrete 3-task workflow. If fresh-eye wins, adopt it; if not, justify each retained Beads field in the spec.

20. **Testing strategy is one concurrent-write integration test.** That's not a strategy, it's a smoke test.
    - **Why it matters:** op-fold determinism, schema evolution, clock-skew handling, claim recheck, doctor accuracy, MCP transport, git rebase under contention — none of these are covered by "two parallel processes write to the same issue."
    - **Suggested resolution:** add (a) property tests on op-fold (any permutation of an op set yields the same final state), (b) golden tests on op schema (each op type has fixture inputs and expected folded state), (c) a fuzzer that produces random op sequences and asserts fold determinism, (d) an end-to-end MCP test driving via a fake stdio client, (e) git contention tests that rebase under concurrent writes.

21. **"Definition of done" is not end-to-end testable without a human.** "Installs and runs end-to-end in fresh CC laptop, CC on the Web, and Cowork environments" requires a human to install in three places.
    - **Why it matters:** the build pipeline is supposed to be agent-driven. A DoD that only a human can sign off on is a bottleneck.
    - **Suggested resolution:** ship three CI jobs that mimic each environment in a container; an agent runs them and reports PASS/FAIL. Human only signs off on the final tag, not on each install verification.

22. **Reproducible state is claimed but not defined.** "Any agent that pulls the repo can derive identical tracker state" — identical how? Same SQLite bytes? Same query results?
    - **Why it matters:** SQLite file bytes will differ across versions and rebuild orders. Query results should match, but only for a defined query set.
    - **Suggested resolution:** define reproducibility as: for any sequence of supported queries, two folders of the same git tree return byte-identical JSON output. Test this in CI.

23. **No story for `.act/` size growth.** Snapshots + ops + (eventually) closed-issue ops accumulate forever. A 5-year-old repo could carry 100MB of tracker state.
    - **Why it matters:** users will balk at cloning a 500MB repo because half of it is tracker history. The "git push/pull is the network protocol" thesis depends on `.act/` staying small.
    - **Suggested resolution:** define a retention policy: closed-issue op directories collapse to a single terminal snapshot after N days; deleted issues retain only a tombstone. Document expected size growth (e.g. ~1KB per closed issue at steady state).

24. **The doctor's job is too narrow.** It only checks orphan closes; it should also catch orphan ops (op for an issue that has no create), broken dep edges (dep references nonexistent issue), and time-travel ops (timestamp before the create).
    - **Why it matters:** op-log integrity is the load-bearing invariant; doctor is the only tool defending it. One check covers a sliver of the failure surface.
    - **Suggested resolution:** spec doctor as a battery of integrity checks with a `--check <name>` filter; orphan-close is one check; ship at least: `orphan-ops`, `dangling-deps`, `time-travel`, `cycle` (in deps), `unknown-op-version`, `index-divergence`.

25. **No defined behavior for deletion.** What happens if a user wants to remove an issue created in error? Op-log is append-only.
    - **Why it matters:** real users will create test issues, typo titles, paste secrets into descriptions. "Append-only forever" plus "everything in git" means a secret leak is permanent unless you rewrite history.
    - **Suggested resolution:** define a `redact` op that overwrites prior op contents *in the snapshot only* and retains a tombstone. For true secret removal, document the git-filter-repo escape hatch as expected. State the policy explicitly.
