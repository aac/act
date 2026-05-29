# act + Orchestration: Planning Notes

Status: planning, pre-implementation
Date: 2026-05-10
Inputs: Anthropic Managed Agents article; a private session-orchestrator design doc; `act/docs/dispatcher-prompt.md`; `act/docs/act-evaluation.md`; `act/CLAUDE.md`.

This note exists to keep us from prematurely codifying integration patterns between act and an orchestrator before either the orchestrator is built or the worker contract is generalized for non-code work. It maps the design space, names what's owned by which layer, and proposes what's safe to do now vs. what should wait.

## Frame: managed-agents applied to act

The Anthropic managed-agents article describes three decoupled components:

- **Session** — durable event log of work
- **Harness** — stateless orchestration loop that drives Claude inference and tool calls
- **Sandbox** — execution environment (container, worktree, etc.)

Applied to act-shaped work:

| Component | What it is, in our world |
|---|---|
| Session | The act op log (`.act/`) — durable, append-only, multi-writer safe |
| Harness | Either *act itself running in a Claude session* (Mode A) or *a separate orchestrator process* (Mode B) |
| Sandbox | Worktree, container, credentials — pre-existing concern, governed by orchestrator in Mode B, by the agent's choices in Mode A |

The article's key lesson — **virtualize the layers so each can change independently** — is the rule we should hold to. act should not bake in assumptions about the harness or sandbox.

## The Two Modes

**Mode A — act is the harness.** One Claude session runs the canonical loop end to end: `act ready` → claim → work → close → push, repeating until ready is empty. Sub-agents inside that session (worktree-isolated, per the skill) are still part of Mode A because the *parent* agent is the orchestrator. This is what `/act:loop` should drive.

**Mode B — act is the session only.** An external orchestrator decides which issues are worked when, by whom, in what environment. Each worker invocation handles one issue and exits. The orchestrator owns lifecycle, parallelism, retry, cross-repo coordination, and environment provisioning. This is what `/act:work <id>` should drive.

"Orchestrator" here is a role, not a specific implementation — see *Orchestrator implementations* below. Mode B doesn't depend on the session-orchestrator project specifically, or on any other named tool; it depends on something fulfilling the contract.

Mode A doesn't disappear when Mode B exists — they coexist. A solo developer running act in one terminal is Mode A. A Plugin Library business-process workflow with 6 parallel agents managed by a separate orchestrator is Mode B. Both must be first-class.

## What belongs where

The cleanest division — and the one that keeps act honest as a primitive — is three layers:

**act owns:**
- The work unit (issue with acceptance criteria)
- Atomic claim semantics
- Op log durability and multi-writer concurrency (HLC, etc.)
- Identity (node_id), assignment, status state machine
- Pre-close hooks (`.act/hooks/close`) as a project-defined gate
- The canonical loop's *order* (claim → work → close → push), independent of *how* the work happens

**Orchestrator (Mode B) owns:**
- Session lifecycle: spawn, monitor, kill, resume
- Environment provisioning: worktrees, containers, dependency installs, credentials
- Parallelism budget and admission control
- Dispatch policy: which issue → which worker, when
- Retry and timeout policy
- Cross-repo coordination
- Halt-handling: what to do when a worker surfaces a halt condition
- Reporting and human-facing UI

**Project CLAUDE.md owns:**
- *How* work happens within a session: testing discipline (TDD or not), lint/format gates, review process, language conventions
- Project-specific definitions of "done" beyond the acceptance criteria
- Codebase navigation, hot paths, conventions

The crucial implication: **`/act:work` and `/act:loop` should specify the act contract and nothing about how to do the work.** They should defer to project CLAUDE.md for everything else. This is the correction Andrew flagged — the current draft of `/act:work` says "Implement. Write tests..." which encodes both code-shape and TDD-shape into a layer where neither belongs.

## Orchestrator implementations

"Orchestrator" is a role defined by the contract above; anything that owns assignment, drives the per-issue worker invocations, and handles integration push-to-main counts. The role is implementation-neutral by design, and act stays a primitive that any harness can compose with.

Three implementations worth naming, by maturity:

- **Claude Code with sub-agents (the Mode B reference implementation, available today with documented workarounds).** A parent Claude Code session is the orchestrator: it reads `act ready`, dispatches workers to worktree sub-agents (`isolation: "worktree"` per the skill), collects closes when sub-agents return, and merges/pushes the worktree branches to main on its own cadence. This is the easiest implementation because every primitive — sub-agents, worktrees, sessions, MCP — already exists. A solo developer using Claude Code today can run Mode B, with two known frictions: the auto-mode classifier still applies to the parent's merge-and-push step and requires a `.claude/settings.json` carveout there, and workers calling `act ... --push` from a worktree still hit the act-5d6a targeting bug (workaround: workers don't call `--push` at all and let the orchestrator handle pushes).
- **External harness (gas town on beads, or any equivalent).** A separate orchestrator process — built on beads, on its own bespoke abstractions, whatever — drives act as a session. The orchestrator handles dispatch, parallelism, retry, halt-handling, and integration. act is just the work-unit primitive plus durable log. Cross-language, cross-runtime; the orchestrator doesn't need to be Claude or even run in the same environment as the worker.
- **Future built-in act-orchestrator.** Possibly someday `act` ships a real orchestrator subcommand that dispatches subprocesses without an external harness. Not now; the contract should be exercised by external implementations first so we discover what's actually load-bearing before baking it into the binary.

The design implication is that act must not bake in assumptions specific to any one implementation. The Claude Code auto-mode classifier currently blocks worker pushes to main in certain configurations — but that is a *Claude-Code-harness-side* constraint, not an act constraint. Moving the push to the orchestrator is the right shape for Claude Code (it shifts the classifier interaction from per-worker-op to per-integration-cycle, where it's far cheaper to authorize even if the classifier still flags it), and it happens to be the right shape for any other harness too; the cleanliness benefit doesn't depend on which harness is hitting which constraint. Mode A's current canonical loop (worker pushes main) is then the *degenerate* case where the orchestrator and worker are the same session — i.e., the orchestrator role still exists, it's just collapsed into the same process.

## The neutral worker kernel

Rewriting `/act:work` to be work-agnostic:

```
You've been dispatched to work act issue <id> in this repo. Environment is
pre-configured by the orchestrator.

1. `act show <id>` — read acceptance criteria.
2. Do the work needed to satisfy the criteria, following this repo's CLAUDE.md
   for HOW (testing, review, formatting, gates). The criteria say what; the
   project's conventions say how.
3. Commit with the `Act-Id: act-XXXXXX` trailer (in the commit body) from
   `act show --commit-marker`. Use two `-m` flags so the trailer becomes
   its own body paragraph separated from the subject by a blank line.
   Pre-act-c4c5 this was a `(act-XXXX)` subject-line suffix; the trailer
   form is the only emission shape now (doctor resolves the old form for
   back-compat). Push to your assigned branch.
4. If criteria are satisfied: `act close <id> --reason "<one-liner>"` and exit 0.
   If a halt condition fires: `act update <id> --note "HALT: <reason>"` and exit
   nonzero. Do NOT close.

Halt conditions (per the act skill): spec ambiguity, non-additive breaking
change, cross-issue scope blocker, deeper-defect-than-described, or external
obligation. Same-branch push is not a halt.
```

No "implement," no "tests," no review-subagent reference. This works equally for "fix the broken gallery lightbox" and "review three vendor contracts and pick one and write a one-pager."

Note the asymmetry from Mode A's current canonical loop: in Mode B, the worker pushes to *its assigned branch*, not to main. Integration to main is the orchestrator's responsibility, on whatever cadence the orchestrator chooses. In the Claude-Code-with-sub-agents reference implementation, the parent session does `git merge --ff-only` from each sub-agent's worktree branch after the sub-agent returns; in a gas town implementation, the orchestrator process would do whatever its own model dictates. This is the design split that makes the loop portable across harnesses with different push constraints — workers don't have to know what their orchestrator's environment will accept.

The review taxonomy in the act skill currently assumes code diffs (`feature-dev:code-reviewer`, "review the diff," etc.). That assumption is a *project CLAUDE.md* concern, not a skill concern. The skill should describe the *principle* — work should be reviewed proportionate to risk; surface the review as a tracked act task with derivative-issues-on-close — and let projects fill in the specific reviewer tools and review-shape for their domain.

## The halt-signal contract

In Mode A, "halt and surface" means "tell the human in conversation." In Mode B, the orchestrator is the receiver and needs a parseable signal. The contract needs to be specified before either `/act:work` or any orchestrator code lands.

**Proposal:**
- Halt signal: `act update <id> --note "HALT: <reason>"` (note text begins with `HALT:`)
- Plus: nonzero exit from the worker process
- Plus: the issue stays in `in_progress` state (no close)

**Why use the existing note mechanism rather than a new subcommand:**
- It already produces a discoverable op that any reader (human or orchestrator) can grep for
- It doesn't require an act surface change
- It survives session death (the note is committed and pushed before exit)

**Orchestrator side:**
- Polls or subscribes to op stream
- Filters for `in_progress` issues with `HALT:`-prefixed notes
- Decides: human escalation, automated retry with adjusted scope, replan, etc.

**Open questions on this contract:**
1. Should `HALT:` be a structured field on the note op rather than a string convention? Structured would be cleaner long-term but a string prefix is fine for now and matches the dispatcher-prompt.md precedent of `OPEN_QUESTIONS.md`.
2. Does act need a `act watch` subcommand emitting structured events so orchestrators don't poll? Likely yes eventually, but not before a real orchestrator exists. Polling is fine for v0.

## Log noise as a control surface

The canonical loop's "push after every op" property is load-bearing only when dedup is *distributed* — i.e., when N writers race on the op log and the only way to make a claim atomic is "first-to-push-to-origin/main wins." Once an orchestrator owns assignment, dedup becomes centralized at the orchestrator, and the per-op push requirement softens dramatically. Log noise stops being a fixed cost of using act and becomes an orchestrator-controlled dial.

The design space, by increasing centralization:

- **Worker-driven, op-per-commit (current Mode A canonical loop).** Every claim, close, and note pushes to main as its own commit. Maximum visibility for any concurrent reader — agents see each op the moment it lands — at maximum log noise cost. This is the right default when there's nothing better to centralize against, i.e., a solo Mode A loop with no orchestrator layer.
- **Worker-driven, per-session bundle (`bundle_strategy=per_session`, already in act).** Within one worker's session, the close op stages into the work commit instead of landing standalone. Roughly halves the commit volume per issue. This is Mode A's existing noise-reduction lever; it's a worker-side optimization that doesn't require any orchestrator at all.
- **Orchestrator-batched claims.** The orchestrator pre-claims N issues in one op-log entry, dispatches N workers, and writes a single batch-close op when they return. Net: ~2 op commits per orchestrator batch instead of 2N per individual worker. This is only available because the orchestrator centralizes dedup — workers no longer have to publish their own claims to coordinate with peers, so the immediate-push requirement goes away.
- **Orchestrator-batched everything.** Same as above, but the orchestrator also batches creates, dependency edits, notes, etc. — buffering ops in-memory and writing them as a single op-log entry on its own schedule (e.g., end of dispatch cycle).

The trade-off is concurrent-reader latency. More batching means a quieter log but less mid-flight visibility for parallel observers. An orchestrator that batches claims for 30 minutes before flushing is invisible to any other reader during that window. Whether that matters depends on whether anyone is reading mid-flight: a solo single-orchestrator project has no other reader, so batch freely; a multi-orchestrator setup or a human watching the log live wants tighter batching to keep the log fresh. The orchestrator gets to make this call per-deployment.

The point is that most of this is **orchestrator policy, not act policy.** act's op semantics are the same regardless of when ops actually land in the log; the orchestrator chooses cadence. Honest qualifier: act does impose some constraints the orchestrator has to work within — commit-marker format (the `Act-Id: act-XXXXXX` trailer in the commit body, per act-c4c5) is hardcoded into orphan detection, op-log filenames follow a per-op convention, and the existing `bundle_strategy=per_session` knob is worker-scoped (one worker's session) rather than orchestrator-scoped (the orchestrator's lifespan, which may span many workers). So the right framing is "cadence is orchestrator policy, atomicity primitives are act-imposed." The only new act-side surface needed for the orchestrator-batched pattern is, eventually, a way for an orchestrator to write multiple ops atomically in one commit — see the open question on multi-op atomicity vs batching.

This also explains a previously-puzzling observation. The act repo dogfood found that closes-in-the-same-commit-as-work happen ~free under `per_session`, but claims still land standalone because they precede the work commit. In Mode A that's unavoidable — the worker has to publish its claim to avoid being double-claimed. In Mode B that asymmetry disappears: the orchestrator has already decided who's working what, so claims can stage indefinitely and land in batches. Mode B is *strictly quieter* than Mode A on log volume, not by act doing less work, but by the orchestrator providing the missing coordination layer.

## Existing inputs and constraints

**`session-orchestrator.md` (knowledge/projects):** Andrew has been planning this since March 28-29. The intended dogfood project is Plugin Library. The phased plan is:
- Phase 0: try built-in Agent Teams (`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`)
- Phase 1: tmux + Bash + CLAUDE.md patterns (no MCP server)
- Phase 2: Channel-based message bus
- Phase 3: multi-hierarchy
- Phase 4: UI

The act integration question lives somewhere between Phase 1 and Phase 2 — once the orchestrator can spawn workers, the question becomes "what's the worker doing?" and "how do we track its work durably?" act answers both.

**Sandboxing (`research/claude-code-docker.md`):** Concrete enough for Mode B to lean on. Auth via `ANTHROPIC_API_KEY` env var; copy `~/.claude/settings.json`, `CLAUDE.md`, `plugins/`, `projects/` per-CLAUDE-md; skip ephemeral session state. This makes "spawn a fresh agent in a container" mechanically possible. Not implemented yet, but the design is researched.

**`act-evaluation.md`:** act is ready for alpha trial in personal repos. Multi-writer concurrency is solid up to single-digit concurrent writers per repo. The architectural concerns that motivated the original commit-noise investigation have been retired. No blockers for the "two modes" question from act's side.

**`act/CLAUDE.md` rationale archive:** Several decisions in there are already foreshadowing this conversation:
- "Push after every close" — concurrent-agent visibility (Mode B has multiple agents observing the same op stream)
- "Sub-agents must use isolation:worktree" — Mode A's parallelism story, but identical principle applies in Mode B
- "Close stages into work commit" — keeps the commit pattern clean enough that an orchestrator reading git log can correlate work commits ↔ act ops without noise

These were Mode-A-driven discoveries that turn out to be Mode-B-load-bearing. Good signal.

## Test cases the design must support

Three concrete scenarios that should drive the design (and that we can stress-test the abstractions against):

**1. aac-website (Mode A, code).** Closed 2026-05-11 after 20 iterations / 23 closes / 0 halts. Full debrief in `docs/aac-website-dogfood-debrief.md`. Result: Mode A drains a code backlog end-to-end. Surfaced one correctness gap (worktree subagent `--push` lands ops on main, filed as act-5d6a) and a handful of UX gaps (act-3c89/7ecd/4b45/f800/f2ea/dfa5). Surfaced that the skill's review taxonomy can be defeated by a reviewer that can't actually read the diff — confidence numbers calibrate against nothing in that case. Surfaced that the loop generalizes for non-code work (docs triage, act-8808) with one orchestration tweak: backlog-check must precede any `act create` regardless of source. Status: closed-with-result.

**2. Cowork tasks (Mode A or B, mixed work).** Cowork as an act user would track tasks like "draft response to X," "summarize this thread," "produce three options for Y." Some are code-adjacent, most aren't. Validates that the act skill's review taxonomy generalizes (or proves it doesn't and needs splitting). Also tests whether `act ready` priorities translate to Cowork's existing UX.

**3. Plugin Library business processes (Mode B, mostly non-code).** "Vendor evaluation: assess three competitors and write a comparison." "Contract review: read this MSA and surface red flags." "Marketing copy: draft 5 variants for the homepage hero." These are the strongest test of "is act general-purpose enough." They have:
- Real acceptance criteria
- Genuine completion signals
- Multi-step work that benefits from `act_next`-style "what's next"
- Non-code review needs (a marketing reviewer is not `feature-dev:code-reviewer`)

If act + the neutralized skill handles case 3 cleanly, we have evidence that the abstractions are right. If it doesn't, we've found the gap.

## What to do now vs. what to defer

**Do now (no new code):**

1. Generalize the act skill. The review-policy section currently assumes code. Split into:
   - A *principle* section (review proportional to risk; reviews are tracked tasks; pin commit hashes; ask for "what's working well" closer)
   - A *code projects* addendum the principle section points to for code-shaped work
   - Leave room for projects to define their own review tooling in CLAUDE.md
2. Stop using "implement," "write tests," and "diff" in the canonical loop's prose where they're not necessary. The loop is `claim → satisfy criteria → close`. Code-isms are project conventions.
3. Document the halt-signal contract as a skill section. `HALT:` note prefix + nonzero exit + `in_progress` state. This is cheap to specify and unblocks Mode B design.
4. Document the worker/orchestrator push-asymmetry in the skill. Mode A's "worker pushes to main" is the degenerate case where worker and orchestrator are the same session; Mode B workers push to an assigned branch and the orchestrator handles integration to main on its own cadence. This is what unblocks the CC-with-sub-agents reference implementation (parent session does the merge/push) and reduces the Claude Code auto-mode classifier problem — workers stop tripping it because they never push to main; the orchestrator's coarser-grained pushes are easier to authorize even when they do still interact with the classifier. Skill change only, no act code change — but three specific sections of `~/.claude/skills/act/SKILL.md` need same-pass revision:
   - **Canonical-loop step 7** (`git push origin main`) becomes mode-specific: Mode A keeps it; Mode B workers push to the assigned branch only, and a separate orchestrator step does the integration push.
   - **Worktree subagent `--push` trap** is currently framed as a bug to work around until act-5d6a lands. Under the new framing it's *correct behavior* (workers shouldn't be pushing to main in the first place). The section should be reframed as a contract clarification rather than a workaround.
   - **Auto-mode caveat section** currently presents the settings carveout as the trade-off cost of using act. With orchestrator-owned pushes that cost shrinks substantially — the worker-side carveout entries (push, merge, checkout) can be removed entirely; only the orchestrator's integration step needs permission. Worth saying so explicitly.

**Do soon (when first orchestrator session runs):**

5. Add `/act:loop` and `/act:work` as slash commands in the act repo. Bodies should be thin — they lean on the skill (which is loaded automatically in any `.act/` repo) and specify only the Mode-specific shape (loop vs. single-issue). The work-agnostic kernel above is the basis for `/act:work`.
6. Try act on a non-code workstream — Cowork tasks or a Plugin Library business process — *manually*, in Mode A, before Mode B exists. This catches the "act doesn't actually generalize" failure mode early, where it's cheap to fix.

**Defer (premature):**

7. Don't file orchestrator-integration issues in act's backlog yet. The orchestrator project is still at Phase 0 (try built-in Agent Teams). act adapting to the orchestrator's needs requires the orchestrator's needs to exist first.
8. Don't design `act watch` / structured event streams. Polling is fine until polling demonstrably isn't.
9. Don't fold orchestration into the act binary. The two should stay separate; act is the work-unit primitive, the orchestrator composes it.
10. Don't try to specify the orchestrator↔act handshake comprehensively. It will be wrong. Wait for the orchestrator to call act for real and let the contract emerge from the friction.

## Open questions worth thinking about

- **Non-code "tests."** The act skill leans on tests as evidence-of-done. For non-code work, what's the equivalent? Probably: the acceptance criteria are themselves the test. A vendor-evaluation task with criterion "produces a comparison covering price, support quality, and integration complexity" is satisfied by inspecting the output. The skill should say this explicitly so a non-code agent doesn't get confused looking for a test framework.
- **Project CLAUDE.md vs. act skill precedence.** What happens when project CLAUDE.md says "all work uses TDD" but the issue is a documentation update? The project's rule should win (the agent applies judgment for trivial cases) but the precedence rule should be explicit somewhere.
- **Mode B + parallelism + non-code.** In Plugin Library, three agents writing marketing copy in parallel don't have a git-index collision problem — they're producing separate files. The "worktrees by default" rule from the act skill is code-specific. Non-code parallelism may have a much looser isolation requirement. The skill currently doesn't distinguish.
- **Cowork as act user, plus the tracking-repo provisioning question.** Cowork has its own UI and its own backend. If Cowork uses act, what's the integration shape — Cowork calls `act` CLI? Cowork imports a Go library? Cowork talks to `act mcp` over MCP? Underneath that is a deeper question: act is git-coupled by design (its op log lives in a git repo; that's how concurrency, durability, and multi-writer dedup work). But Cowork tasks like "summarize this thread" don't live in a project that has a natural git repo. Either Cowork brings its own tracking repo (shared per-org? per-user? per-workspace?), or `act init` learns to provision one when no git context exists. This isn't "act on a non-VCS substrate" — act is and will remain git-coupled. It's "act needs a git repo to track work; where does that repo come from in environments that aren't natively developer-tools projects."
- **Op-writing decoupled from CWD (filed as act-5d6a).** The aac-website dogfood surfaced that `act create/update/close --push` from a worktree subagent lands ops on main, not the worktree branch, because `--push` follows tracking config rather than committing target. Fix is a `--branch <ref>` flag. This is a Mode B prerequisite: an orchestrator dispatching workers in worktrees needs ops to follow the work, not jump to main.
- **Multi-op atomicity vs multi-op batching (two related but independent affordances).** The orchestrator-batched-claims pattern eventually wants both:
  - *Atomicity*: multiple ops land or none do, so an orchestrator that claims 5 issues and crashes after 3 doesn't leave partial state behind.
  - *Batching*: multiple ops land in one commit, so the log isn't N entries long for one logical batch.
  You can build either without the other. Today's `act create / update / close` has neither. The v0 workaround is to call `act` N times in sequence and accept both gaps (N commits, no all-or-nothing). Whether the eventual API is a new `act batch` subcommand consuming ops on stdin, a flag on existing subcommands, or a library affordance the orchestrator imports, is the design call. Don't need to ship before the first orchestrator runs, but the noise-reduction story Mode B promises depends on at least batching landing eventually.

## Recommended next concrete step

Generalize the act skill (item 1 above) and write the halt-signal contract section (item 3). Both can happen in one pass. The diff is small, doesn't require new code, and unblocks everything downstream. The act skill is the document everything else hangs off, and getting its abstractions right is the prerequisite for the slash commands, the orchestrator integration, and any non-code dogfood.

After that, manually walk act through one non-code task (a single Cowork-flavored task or a Plugin Library business-process task) in Mode A. Observe where the skill creaks. Fix what creaks. Then file the slash commands.

Orchestrator work happens on its own track, dogfooded against Plugin Library code work first (per the existing project plan), and the act integration emerges from real friction rather than design speculation.

## File pointers

- This file: `docs/orchestration-design.md`
- Sister doc: a private session-orchestrator design doc (the orchestrator project itself; not part of this repo)
- Anthropic managed-agents: https://www.anthropic.com/engineering/managed-agents
- act skill: `~/.claude/skills/act/SKILL.md`
- Existing act dispatcher precedent: `~/Workspace/act/docs/dispatcher-prompt.md`
