# Session handoff — 2026-05-10 (evening)

Resumed from afternoon handoff. Cleared two p=1s and filed one derivative. v0.2 marquee work (act-c26a composed primitives) is now in.

> **Quick read:** act-75fd (eval doc refresh) and act-c26a (`act create --blocked-by` + `act_file_blocker`) both shipped, pushed clean. Reviewer's lightweight pass on act-c26a returned two important findings (self-loop guard + JSON round-trip in echo test) — both fixed inline. One cross-cutting nit filed as act-c22b. No live worktrees. Backlog now 17 ready (one closed, one filed, net -0 because the derivative replaces the closed-feature slot).

## What landed this session

- **act-75fd closed (commit a52bdb7).** Updated `docs/act-evaluation.md` to reflect post-act-a659 reality: per_session bundling + close-stages-into-work-commit approximates Dolt's "transaction = one commit" property in plain git; the original "is per-op-commit load-bearing?" question (act-6018) is resolved (hideable behind bundle_strategy). act-6181 closed. Remaining architecture concerns: act-492e, act-b7ad, act-7574 (latent multi-writer, not alpha-blocking). Issue intentionally treats this as an update pass; next material revision should follow a real alpha trial in another repo.
- **act-c26a closed (commit b4610f6).** Shipped `act create --blocked-by <id>` (repeatable, dedups) + `act_file_blocker` MCP composed tool. Single atomic git commit via `WriteOpsAndAutoCommit` with rollback on failure. 12 new tests (7 CLI, 5 MCP). Tools-count assertion bumped 15→16. `act help workflow` updated.
- **act-c22b filed (p=2, bug).** Reviewer-derived: `WriteOpsAndAutoCommit` rollback (in internal/cli/util.go) calls `unstage` on files that were never staged when a partial-stage failure forces rollback. Atomicity preserved (error discarded, file removed) but spurious git stderr emitted. The cleaner `writeBlockOpsViaInterface` pattern in composed.go (tracks `staged[]` separately) should be propagated. Affects act_block equally.

## Design decision worth preserving (act-c26a)

The marquee design question: which dep-edge direction does `--blocked-by` mean? The AC in docs/issues/act-g003-gap.md hinted both workflow A (new issue blocks existing) and workflow C (new issue blocked by existing) should reduce to one call, but a single flag with a single semantic can only serve one workflow cleanly. Andrew's call: "pick whatever is most intuitive — single choice, not flags." Shipped `--blocked-by X` = "new issue is blocked by X" (workflow C, matching `act_block`'s `blocked_by` semantic). Workflow A continues to use `act_block` after create (already 2 calls, well-optimized). Net: `--block-parent` from AC #4 is NOT implemented; deviation documented in the close reason and the commit body.

This is the kind of intuitive-direction-over-feature-completeness call worth defending if a future reviewer flags the AC drift.

## Reviewer findings worth preserving

Lightweight code-reviewer pass on the b4610f6 unstaged diff, >70% confidence filter, returned three important findings:

1. *(filed as act-c22b)* WriteOpsAndAutoCommit rollback unstage noise — cross-cutting, deferred.
2. *(fixed inline)* No self-loop guard if a `--blocked-by` id resolved to the new issue's own id under a concurrent-writer race. Added a defensive check matching the parity in act_block (composed.go:339). Cheap invariant; correctness-story value > the cost.
3. *(fixed inline)* `TestActFileBlocker_MultipleBlockers` was asserting on the in-process `[]string` type rather than the wire `[]any` shape clients actually receive. Now marshals → unmarshals → asserts on `[]any` of `string`. Catches type-shape regressions.

What's working well (do NOT regress):
- `seen[parent]` dedup after full-id resolution: an agent passing two different prefix forms that resolve to the same full id correctly produces one edge.
- Op ordering correctness: `env` (create) is index 0 in the batch; HLC clock advances monotonically via successive `Send()` calls → fold applies create before any add_dep regardless of FS ordering.
- Error envelope exit codes consistent with depadd.go: `id_ambiguous` exit 2, `issue_not_found` exit 3 (universal table).
- `TestRunCreate_BlockedBy_UnknownTarget` is the most load-bearing test: verifies no commit, no HEAD movement, no issue directories on disk after a resolution failure.

## Where things stand

- Backlog: 17 ready issues. Top of p=1 queue (4 left):
  - **act-6051** — canonical bootstrap decision (curl vs brew vs go install). This is a meta-decision Andrew should weigh in on; serves act-4fe6 / act-e6a5 / act-8416 once decided.
  - **act-ff5c** — doc-drift prevention process. Substantive design work, doable without Andrew input.
  - **act-8416** / **act-4fe6** — act in Cowork / CC Web. Each needs external-system context.
- act-9c8c (p=2, show work commits) still open; the post-act-a659 case for bumping it to p=1 still holds — work commits ARE the close commits now, and there's no surface in `act show` to find them. Smallest concrete code change of the remaining backlog.

## What to look at first when resuming

1. **act-6051 (bootstrap decision).** This is the next adoption-blocker. Probably ask Andrew which install path to canonicalize before touching anything — the answer routes which sibling issues become "the recommended way" vs "alternates."
2. **act-9c8c (show work commits).** Smallest discrete win. Single git invocation at show-time, JSON + human renderings. Could be done autonomously without surfacing.
3. **act-ff5c (doc-drift prevention).** Design-heavy; would benefit from a brainstorming pass before code. The 2026-05-10 dogfood found two real doc-drift bugs (prefix matching, missing `git push` in the canonical loop); a hook or test pattern that surfaces those drift cases automatically is the target.
4. **act-c22b (rollback unstage noise).** Trivial fix once someone touches util.go. Worth bundling with the next change that lands in that file.

## Known stale areas worth cleaning

- `bundle_strategy=per_op` still exists alongside `per_session`. The deprecation question is captured in CLAUDE.md; revisit once another repo has run on per_session+act-a659 for a while.
- Surface-gap-analysis Workflows A and C: the AC said both reduce to 1 call; in practice only C does (via `--blocked-by`), and A remains a 2-call sequence (create + act_block). The gap analysis doc isn't updated to reflect this. Low-priority; agents reading the doc will infer it from `act help workflow`.

## Operational notes

- `bin/act` is current as of b4610f6. Rebuild with `go build -o bin/act ./cmd/act` if missing.
- `.act/hooks/close` still runs gofmt + vet + tests on every close. All test suites green at session end.
- No live worktrees.
- This session ran in `main` (no worktree). All work was on cmd/act/, internal/cli/, internal/mcp/, plus docs. CLAUDE.md's "default serial sub-agents in this repo" rule held.
- The full ToolSearch / deferred-tool dance for TaskCreate took one round-trip; harmless but a reminder that the loop loads more incrementally than older sessions.
