# Claim identity, re-claim confirmation, and unconfirmed wins

_Design note for act-2379f4, 2026-10-30. Decides three questions about `act update --claim`
(and the `act next` / MCP `act_next` paths that compose it). Grounded in `internal/claim/claim.go`,
`internal/cli/update.go` (`runUpdateClaim`, `claimGitOps`), `internal/gitops/gitops.go`
(`PullRebaseBranch`), `internal/config/{config,remote}.go`, and reproductions run against a dev
build with two checkouts of one tracker sharing a local bare `origin` (§1). Ends with the
implementation plan a cold implementer builds from._

## 1. What is broken, reproduced

Setup: tracker A (`act init`, bare `origin` added, one issue created and auto-published); tracker B
made by `act state import --from-remote <bare> <dir>`. Both report `node_id: b3c74a87`.
`git -C A/.act ls-files config.json` → tracked, so the clone carried A's identity into B.

**F2 — shared identity.** B claims: exit 0 `claimed:true winner=b3c74a87`. A claims: exit 5
`claim_lost winner=b3c74a87`. A claims again: exit 0 `claimed:true winner=b3c74a87`,
`already_held` path (no op written). Both `act show` say `in_progress, assignee b3c74a87`. Two
holders, indistinguishable — because `singleAttempt` Step 0 compares the winning op's assignee to
*our* assignee, and ours is B's.

**F1 — re-claim after a rebase conflict.** A and B each commit a different edit to a tracked file
(`config.json`) so the next rebase conflicts. B claims and pushes. A claims: the op is committed,
the fetch succeeds, the rebase conflicts → exit 1 `claim_failed`, rebase aborted, A's nested repo 2
commits ahead of `origin/main` with the claim op on top. A claims again: exit 0 `claimed:true`
from Step 0 — origin never consulted, B's earlier claim never seen. This reproduces with *distinct*
identities too: the only claim op visible in A is A's own, so Step 0 says "already held" whatever
B's id is. Decisions 1 and 2 are therefore independent fixes.

**F3 — unreachable origin.** B claims. A's `origin` URL is broken; A claims: exit 0
`claimed:true push_deferred:true`, commit queued in `.act/.pending-pushes`. Origin restored; A
claims again: exit 0 `claimed:true` — still from Step 0, still without a fetch. The "unconfirmed"
claim is never confirmed by the one command whose job that is; the queued commit only lands when
some *other* write flushes the queue, and the loss is discovered by whoever reads next.

Two facts from the code that shape the fix: `config.WriteConfig` is called only from `act init`,
so `config.json` is written once and committed; and every writer builds its clock from
`cfg.NodeID` (15 call sites: `cli/{create,update,close,delete,depadd,reopen}.go`,
`mcp/composed.go`, `importer/importer.go`, `cmd/act/{mine,ready}.go`,
`cli/bootstrap_worker.go:readSourceNodeID`).

## 2. Decision 1 — the claimant is the checkout; its identity lives in `.act/.git/config`

**Decision.** A claimant is one checkout of a tracker — one `.act/` working tree with its own op
log, index and write lock. Its `node_id` is 8 lowercase hex characters minted at random
(`crypto/rand`, 4 bytes) and stored in the nested repo's own git config as `act.nodeId`, with the
absolute path of the state root beside it as `act.nodeIdPath`. `node_id` is no longer read from
`.act/config.json` and `act init` no longer writes it there.

**Why the checkout, not the machine or the session.**
- *Machine* fails the ticket's own case on one machine: the main checkout plus every
  `bootstrap-worker` copy and every `state import` into a worktree would share an id, and F2
  recurs between them. It also needs a stable machine id, which macOS does not provide
  (`getMachineID` falls back to `os.Hostname()`, which DHCP can rename).
- *Session* breaks the one re-claim the protocol wants to be idempotent: an orchestrator resuming
  a ticket it holds (act-fdb2 made that a no-op instead of "lost race against itself"), and
  `act mine` / `ready --mine` continuity across a session boundary. Sessions sharing a checkout
  already see each other's ops on disk and serialise on `.act/.write.lock`; treating them as one
  claimant is consistent with that. What they lack is a *signal* that the hold pre-existed, which
  Decision 2 adds (`already_held` in the result).
- *Checkout* is the unit that has a distinct op log and a distinct relationship to origin, which
  is exactly what the claim protocol reasons about.

**Why `.act/.git/config`.** `internal/config/remote.go` already documents the property this needs:
"the nested .git/config file is per-clone, so it does not accidentally propagate via pull/push"
(that is why `act.role` lives there). `git clone` — which is what `state import --from-remote` and
`act-sync` do — creates a fresh config. The path fingerprint covers the other way a checkout is
made: `bootstrap-worker --from-cwd` copies `.act/.git` wholesale (bootstrap_worker.go:263 onward),
so the copy would carry the id; when `act.nodeIdPath` ≠ the current state root, the id is re-minted.
A `~/.config/act/...` file (the machine-label precedent) is wrong here because the identity is
per-checkout, not per-machine. An env override is not added: nothing needs it, and `mine`/`ready`
already have `--as`.

**Rejected: derive the id from `sha256(machine-id || path)` so nothing needs storing.** Tempting
(every copy path gets a distinct id for free) but it rides on the hostname on macOS, and a hostname
flap would silently change the holder's identity and make its own re-claim lose to itself.
Revisit if act ever gets a stable per-machine id.

**What `winner` reports.** Unchanged in shape: `winner` is the `node_id` of the checkout holding
the claim, and `claimed` is the only field a caller compares — once identities are distinct,
`claimed:false` with `winner:<id>` means another checkout, full stop. No `held_by_you` field is
added. For humans, `act doctor --json` gains a top-level `node_id` ("this checkout's claim
identity"), and `act init --json`'s existing `node_id` keeps meaning the same thing.

**Migration.** Lazy: `ResolveNodeID` mints on first use in any checkout whose nested config lacks
`act.nodeId` or whose `act.nodeIdPath` differs. No checkout inherits the legacy shared id — adopting
it anywhere would keep the laptop/mini main checkouts indistinguishable, which is the reported
case. Consequence at cutover, stated plainly: every issue currently `in_progress` reads as held by
the legacy id (`f188029a` in this repo) in every checkout, so the first `act update --claim` of such
an issue from its real holder is exit 5, not already-held. Remedy is two commands
(`act update <id> --unclaim` then `--claim`), or none — `act close` does not check the assignee.
This is the "skip backward-compat ceremony" rule applied: fewer than 30 trackers, all Andrew's.
The stale `node_id` key left in existing `config.json` files is ignored on read (unknown JSON
field) and is not rewritten. A `.act/` with no nested `.git` (none exist in practice) gets a
path-derived id without persisting it, so reads keep working.

## 3. Decision 2 — "already held" may be answered locally only when the hold is on origin

**Decision.** Reverse the pinned rule conditionally. Step 0 of `singleAttempt` may short-circuit
without a pull **only if** the checkout has no unpublished commits — `origin` exists and
`origin/<branch>` contains `HEAD`, or there is no `origin` at all. Otherwise the claim op this
checkout holds may be exactly the one a failed rebase or a failed push left stranded (F1, F3), and
the re-claim runs the fetch/rebase first:

1. `winnerOnDisk` says the winner's assignee is ours.
2. `gitOps.Unpublished()` → false, or `opts.Isolated`: return `AlreadyHeld` as today (fast path;
   this is every normal orchestrator re-claim, whose claim commit was pushed).
3. Otherwise `gitOps.PullRebase()`, classified exactly as the main path classifies it:
   - `ErrNoUpstream` / soft-fail: proceed as confirmed.
   - `ErrOriginUnreachable`: re-run `winnerOnDisk`; if still ours, return `AlreadyHeld` **with
     `PullErr` set** (an unconfirmed hold — Decision 3 decides what the caller sees).
   - rebase conflict: hard error, `claim_failed`, as today. The retry never says "held" while
     origin's history cannot be brought in.
   - success: re-run `winnerOnDisk`. Still ours → `AlreadyHeld`, then publish the pending commit
     (the Push / publish leg, so the hold becomes confirmed). Another assignee → a loss, exit 5,
     `winner` names them. **No new op is written on this path** — the retry decides on the ops
     already in the log.

`--wait` does not loop on an unconfirmed already-held result; it returns.

**Why this and not "always pull".** The idempotent fast path exists because a resumed orchestrator
re-claims often and must not pay a fetch each time; when its commit is on origin the pull would add
nothing (origin's later ops cannot displace an earlier winner). The unpublished check is the exact
condition under which local state can be lying. **Why HEAD-level and not per-op.** Any unpushed
commit means this checkout's view of origin is stale; `git rev-list --count origin/<branch>..HEAD`
is one read-only probe, no lock needed, and it is simpler than locating the op's commit.

The pinned test `TestRunClaim_IdempotentReClaimSameAssignee` ("idempotent path must not pull")
becomes the published case; a sibling asserts the unpublished case pulls.

## 4. Decision 3 — an unconfirmed claim is not a win

**Decision.** A claim whose op is committed locally but **not on origin** when the command ends —
the fetch did not reach origin (`Result.PullErr`), or the fetch succeeded and the push or publish
leg did not (`Result.PushErr`, deferred publish) — is reported as **not won**: exit **6**, result
`{"ok":false,"claimed":false,"id":…,"error":"claim_unconfirmed","reason":"origin_unreachable"|"push_failed","push_deferred":true,"push_deferred_reason":"…"}`.
The op stays committed and queued in `.act/.pending-pushes` exactly as now; nothing is retracted.
Same for the Decision-2 path when an already-held hold cannot be confirmed. `--isolated` and
`--offline` are exempt: the caller asked for no confirmation, so a local win is exit 0
`claimed:true` as today (and `--offline` queues as today).

**Why not keep exit 0.** act-2bd35b and act-8ffc13 chose exit 0 so that callers applying "non-zero
means it did not happen" would not re-run and duplicate the op. After Decision 2 a re-run of a
claim writes nothing and is the *intended* way to confirm, so that reason no longer applies to
claims. What is left is the thing the security review named: the exit code is the dispatch
decision for every shell caller, and exit 0 reads as "won". A dispatcher that cannot double-dispatch
needs the cause on the wire, not a `push_deferred` key that elsewhere means "publish chore pending".

**What a dispatcher does with exit 6.** Do not dispatch. Either re-run the same claim later (it is
idempotent: Decision 2 pulls, then answers 0 held / 5 lost / 6 still unconfirmed) or pick other
work; never treat the local `in_progress` as ownership. `act next` already propagates any non-0,
non-5 claim exit with its envelope, so it stops on 6 (correct: with origin down every pick would be
unconfirmed; claiming three issues provisionally is worse than stopping). MCP `act_next` today
treats every non-zero as a loss and moves to the next candidate — it must return the
`claim_unconfirmed` envelope as an error result on exit 6 instead of looping.

**`already_held`.** The win result gains `"already_held": true` when Step 0 answered (today's
`Result.AlreadyHeld`, never surfaced). It tells a second session in the same checkout that the hold
pre-existed; whether that is "me earlier" or "a sibling" is the caller's to know. Human form:
`Claimed <id> (winner=<id>, already held by this checkout)`.

## 5. Implementation plan

Two units, ordered. **U1 (identity)** is mechanical and self-contained; **U2 (confirmation,
Decisions 2 and 3)** changes one code path and one exit contract and should land as a single
coherent change with its spec edits. U2's integration test — the ticket's accept criterion — only
means something once identities are distinct, so U1 lands first. Not one unit: the two touch
different files, and a reviewer should be able to read "who is the claimant" apart from "when is a
claim won". Not three: splitting Decision 3 from Decision 2 would ship an intermediate state where
the re-claim pulls but still reports an unconfirmed hold as exit 0.

### U1 — per-checkout identity

Code:
- `internal/config/nodeid.go` (new): `const NodeIDKey = "act.nodeId"`, `NodeIDPathKey =
  "act.nodeIdPath"`; `func ResolveNodeID(paths LayoutPaths) (string, error)` — read both keys via
  `GetGitConfig(ActGitConfigPath(paths.Root), …)`; if id is 8-hex and path == `paths.Root`, return
  it; else mint (`crypto/rand`, `hex.EncodeToString(b[:4])`), `SetGitConfig` both keys, return. If
  `paths.Root/.git` is missing, return `sha256(paths.Root)[0:8]` without writing. Validate as
  `^[0-9a-f]{8}$` — the shape `op.Envelope.Validate` and `hlc` already enforce (both keep their
  checks unexported, so write the regexp here rather than reaching for theirs).
- `config.Config`: drop `NodeID`; `init.go:RunInit` stops computing/writing it, calls
  `ResolveNodeID` after `bootstrapNestedRepo` and reports it in `successOutput.NodeID`.
  `ComputeNodeID` and `cmd/act/main.go:getMachineID/getGitEmail` become dead — delete them
  (`InitOptions.MachineID/GitEmail` stay only for `pinNestedCommitIdentity`'s email).
- Replace every `cfg.NodeID` consumer listed in §1 with `config.ResolveNodeID(paths)`;
  `bootstrap_worker.go:readSourceNodeID` resolves against the source/target state root instead of
  parsing `config.json`. `state import --from-remote` and both bootstrap copy modes call
  `ResolveNodeID(targetPaths)` after the clone/copy so the new checkout's `act init --json`-style
  envelope and first write agree.
- `doctor.go`: add `node_id` to the `--json` top level.

Tests (`TestDocClaim_*` named, registered in `internal/cli/docs_sweep_test.go`):
- `internal/config`: `TestNodeID_MintedOnceAndStable`, `TestNodeID_RemintedWhenPathDiffers`
  (write a config with a stale path, resolve, expect a different id and the new path),
  `TestNodeID_NoNestedGitFallsBackWithoutWriting`.
- `cmd/act` (helpers: `sp6Site`, `wfxAddBareOrigin`, `createIssue`, `runActIn`):
  `TestDocClaim_Identity_StateImportGetsItsOwnNodeID` — init A, add bare origin, `act state import
  --from-remote <bare> <B>`, assert the two `act doctor --json` `node_id`s differ and neither equals
  the `node_id` key still present in `config.json` (if any). `TestDocClaim_Identity_InitDoesNotWrite
  NodeIDToConfig` — `config.json` has no `node_id` key after `act init`.
- Update `TestDocClaim_*` and sweep rows that quote `node_id derived at init from machine-id + git
  email` (help.go:79,156,177) and spec lines naming `.act/config.json node_id` (spec.md ~911,
  the `ready --mine` row ~132 in the sweep registry).

Docs: `cmd/act/help.go` IDENTITY section → "Each checkout of a tracker has a node_id, minted on
first use and kept in `.act/.git/config` (`act.nodeId`); it never travels with a clone. `assignee`
strings tag work with the checkout that holds it." `docs/spec.md` §1 config shape and `act init`
output (the `act update` section at ~911 already says `winner` is "the `node_id` of the checkout
that holds the claim" — keep it; the code now matches the prose). The
`concurrent_claim_two_writers` note (§7.4) gains: two checkouts of one tracker never share a
`node_id`.

### U2 — a claim is won when origin has seen it

Code, `internal/claim/claim.go`:
- `GitOps` gains `Unpublished() (bool, error)`: true when `origin` exists and
  `origin/<branch>` does not contain `HEAD` (missing `origin/<branch>` ref with an `origin` present
  → true). `FakeGitOps` in `claim_test.go` gains `UnpublishedResult bool` + call counter.
- `singleAttempt` Step 0 per §3: on ours-and-unpublished-and-!Isolated run `PullRebase`, classify as
  in Step 4, re-run `winnerOnDisk`; return `AlreadyHeld` (+`PullErr` on unreachable), loss, or the
  hard error. On a confirmed `AlreadyHeld` with a previously unpublished commit, run `Push()` when
  `opts.Push`, else let the caller's publish leg run (today's `!res.AlreadyHeld` guard in
  `runUpdateClaim` at update.go:1293 becomes `!res.AlreadyHeld || res.Republish`, with a new
  `Result.Republish bool` set on that path).
- `runClaimInternal`: do not loop under `--wait` when the result is a hold with `PullErr`.
- `internal/gitops/gitops.go`: `(*GitOps).Unpublished(branch string) (bool, error)` using
  `hasOriginRemote`, `rev-parse origin/<branch>`, `merge-base --is-ancestor HEAD origin/<branch>`
  (route through `runCombined` like `ancestryToOrigin`). `claimGitOps.Unpublished()` in update.go
  calls it with `c.branch`; no write lock (read-only).

Code, `internal/cli/update.go:runUpdateClaim`:
- New slug `ErrClaimUnconfirmed = "claim_unconfirmed"` in `errors.go`, exit 6.
- After `RunClaim`: if `res.Claimed && !opts.Isolated && !opts.Offline` and any of `res.PullErr`,
  `res.PushErr`, or the publish leg's `Deferred` is set → still call `deferPublish`/queue as now,
  then return `UpdateClaimResult{OK:false, Claimed:false, ID, Error: ErrClaimUnconfirmed,
  Reason: "origin_unreachable" | "push_failed", PushDeferred:true, PushDeferredReason}`, exit 6.
  `UpdateClaimResult` gains `AlreadyHeld bool \`json:"already_held,omitempty"\``; set from
  `res.AlreadyHeld` on the win path. `FormatUpdateClaimHuman` renders both.
- `publish.go:emitPublishDeferredWarning`: when `opType == "claim"` the last line reads "re-run the
  claim once origin is reachable to confirm it — a re-claim writes no second op" instead of "Do NOT
  re-run this command".
- `cmd/act/next.go`: no change needed (non-0/non-5 already propagates the envelope and code);
  add `already_held` to the JSON body alongside `warnings`. `internal/mcp/composed.go` `act_next`
  loop: on `claimCode == 6` return `(claimOut, true)` immediately; also pass `already_held`
  through. Add exit 6 to help.go's exit-code block (~906) and the spec error table (~1309).

Tests:
- `internal/claim/claim_test.go`: keep `TestRunClaim_IdempotentReClaimSameAssignee` with
  `UnpublishedResult:false` (fast path, no pull); add `TestRunClaim_UnpublishedReClaimPulls`
  (Unpublished true → PullRebaseCalls 1, CommitCalls 0, AlreadyHeld, Republish),
  `TestRunClaim_UnpublishedReClaimLosesToOriginsEarlierOp` (fake PullRebase plants an earlier
  rival op via a callback before returning nil → Claimed false, Winner rival, CommitCalls 0),
  `TestRunClaim_UnpublishedReClaimUnreachableStaysUnconfirmed` (PullErr set, AlreadyHeld true),
  `TestRunClaim_UnpublishedReClaimRebaseConflictIsHardError`, and under `--isolated` no pull.
- `cmd/act` integration (helpers in `clr_claim_rebase_test.go`: `clrRunClaim`,
  `clrSiteWithOrigin`; make B with `act state import --from-remote`). Name them for the sweep:
  - `TestDocClaim_Clr_TwoCheckoutsNeverBothHold` — B claims (0); A claims (5); A re-claims (5,
    `winner` = B's id, no new op file in A).
  - `TestDocClaim_Clr_ReclaimAfterRebaseConflictConsultsOrigin` — F1 setup (conflicting commits
    to a tracked file); A: exit 1 `claim_failed`; A again: exit 1 again (never 0) while the conflict
    stands; resolve the conflict in A's nested repo; A again: exit 5 with B as winner.
  - `TestDocClaim_Clr_UnreachableOriginClaimIsUnconfirmed` — replaces the exit-0 assertion in
    `TestDocClaim_Spec_ClrClaimWithUnreachableOriginIsUnconfirmed`: exit 6,
    `claim_unconfirmed`, `reason: origin_unreachable`, op committed, `.pending-pushes` has one
    entry; restore the URL; A again (B holds): exit 5; variant with no rival: exit 0 `claimed:true
    already_held:true` and the commit now on origin (`wfxOriginHasHead`).
  - `TestDocClaim_Clr_PushFailedClaimIsUnconfirmed` — make the bare origin reject the push with a
    `hooks/pre-receive` that exits 1 (the fetch still succeeds): exit 6 `reason: push_failed`, op
    committed and queued; remove the hook; A again with no rival: exit 0 `already_held:true`,
    commit on origin.
  - Update `TestDocClaim_Spec_PdfNextCarriesTheClaimsPushDeferred`,
    `TestDocClaim_Spec_UpdateClaimPushUnreachableOriginDefers`,
    `internal/mcp/pdf_docclaim_test.go`, `clp_next_publish_test.go` to the new contract (next/MCP
    surface the exit-6 envelope rather than a claimed:true body).
- `go test ./...` green, including `internal/cli/concurrent_test.go` (unchanged behaviour: two
  writers, one `claimed:true`, one exit 5).

Docs (same commit as the code, per AGENTS.md): `docs/spec.md` §4 step 5 and the two act-2bd35b
paragraphs (rewrite: unconfirmed is exit 6 `claim_unconfirmed`; a re-claim of an unpublished hold
rebases first; `--isolated`/`--offline` exempt), `act update` JSON examples (+`already_held`,
unconfirmed example), the `act next` exit-code line and MCP `act_next` schema, the error table row
(`claim_unconfirmed | 6 | no message: prints the claim result | none`), the `--wait` sentence.
`cmd/act/help.go` exit codes and `errors` list. `skills/act/SKILL.md` step 1: add the exit-6 case
beside `claim_lost` — "not yours yet; re-run to confirm or take other work; never dispatch on it".
`docs/README.md`: add this note under "Design and architecture" (authoritative).

## 6. Follow-ups

- `config.json`'s `last_hlc` is written only by `act init` and never updated (`WriteConfig` has one
  caller), so the HLC plausibility check always compares against `now()`. Either maintain it or
  delete the field and the dead branch in `claim.repoReference`.
- `ErrPullRebaseSoftFail` (dirty nested tree, act-68f08b) still yields a win decided without
  origin's ops. It is a legacy-layout symptom and left as-is here; if it ever fires on a current
  store it should be classified as unconfirmed like an unreachable fetch.
- With `README`/spec now saying a claimant is a checkout, `act mine` on one machine shows only that
  checkout's holds. If a cross-checkout "what does this person hold" view is wanted, it needs a
  claim payload field (`machine` from `ResolveMachine`) — not added here.
- `internal/mcp/composed.go` `act_next` treats every non-zero claim exit as a loss and retries other
  candidates; U2 fixes exit 6, but exit 1 (`claim_failed`) and exit 2 are still swallowed into the
  backoff loop and could claim nothing while reporting only "candidates". Worth its own ticket.
- `docs/README.md` says it lists the complete tracked set, but `host-affinity-design.md` has no
  row. One-line fix in whatever commit next touches that file.

## 7. Asks

None. All three calls are technical and reversible; the one operator-visible consequence (in-flight
`in_progress` issues read as held by the legacy id at cutover) is stated in §2 and costs two
commands per issue at most.
