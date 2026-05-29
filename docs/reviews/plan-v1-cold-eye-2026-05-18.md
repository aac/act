# Cold-eye review — Phase 2 implementation plan v1

**Reviewer perspective.** Read plan v1 (commit `d07b4f5`) and brief v4 (commit `a7f1bd1`) cold. No prior session context. Assessing whether the plan stands on its own for an agent picking up tickets without phoning home.

**Findings filtered at >70% confidence.** Each cites the plan section, what's wrong/missing, and the smallest fix.

---

## 1. Ticket-by-ticket: can a cold agent execute?

### Ticket 1 (foundation)
**Mostly yes.** Concrete, grep-able acceptance criteria. Two stumbles:
- The post-receive hook is created as a no-op skeleton here and rewritten by ticket 6. Ticket 1 should pin the exact path, mode (`0755`), and skeleton content so ticket 6 can pattern-match safely.
- `upstream_public` detection mechanism is unnamed. URL heuristic? `git ls-remote` anon? HTTPS GET? Fix: pick one (e.g., anonymous HTTPS GET; 200 = public, 401/404 = private; anything else → treat-as-private with stderr warning).

### Ticket 2 (push-contention retry helper)
**Yes, mostly.** Loop matches the brief. Two gaps:
- "Exponential backoff capped at 1s" lacks a base. Name one (e.g., 50ms doubling).
- The shallow-clone failure test specifies `git daemon`. Ticket 11 inherits the choice. Fix: settle on one fixture-remote shape (`git daemon` vs SSH-loopback vs bare-repo-on-filesystem) here and have later tickets reuse via a shared helper.

### Ticket 3 (push-on-write integration)
**Yes, but the largest blast radius.** Plan correctly flags "do not parallelize." Open seams:
- `.act/.pending-pushes` schema is undefined. Pin it (e.g., JSONL with `{op_id, committed_at, branch}`).
- Slow-write warning string isn't named. CLAUDE.md's discipline rule requires a `TestDocClaim_*` that greps for *something*. Pin the template (e.g., `act: slow write (NNNms) — see .act/.slow-writes`).
- On `push_exhausted` after the work commit, the close op is *already* a real commit (per CLAUDE.md's close-stages-into-commit flow). Local says closed, remote doesn't know. Plan's "harvest if needed" almost-covers this. Fix: state explicitly that the next reachable write retries via `.act/.pending-pushes`; harvest is only last-resort.

### Ticket 4 (error envelope additions)
**Not a real ticket — bundled.** Drop the slot or rename to "constraint reminder." The dep graph references 1–11 but ticket 4 has no edges; a cold orchestrator might dispatch nothing.

### Ticket 5 (read TTL cache)
**Yes.** File list disjoint from ticket 3 (but see finding 3.4). Two worries:
- `--fresh` / `--no-cache` — pick one canonical, alias the other. Each flag needs its own `TestDocClaim_*` per the discipline rule.
- Plan deletes `.act/fold-checkpoint.json` and `.act/index.db` on invalidation. Brief says "treat as stale," which is cheaper than delete-and-rebuild. Fix: align with the brief — mark stale via tree-hash field, don't delete.

### Ticket 6 (`act remote sync` + triggers)
**Mostly yes, but orchestrator-detection is open.** Plan's own open question #4 names the cleaner answer (`act.role=orchestrator` config key set by ticket 1) and then doesn't commit. The proposed heuristic ("origin is `.` or path under same filesystem root") breaks under worktrees, symlinks, bind-mounts. Fix: commit to the config key in ticket 1's scope; ticket 6 reads it. Also: if detection is uncertain at runtime, default to "sync anyway" — the dangerous case is the orchestrator silently skipping sync.

### Ticket 7 (`act bootstrap-worker --from-remote`)
**Yes.** Atomic-rename, timeout, error codes all named. Missing context: cold agent doesn't know whether `--import-from` exists already or whether this ticket builds both modes. Fix: state explicitly "Phase 1.5 ships `--import-from`; ticket 7 adds `--from-remote` alongside."

### Ticket 8 (harvest narrowing)
**Yes.** Three test cases, crisp behavior.

### Ticket 9 (doctor extensions)
**Yes.** Each case has detection mechanism inherited from the brief.

### Ticket 10 (orchestrate doc updates)
**Yes, but path trap.** "Files touched" includes `~/.claude/commands/orchestrate.md`, a symlink to Andrew's `claude-config` repo per global CLAUDE.md. A cold agent on a worktree dispatch commits the change "successfully" but it never reaches the act repo or the claude-config remote. Fix: name the symlink target and require the cross-repo commit-and-push.

### Ticket 11 (E2E integration)
**Yes, but risks becoming the dumping ground.** Each test needs fixture infrastructure earlier tickets touched in passing. Fix: require tickets 2/6/7 to land fixtures in a shared package (e.g., `internal/testfixtures/remote.go`), not test-private.

---

## 2. Over- vs. under-specified

**Under-specified (smuggled "figure it out later"):**

- Ticket 3: `.act/.pending-pushes` format, slow-write warning string.
- Ticket 1: `upstream_public` detection mechanism.
- Ticket 6: orchestrator-detection mechanism (plan calls this out but doesn't decide).
- Open question #1 (slow-write log format): plan says "JSON-lines with `{timestamp, op_id, duration_ms, op_type}` is a reasonable default but worth confirming." This is a schema — settle it now or in ticket 3's scope, not after start.

**Over-specified (plan doing implementation's job):**

- Ticket 2: the five-step push-retry loop is essentially pseudocode pulled from the brief. That's fine — the brief is the authoritative source — but the plan duplicates it verbatim rather than referencing. Risk: if the brief changes, the plan and the brief disagree. Fix: have ticket 2 link to brief section "Push contention" and only call out plan-stage details (backoff base, fixture choice).
- Ticket 5: enumerating the file paths to touch is helpful, but the invariant "post-rebase, invalidate checkpoint and index.db" is repeated almost verbatim from the brief. Same risk.

The plan is roughly right-sized overall — the over-specification is a duplicated-source problem, not a feature-creep problem.

---

## 3. Implicit dependencies the deps graph misses

1. **Ticket 1 ↔ Ticket 6 share `.act/.git/hooks/post-receive`.** Ticket 1 creates a no-op skeleton; ticket 6 rewrites with real content. Graph edge missing.
2. **Ticket 1 ↔ Ticket 6 share the `act.role` config key** (if open question #4 is resolved by adding the key). The plan's "Open question #4 may change ticket 1's scope" should be a hard pre-ticket-1 decision.
3. **Tickets 2, 6, 7, 11 share fixture-remote infrastructure** (bare repo + `git daemon` or equivalent). Plan flags this in risks but doesn't make ticket 2 owner of the shared fixture. Without that, every later ticket reinvents.
4. **Tickets 3 and 5 both touch `internal/gitops/gitops.go`** via the slow-write measurement (ticket 3) and the read-cache fetch path (ticket 5). The plan says they can run in parallel after ticket 2. They can't — both edit the same file. Fix: state explicitly that ticket 3 lands first, ticket 5 rebases.
5. **Ticket 10 depends on `skills/act/SKILL.md` being canonical for worker-protocol claims.** Plan doesn't say which doc is authoritative if the orchestrate doc and SKILL.md disagree. Fix: name SKILL.md as canonical and orchestrate.md as a thin pointer.
6. **Ticket 9's `.act/.slow-writes` summary depends on ticket 3's file format.** The graph shows 9 after 6 and 8; it should also be after 3 (it is, transitively, via 6, but the read happens against ticket 3's file format). Cold agent on ticket 9 needs the format pinned.

---

## 4. "Open implementation questions" that should be in the plan

1. **Open #1 (slow-write log format).** Schema affects ticket 3 (writer) and ticket 9 (reader). Pin it now.
2. **Open #2 (`.act/.sync-log` retention).** "Last 100 or last 7 days" — pick one. Otherwise ticket 6 implements an arbitrary cap and ticket 9 may surprise on overflow.
3. **Open #4 (orchestrator detection).** As discussed in finding 1. The plan even names the better answer (config key) and then leaves it open. Decide.

Open #3 (worker telemetry) is correctly punted — the brief itself defers it. Fine to leave.

---

## 5. Surprises

**Positive:**

- The plan's "Risks and rough-edges" section names the largest serialization point (ticket 3) and Windows untestability honestly. Calibrated risk disclosure.
- Documentation discipline is woven through every ticket's "Files touched" with explicit `TestDocClaim_*` callouts. The plan absorbed the CLAUDE.md rule rather than treating it as overhead.
- Ticket 4's bundled-into-others framing is correct — error-envelope work doesn't deserve a standalone ticket. The plan resists the urge to invent ceremony.

**Negative:**

- Open question #4 (orchestrator detection) reaches the plan stage with the author *recommending* the cleaner answer ("consider switching to it in ticket 6 implementation") but not committing. That's exactly the kind of decision the plan-review gate exists to surface. Decide it.
- Ticket 10 edits a symlink to a different repo without flagging the cross-repo commit-and-push requirement. A cold agent on a worktree-isolated dispatch will get this wrong.
- The wall-clock estimate ("5-10 working days of orchestrate time") is plausible but assumes ticket 3 doesn't blow up. Ticket 3 touches seven files and adds an `--offline` flag with persistent state. If any of those files have downstream tests that break in subtle ways, ticket 3 could swallow a third of the budget alone. Plan should call this out.

---

## 6. Summaries

**What this plan says it will deliver.**
Eleven tickets across four phases that turn the v4 design brief into shipped code: foundation subcommands (`act remote enable/disable/add-upstream` with new config keys), a push-retry helper with reachability verification, push-on-write integration across every act write path plus an `--offline` flag, a read-side TTL cache with dispatch-mode bypass, `act remote sync` with two triggers (post-receive hook and orchestrator self-write), `act bootstrap-worker --from-remote` for clone-based worker bootstrap, harvest narrowed to fallback role, doctor's three-state reconciliation cases, `/orchestrate` doc updates, and a comprehensive E2E suite. ~3500-5000 LoC, 5–10 days of orchestrate time.

**What I'd warn the implementer about before they start.**
Five real traps. (1) Ticket 3 touches seven write-path files and is the largest serialization point — block all other write-path work while it lands. (2) Tickets 3 and 5 both edit `internal/gitops/gitops.go`; the graph says they can run in parallel after ticket 2 but they cannot. (3) Ticket 10 writes through a symlink into a different repo; do the cross-repo commit-and-push, don't assume worktree isolation covers it. (4) Resolve open question #4 (orchestrator detection) before ticket 1 starts, because the cleaner answer (a config key) belongs in ticket 1's scope, not ticket 6's. (5) Make ticket 2 the owner of the shared fixture-remote helper so tickets 6/7/11 reuse rather than reinvent.

---

**Verdict:** `needs-iteration`.

**Smallest set of changes to get to `plan-ready`:**

1. Commit to the `act.role` config-key approach for orchestrator detection; move setup into ticket 1, reads into ticket 6. Remove open question #4.
2. Pin the slow-write log schema and the `.act/.sync-log` retention policy in the plan body (resolves open questions #1 and #2).
3. Add a dependency edge between ticket 3 and ticket 5 (or state explicitly that ticket 5 rebases on ticket 3 because both touch `internal/gitops/gitops.go`).
4. Add a note to ticket 10 about the `~/.claude/commands/orchestrate.md` symlink and the cross-repo commit-and-push requirement.
5. Name a shared fixture-remote package (e.g., `internal/testfixtures/remote.go`), owned by ticket 2, reused by 6/7/11.

None of these change the plan's structure — they're scope tightenings and one dep-graph correction. After the changes the plan reads as `plan-ready`.
