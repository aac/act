# Redaction: taking text out of stored op files

_Design note for act-58570f, 2026-11-28. Decides how act removes a string from every op file that
holds it, how the fold stays deterministic while peers catch up, and what a peer holding the old
files does on its next sync. Grounded in `internal/fold/{fold,apply,signature}.go`,
`internal/op/{envelope,filename,payloads}.go`, `internal/index/{index,incremental}.go`,
`internal/cli/{util,publish,cache,harvest,log}.go`, `internal/gitops/{gitops,fetch_rebase,collision}.go`,
and reproductions run against a dev build in scratch trackers under `/tmp` (§1). Ends with the
implementation plan a cold implementer builds from. Doc only: nothing here is built yet._

**The shape in five lines.** `act redact --patterns-file PATH` re-issues every op file that holds a
listed string: same envelope, the string replaced by `[redacted]` in the payload's text, a
`"redacted": <generation>` key added to the payload, written under the new content hash's file
name, the old file deleted, one `redact` record op per touched issue, one commit, pushed. The fold
gains one rule — among op files that share a slot `(issue_id, op_type, hlc.wall, hlc.logical,
node_id)`, the highest `redacted` generation is the op and the rest are ignored — so a checkout that
holds both the old and the new file folds exactly like one that holds only the new. The nested
repo's git history is **not** rewritten and still holds the text (§8).

## 1. The need, reproduced

Scratch tracker A with a bare `origin`, a ticket whose description holds a planted string, the
description then edited, a second checkout B cloned from the bare (`XDG_CONFIG_HOME` pointed at an
empty dir and `ACT_TRACKER_REMOTE` unset so no real tracker is touched):

```
$ act init                                   # in /tmp/act-redact-design/A (a fresh git repo)
$ git init -q --bare ../origin.git; git -C .act remote add origin ../origin.git; git -C .act push -q -u origin main
$ act create "Reconcile brokerage statement" --description "Wire from account PLANTED-4417-9902-7731 to savings; confirm by Friday." --json
{"ok":true,"id":"act-ae2eae","prefix":"act-ae2eae","op_id":"ded8a681","committed":true,"pushed":true,"title":"Reconcile brokerage statement"}
$ act update act-ae2eae --description "Wire from the brokerage account to savings; confirm by Friday."
Updated act-ae2eae (wrote 1 op)
$ act show act-ae2eae | grep description
description: Wire from the brokerage account to savings; confirm by Friday.
$ grep -rc PLANTED-4417-9902-7731 .act/ops
.act/ops/act-ae2eae/2026-11/2026-11-28T19-41-00.457Z-37d15d7b-update_field.json:0
.act/ops/act-ae2eae/2026-11/2026-11-28T19-41-00.229Z-ded8a681-create.json:1
$ act doctor
act doctor: 0 findings
$ act state import --from-remote /tmp/act-redact-design/origin.git /tmp/act-redact-design/B   # then, in B:
$ act show act-ae2eae | grep description
description: Wire from the brokerage account to savings; confirm by Friday.
$ grep -rc PLANTED-4417-9902-7731 .act/ops | grep -v ':0'
.act/ops/act-ae2eae/2026-11/2026-11-28T19-41-00.229Z-ded8a681-create.json:1
$ git --git-dir ../origin.git grep -c PLANTED-4417-9902-7731 main -- ops
main:ops/act-ae2eae/2026-11/2026-11-28T19-41-00.229Z-ded8a681-create.json:1
$ git --git-dir ../origin.git log --oneline -SPLANTED-4417-9902-7731 -- ops
49c9f06 act-op: (act-ae2eae) create
```

`act show` no longer displays the string; the create op still holds it in a current file in both
checkouts and in the bare's current tree; `act log act-ae2eae --json` prints it (the create payload
is in the output); `act doctor` is clean, so nothing reports it.

Three further facts measured for this design:

- **The index holds it too, while it is the current value.** After `act create` + `act list`,
  `grep -rla --exclude-dir=.git PLANTED-… .act` lists `.act/index.db` beside the op file. After the
  description edit and another read it no longer matched in that probe — the row happened to be
  overwritten in place. SQLite makes no such promise (freed pages and FTS segments are not zeroed),
  so §7 deletes and rebuilds rather than relying on it.
- **Today's fold picks arbitrarily between an op and a rewritten copy of it.** Four scratch
  trackers, each with the original create op plus a hand-made twin (same envelope, the string
  replaced by a different token, a new file name) left beside it: `act show` rendered the
  replacement in two (`[redacted]`, `XXXX`) and the planted string in two (`***`, `[gone]`).
  `applyCreate` is first-create-wins and the sort tiebreak is the content hash, so which twin is
  "first" is a coin flip per op. A rewrite that lands as a new file therefore needs a fold rule
  (§4); without one a peer that still has the old file can show, and index, the old text.
- **No two distinct ops share a slot in practice.** Across 37 trackers on this machine (31,549 op
  files), the key `(issue_id, op_type, hlc.wall, hlc.logical, node_id)` is shared by more than one
  file in 55 cases, all in one tracker, and all 55 are the *same op* stored twice byte-for-byte —
  once under the pre-act-2f3d colon file name and once under the dash form. So the key identifies
  an op, and "one op at two paths" is a real shape the design has to handle (it does: §5.4).

## 2. Why the removed `act redact` is not this

`act redact` existed in 0.1.0 and was removed (act-8d1d; `CHANGELOG.md`, and the comment on
`op.ValidOpTypes`: "Writers must not emit new redact ops; the only path for true secret removal is
git-filter-repo"). Its op said "render this field as `<redacted>` from this HLC forward" and, per the
spec row that survives it, "never mutates prior payloads on disk". It masked at fold time: the text
stayed in every op file, and act-0d5d records that even the mask did not hold (`act show` still
displayed the old value). The op type is still in the enum as a parse-only entry with no writer and
no apply path.

This design reuses the command name and the op type because both now mean what the words say, and
because the op type is the one name every existing binary already parses and skips. It does **not**
reuse the mechanism. The `redact` op written here is a record, never an instruction to the fold:
`fold.ApplyDispatch("redact")` stays `nil`. What changes the folded state is that the op files
themselves hold different text.

## 3. Decision 1 — a redaction re-issues op files and records that it did

**Decision.** A redaction is an act operation, not a one-off tool. For each op file that holds a
listed string it writes a *re-issued* op: the same envelope (`op_version`, `schema_version`,
`writer_version`, `op_type`, `issue_id`, `node_id`, `hlc` all unchanged), a payload in which the
string is replaced by `[redacted]` in the text fields (§5.2) and which carries one new key,
`"redacted": N` (N ≥ 1, the generation). The re-issued op is written under the file name its own
content hash gives it (`<iso>-<hash8>-<op_type>.json`, same directory, same `<iso>`), and the old
file is deleted. Each touched issue also gets one `redact` record op (§5.5). All of it is one commit
in the nested repo, published like any write.

**Why the marker is in the payload.** The op hash is `sha256` over `{hlc, node_id, payload}`
(`Envelope.FullHash`), so a payload key is hashed with no change to `Envelope`, `hashInput` or any
existing op's hash, and an older binary computes the same hash for a re-issued op as a newer one.
Payload structs are decoded with plain `json.Unmarshal`, which ignores an unknown key, so every
apply function and every older binary reads a re-issued op as an ordinary op. `act log --json`
shows the key, which is the per-op audit trail.

**Rejected: mask at fold time (the old command).** The text stays in the file; that is the whole
need. Not revisitable.

**Rejected: rewrite the file in place, keeping its name.** Tempting because the path would then be
the identity — git would carry a modification, a peer could never hold old and new side by side,
and no fold rule would be needed. It loses on three counts. (1) The name embeds `hash8` of the
*original* content. With the rest of the payload now public in the same file, 32 bits of a hash of
the original is a filter an attacker runs candidates through: a 10-digit account number has 10^10
candidates and 2^32 ≈ 4.3×10^9 hash prefixes, so about three survive. The text would be "gone" and
recoverable. (2) `fold.OpsSignature` — the staleness key every read trusts — is sound because "a
given path carries one and only one op body" (its doc comment); an in-place rewrite makes that
false. (3) `act harvest` / `act state export` treat file-name equality as byte equality and answer
a same-name-different-bytes pair with `op_filename_collision`, exit 1. Flip condition: none while
the file name is content-derived.

**Rejected: a one-time rewrite tool that leaves no trace in the op files.** A script that edits
and commits would pass the single-checkout test and fail the second: §1 shows a checkout holding
both files folds either one. Something in the op files has to say which is the op; the `redacted`
generation is that, and once it exists the tool is an operation.

**Rejected: record the superseded files by hash or by name** (a tombstone list in the `redact`
op). Either is a hash of the secret-bearing bytes published next to everything else in those bytes
— the same oracle as (1) above, at 32 or 256 bits. The slot key identifies the old file without
any function of its content.

**Rejected: delete the old ops outright.** A create op cannot be deleted without orphaning the
issue, and dropping an old `update_field` changes nothing that re-issuing it does not, while
losing the record that an edit happened.

**Rejected for this build: a file-and-field target** (`act redact <id> --superseded description` —
blank every stored value of a field except the one in force, no pattern needed). It suits the
motivating tickets, whose descriptions were already edited, and it never asks anyone to handle the
secret. It loses to patterns as the *first* mode because only a pattern can answer "is this string
anywhere in the tracker" — other tickets, titles, close reasons, quarantined files — and prove it
with a scan (§5.6). The machinery below supports it as a second `apply` function over the same
slot re-issue; build it when an agent needs to redact without being able to extract the string.

## 4. Decision 2 — the fold: the highest generation in a slot is the op

**Decision.** `fold.discoverAndParse` — the one loader behind `Fold`, `FoldIssue` and
`FoldIssueStrict` — groups parsed ops by **slot**: `(env.IssueID, env.OpType, env.HLC.Wall,
env.HLC.Logical, env.NodeID)`. For a group of two or more it reads each member's payload key
`redacted` (absent or not an integer ≥ 1 reads as 0) and takes the maximum `m`. If `m > 0`, every
member with a generation below `m` is **superseded**: dropped before the sort, never applied, not
counted in `OpsConsumed`. A group whose maximum is 0 is left exactly as today (that is the 55
colon/dash duplicates of §1, which fold idempotently). A single-member group is never inspected, so
the rule costs one map insert per op and a payload peek only where a slot is shared.

The rule is a pure function of the set of op files, so the determinism contract (spec §6) holds
unchanged: any permutation, any machine, same output.

**What each kind of peer folds.**

| Peer holds | Folds | Shows |
|---|---|---|
| only the old files (has not pulled) | exactly as before this design; nothing about old files changed | the old text — it still has it |
| old and re-issued files side by side | the re-issued ops; the old ones are superseded | `[redacted]`, identical to the next row |
| only the re-issued files (pulled) | the re-issued ops | `[redacted]` |

Two checkouts that hold the same files produce byte-identical `act list --json` / `act show --json`
issue data; a checkout that has not pulled differs from one that has, by the text, until it pulls —
the same eventual consistency every other op has.

**Two things a redaction can change in the fold besides the text**, both deterministic and both
stated so nobody is surprised by them:

- *Exact ties.* LWW and claim order break `(wall, logical)` ties by op hash. A re-issued op has a
  new hash, so if it was tied with another node's op at the same millisecond and logical counter,
  the winner between those two may flip. Either order was arbitrary to begin with.
- *Criteria that become equal.* Acceptance criteria are a set keyed by text. Two criteria that
  differ only inside the redacted string become the same string and fold as one.

**Who else reads op files.** `act log` (`internal/cli/log.go`, its own walk) applies the same rule
through a helper the fold package exports, so the log is what the fold saw and never prints a
superseded file's payload. `internal/claim` and `cli.InClaimWindowForNode` read claim-family ops
with their own walks and are left alone: a superseded claim or close twin carries the same assignee
and the same HLC as the op that superseded it, so their answers cannot differ.

**Equal generations.** Two members at the same non-zero generation are both live and fold like any
two ops (for a create, the smaller hash is first). Identical bytes at two paths is harmless. Two
*different* bodies at one generation means two redactions re-issued the same op independently
(§6.3); the fold stays deterministic, `act doctor` reports it (`redaction-conflict`, §6.4).

**Rejected: put the generation in the envelope or the file name.** An envelope key changes
`Envelope`, forces a decision about hashing it, and makes old and new binaries hash a re-issued op
differently. A file-name marker changes `op.Parse`'s grammar, which older binaries apply in doctor
checks. The payload key needs neither.

**Rejected: a lineage (`redacted_from: <id>`) instead of a slot.** Any id of the original that is
not random is a function of its content (the oracle again), and a random one cannot be recomputed
by a second checkout redacting the same op, so two honest redactions could never recognise each
other. The slot is content-free and every checkout computes the same one.

## 5. Decision 3 — the command

### 5.1 Surface

```
act redact --patterns-file PATH [--dry-run] [--json]
```

- `--patterns-file PATH` (required). The strings to remove, one per line; `-` reads stdin (the
  `--description-file` convention). There is no flag that takes a string on the command line, and
  **a positional argument is refused with exit 2 and is not echoed in the message** — it may be the
  secret someone typed by mistake. The message is the fixed text
  `act redact: takes no positional arguments; strings to remove are read from --patterns-file`.
- `--dry-run`. Plans against the tree as it stands — no lock, no fetch, no write — and prints the
  counts a run would produce. Earns its place because the operation cannot be undone by act and a
  mistyped pattern file is the likeliest mistake.
- `--json`. One JSON object on stdout.

It takes none of the universal write flags (`--no-commit`, `--push`, `--isolated`, `--offline`,
`--branch`, `--verify`): each is an unknown flag, exit 2 `bad_flag`. A redaction that is committed
but deliberately not published, or staged but not committed, is a half-state with no use. It always
covers the whole tracker; there is no per-issue scope, because "gone from this ticket, still in
that one" is not a result anyone wants from this command. No MCP tool is added: nothing in an agent
loop redacts, and a tool argument is one more place a string could be passed inline.

### 5.2 What is rewritten: the payload's text fields, and nothing else

| `op_type` | Text fields (JSON pointers into the payload) |
|---|---|
| `create` | `/title`, `/description`, each `/accept/N` |
| `update_field` | `/value`, only when `/field` is `title` or `description` and the value is a string |
| `add_accept` | `/criterion` |
| `set_accept` | each `/criteria/N` |
| `close`, `reopen`, `unclaim` | `/reason` |
| `add_external_dep`, `remove_external_dep` | `/ref` |
| `redact` (pre-act-8d1d payloads only) | `/new_value` |
| every other type, known or not | none |

Matching and replacement run on the **decoded string**, so a pattern containing a quote, a newline
or non-ASCII text matches whatever its JSON escaping in the file. Everything else in the op is
copied through: the payload is decoded to `map[string]any` and re-encoded exactly the way
`Envelope.Marshal` already round-trips every op, so an op with no hit re-marshals to its original
bytes (U2 asserts this over the golden ops). An allowlist, not "every string in the payload",
because the other strings are structure — `nonce`, `parent`, `field`, `edge_type`, ids — and a
replacement inside one corrupts the op. A listed string found anywhere outside these fields is a
**residual** (§5.6), never silently skipped. Length caps are not re-applied: `[redacted]` can be
longer than the string it replaces, caps bind new writes only (spec "Length caps"), and the fold
never checks them.

### 5.3 Patterns

Read the whole file as UTF-8, split on `\n`, strip one trailing `\r`, trim leading and trailing
spaces and tabs, drop empty lines, de-duplicate. Trimming is deliberate: an invisible trailing space
would turn a real redaction into a silent zero-match, and a secret's trimmed core is still the
secret. Each remaining line is one **literal, case-sensitive** pattern; there is no comment syntax
and no regular expressions (a regex invites a pattern broader than the person meant, and "does it
still match" has no crisp answer to verify). Rules, each exit 2 `bad_input` naming the 1-based line
number and never the line's text:

- no patterns at all;
- a pattern shorter than **8 bytes** — below that a pattern collides by chance with act's own
  random hex (a 4-digit pattern appears in roughly one `nonce` in 2,300), which the residual scan
  cannot tell from a real occurrence. A shorter secret is given with the words written next to it;
- a pattern that occurs inside the replacement text `[redacted]`, which would re-match forever.

Apply order is longest pattern first, ties by byte order, repeated until no pattern matches (at
most 8 rounds; still matching is a residual). The order is fixed so two checkouts given the same
file produce byte-identical re-issued ops. For the scans of §5.6 each pattern has two **raw forms**:
its bytes, and its bytes as `canonicaljson` would escape them inside a string, when that differs.

### 5.4 One pass over the tree

A pass plans, then executes; `--dry-run` stops after the plan.

**Scan.** Parse every `*.json` under `ops/` with `op.UnmarshalTolerant` (a file that will not
parse aborts with `redact_failed`: the fold is already broken and that is `act doctor`'s to
report). Group by slot (§4). Also parse every `*.json` under `.failed-ops/` and `.collisions/` —
op files act moved aside, which can hold the same text; these are handled one file at a time, with
no slot grouping and no git.

**Per slot in `ops/`.**
1. Split the members into *live* and *dead* by the fold rule.
2. *Own-stale rule.* If the live members hold two or more different bodies, all marked, and some
   were written by an earlier pass of this same run and some were not, the run's own are moved to
   dead (§6.3 is why).
3. Apply the patterns to each live member's text fields. If nothing in the slot changes: delete
   the dead files, count them as `leftovers_removed`, done.
4. Otherwise re-issue **every** live member at generation `max generation in the slot + 1`,
   changed or not, each written into the directory of the file it replaces under the name its new
   hash gives (`op.Filename`; if that path already holds identical bytes, it is the file — two
   paths of one op collapse to one here), atomically and with read-back (export the helper from
   `internal/op`; do not re-implement `atomicWrite`). Then delete every file of the slot that is not
   a newly written path. Re-issuing all live members is what keeps two genuinely distinct ops that
   share a slot — never observed, §1 — both alive rather than letting the rule hide one.

**Order inside the pass is write-then-delete**, so at every instant the tree folds correctly:
with a re-issued file and its original both present the fold already prefers the re-issued one. A
run killed mid-pass leaves a tree that reads as redacted, and the next run finishes it — its plan
finds the originals as dead members, and an issue counts as *touched* (below) when it holds a
marked op and `git status --porcelain -- ops/<issue>` is not empty.

**Quarantined files.** One with a hit is re-issued in place the same way (generation = its own + 1).
One whose slot has a higher-generation member under `ops/` is a pre-redaction copy and is deleted.

**Record, stage, commit.** For each issue with at least one re-issued op, write one `redact` record
op (§5.5). Then `git add -A -- ops/<issue>` for every touched issue and one pathspec-scoped commit
of those directories, `--no-verify`, subject `act-redact: re-issued <N> op file(s) in <M> issue(s)`
(or `act-redact: removed <N> superseded op file(s)` when nothing was re-issued). No hook runs: hooks
fire once per logical op on the writer that originated it, and a re-issued create is not a create.

### 5.5 The `redact` record op

```jsonc
// redact — written by `act redact`; a record, never applied by the fold
{ "ops_rewritten": 2, "replacements": 3 }      // both integers >= 1; per issue, per run
```

Ordinary envelope: this checkout's `node_id`, an HLC from one `hlc.Clock` created for the run,
written with `op.ProbeAndWrite`. `op.RedactPayload` with a `Validate` and a `ValidatePayload` case
replaces today's "no payload validator registered" rejection. The pre-act-8d1d payload
(`field_path`, `new_value`) is never validated — nothing validates payloads on read — and
`ApplyDispatch("redact")` stays `nil`, so old and new records are both skipped by the fold. The
record does three jobs: `act log` shows that, when and from which checkout a redaction happened;
its file name (`*-redact.json`) lets doctor find issues with redactions without parsing every op;
and the set of those names is what tells a peer's index to rebuild from scratch (§7). It holds no
pattern, no hash of one, and no hash or name of any file it replaced.

### 5.6 The run

1. Parse flags and read the patterns — before touching the tracker, so a bad file changes nothing.
2. Resolve the repo and `.act/`; the usual `not_in_git` / `act_not_initialized` /
   `tracker_not_checked_out` exits.
3. `--dry-run`: plan, print, exit 0 — or 1 with `redact_residual` if the plan has residuals.
4. Take the write lock (`gitops.AcquireWriteLock`) and hold it to the end.
5. If the nested repo has `origin`: `FetchAndRebase(<current branch>)`. `ErrShallowRecovered` is
   success. A fetch that does not reach origin is **exit 3 `remote_unreachable`, nothing changed**
   — redacting a stale tree is how two redactions come to disagree (§6.3), and the person asking
   wants origin clean, not just this disk. Any other failure is exit 1 `redact_failed`.
6. Rounds, at most 5:
   - Plan. **If the first round's plan has a residual, exit 1 `redact_residual` with nothing
     changed.** A residual is a raw form of a pattern that would remain after the pass: in an op
     file's bytes after its text fields are replaced (so: in a structural field, an object key, the
     envelope), in an op type with no text fields, or in any other regular file under `.act/`
     outside `.git/` and outside the derived set of §7. Refusing whole is the safe failure — a
     pattern like a date would otherwise blank prose across the tracker and still be everywhere.
   - Execute, record, stage, commit (§5.4). `n` = files re-issued plus files removed.
   - If `n == 0` and nothing is unpublished (`gops.Unpublished()` false, or no `origin`): stop.
   - `PublishCommittedOp(gops, "redact", "", stderr)` — the existing flush / fetch / rebase / push /
     verify path, which also fires the orchestrator's upstream sync and answers `NoRemote` where
     there is no `origin`. A deferred publish ends the rounds with the run **unconfirmed**.
   - With no `origin`, or when `n == 0`, stop. Otherwise go round again: the publish may have
     rebased this commit onto ops the first plan never saw, and the patterns are still in memory
     to check them. A fifth round that still changes something is exit 1 `redact_failed`.
7. `index.ScrubDerived` then `EnsureCurrent` (§7) — always, even when nothing matched.
8. **Verification scan:** every regular file under `.act/` outside `.git/`, raw bytes, every raw
   form. Any hit is exit 1 `redact_residual` (with `details.changed` true if a round committed).
9. Print the result. Exit 0, or 6 when unconfirmed.

So **exit 0 means the scan found none of the strings in any file under `.act/` outside `.git/`,
and origin (if there is one) has the commit.** Running it again is the way to check and is a no-op:
zero counts, no commit, exit 0.

### 5.7 Output, exit codes, error codes

Success, `--json` (keys always present; `published` is false when there is no `origin`):

```json
{"ok":true,"dry_run":false,"patterns":1,"issues":1,"ops_rewritten":2,"replacements":3,
 "leftovers_removed":0,"quarantined_rewritten":0,"published":true}
```

Human form, stdout:

```
Redacted 3 occurrence(s) in 2 op file(s) across 1 issue(s); removed 0 leftover file(s).
Published to origin.
Not rewritten: the git history of .act/ still holds the original text, here, on origin and in every clone and backup.
```

(`Would redact … Nothing was changed.` under `--dry-run`; `No stored file holds any of the 1
pattern(s). Nothing was changed.` on zero counts; `No origin remote: this checkout is the only copy
act knows of.` in place of the second line.) The third line is printed on every non-dry run that
changed something. **Nothing act prints — result, error, warning — contains a pattern, matched
text, or the text around a match.** Paths appear only in `redact_residual`'s `details.paths`
(relative to `.act/`), because the person has to go and look.

Unconfirmed, exit 6: the success object with `"ok":false,"error":"redact_unconfirmed",
"published":false,"push_deferred":true,"push_deferred_reason":"…"`, after the usual
`act: WARNING:` block, whose last line for `redact` reads `re-run act redact with the same patterns
file once origin is reachable; it re-issues nothing twice and publishes this commit`.

| exit | code | when |
|---|---|---|
| 0 | — | done and verified; also "nothing matched" |
| 1 | `redact_residual` | a listed string is somewhere act does not rewrite. `details`: `paths`, `changed` |
| 1 | `redact_failed` | a step failed (parse, write, stage, commit, rebase, five rounds); the message names the step. Re-run: a partial pass is finished by the next one |
| 1 | `write_lock_timeout` | as for every write |
| 2 | `bad_flag` | unknown flag, missing `--patterns-file`, a positional argument |
| 2 | `bad_input` | no patterns, a pattern under 8 bytes, a pattern inside `[redacted]` |
| 3 | `file_not_found` | the patterns file |
| 3 | `not_in_git`, `act_not_initialized`, `tracker_not_checked_out` | as elsewhere |
| 3 | `remote_unreachable` | `origin` is configured and the first fetch did not reach it; nothing changed. `details`: `url` (masked), `stderr_tail` |
| 6 | `redact_unconfirmed` | redacted and committed here, not on origin. Prints the result object, not an envelope, like `claim_unconfirmed` |

New codes: `redact_residual`, `redact_failed`, `redact_unconfirmed`. Exit 6 keeps one meaning —
"committed here, origin has not seen it, re-run to confirm" — now for two commands. The spec
sentence calling `remote_unreachable` "a `bootstrap-worker` clone-failure outcome, not a write-path
one" gains the exception: an ordinary write degrades to a deferral, a redaction refuses to start.

**Rejected: roll the local commit back when the push fails** (`git reset --hard`), so that a
redaction is all-or-nothing against origin. It needs a destructive command in a checkout that an
unlocked sweep (`act-sync`) may have committed on top of, and it puts the string back on the one
disk that was clean. Exit 6 with an idempotent re-run is the claim precedent and loses nothing.

## 6. Decision 4 — sync: what a peer does

### 6.1 The ordinary case

A peer that holds the old files and syncs — act's own read-path or write-path `FetchAndRebase`, or
an outside `git pull --rebase` such as `act-sync` — receives one commit that deletes the old paths
and adds the re-issued files and the record. Git removes the old files from its working tree. Its
own unpublished commits replay on top; they only ever add other paths, so there is no conflict, and
nothing in them re-adds an old path. Its next act read sees a new `*-redact.json`, deletes its
index and derived files and rebuilds them (§7). After that no file under its `.act/` outside
`.git/` holds the string, and its fold equals the redacting checkout's. The same holds for an
orchestrator checkout receiving the push under `receive.denyCurrentBranch=updateInstead`.

The old files do not come back from this peer: it has no commit that adds them (they arrived from
origin, where the redaction now sits on top of them), and act never writes an op to an existing or
former path.

### 6.2 Copies that sync by file, not by git

`act state export` / `act harvest` copy every op file the host lacks *by name*
(`RunHarvest`, the candidate loop). A worker copy made before the redaction still has the old file,
under a name the host no longer has, and would copy it back in. **Guard:** for each candidate the
host lacks, parse it and glob the host's same directory for `<iso>-*-<op_type>.json` (both the
dash and the legacy colon form of `<iso>`); if a host file in the candidate's slot has a higher
`redacted` generation, the candidate is skipped with `SkippedOp{Reason: "superseded_by_redaction"}`
and never copied. The glob keeps the check proportional to the candidates, not the tracker.

### 6.3 Two redactions of the same op

Checkout A redacts string *a*, checkout B redacts string *b*, both from the same original op X,
neither having seen the other. A pushes X′a (generation 1). B's publish rebases onto it and the
tree now holds X′a (which still has *b*) and B's X′b (which still has *a*), both generation 1. The
fold is deterministic — both are live, the smaller hash is first — but *a* is back in a current
file, written by B from the original it held. No content-free rule can know that; only A's
patterns can.

Three things keep this from happening or from staying:

- The first fetch is mandatory (§5.6 step 5), so the window is the seconds between B's fetch and
  B's push, on a tracker with one operator.
- After every publish B plans again (§5.6 step 6) with its patterns still in memory, and the
  **own-stale rule** (§5.4) settles the slot: X′b is B's own output from this run, X′a is not, so
  X′b is dropped and X′a — the version that already carries A's redaction — is re-issued at
  generation 2 with *b* removed. The result is one file with both gone, published seconds later.
- If B dies inside those seconds, or the commit was queued (exit 6) and a later ordinary write
  published it with no patterns in hand, the tie stands and `act doctor` reports it (§6.4). The
  remedy is to re-run each redaction: each run re-issues both bodies with its own string removed,
  and once both strings are gone from both the bodies are byte-identical and collapse to one file.

**Rejected: make the merge automatic** by recording each replacement as a span over the original
text, so any checkout can union two redactions without the patterns. It is a sound CRDT and a
second mechanism — span bookkeeping in every re-issued op, coordinate mapping, its own tests — for
a race between two people who are the same person. Revisit if `redaction-conflict` is ever seen.

### 6.4 `act doctor --check redaction`

One check, scoped to issues that have a `*-redact.json` (found by name, so the cost is nil on a
tracker that has never redacted). It needs the op files to parse, so it is skipped when the fold
fails, like the other fold-dependent checks. Two finding names, both error severity:

- **`redaction-leftover`** — a superseded op file is present under `ops/`, or a pre-redaction copy
  sits under `.failed-ops/` or `.collisions/`. The fold already ignores it, but the text is on disk
  and, if tracked, will be pushed. Message: the count, the paths, and `remove with 'act doctor
  --fix'`. **`--fix`** deletes them under the write lock, commits the removal of tracked ones
  (`act-redact: removed <N> superseded op file(s)`) and publishes like a write.
- **`redaction-conflict`** — a slot holds two or more different bodies at the same non-zero
  generation. Message names the slot's files and says: `two redactions re-issued this op
  independently and each may still hold the other's string; re-run each 'act redact' with its
  patterns file`. `--fix` does nothing for it.

### 6.5 What can still put an old file back, and why reads do not sweep

One route survives §6.1–6.2: a commit that *adds* the old file being replayed after the redaction
— the same op having reached the redacting checkout by a file copy (harvest) while its original
commit was still unpublished somewhere else. The replayed file is inert to the fold, is reported by
`act doctor` as an error (so `act remote enable` and any health sweep see it), and is removed by
`act doctor --fix` or by any later `act redact` run.

**Rejected: sweep superseded files automatically on every read or after every rebase.** It would
close that route without a human, at the price of reads that delete tracked files and make commits
under the write lock — and it still would not see an outside `git pull`. Reads staying read-only
is worth more than automating a case that needs two unusual things to coincide. Revisit if doctor
ever reports a `redaction-leftover` in a real tracker.

One thing no mechanism here covers, by construction: an op holding the string that the redacting
checkout **had never seen** — written on another machine and still unpublished there. When it is
published it is a new op, not a returning one, and nothing in the tracker knows the string (storing
a hash of it would be the oracle of §3). Hence the operating rule in the help text: run the same
command on any checkout that had unpublished work.

## 7. Decision 5 — the index and the other derived files are deleted, not updated

`index.db` stores titles and descriptions and an FTS copy of both. An ordinary refresh replaces
rows inside the same file, and SQLite does not zero what it frees (§1). So when a redaction arrives
the derived files are **removed and rebuilt**:

- `index.ScrubDerived(stateRoot)` removes, when present: `index.db`, `index.db-wal`,
  `index.db-shm`, `index.db-journal`, `index.db.malformed-*`, `.doctor-rebuild.db*`,
  `fold-checkpoint.json`, `fold-checkpoint.json.tmp` and `.slow-writes`. All are machine-local
  files from the `nested-exclude` list. The checkpoint holds a hash of each issue's folded text and
  `.slow-writes` holds the `hash8` of ops whose commit was slow — small, but the same kind of
  fingerprint §3 refuses to leave in a file name. A file that cannot be removed is an error
  (`index_rebuild_failed`), and the next read tries again.
- **The redacting checkout** calls it at the end of every non-dry run (§5.6 step 7).
- **Every other checkout** is triggered by state, not by an event, so an outside `git pull` is
  covered: `fold.OpsSignatures` already walks every op file on every read; it additionally collects
  the relative paths ending `-redact.json` and returns `RedactKey` — `""` when there are none,
  else the same hash it uses for the tree key over that sorted list. `index_state` gains a second
  row, `redact_key`, written by `Rebuild` in the same transaction as `ops_build_key`.
  `EnsureCurrent` compares before anything else: stored row (absent reads as `""`) ≠ current key →
  close the handle, `ScrubDerived`, reopen, `ApplySchema`, full `Rebuild`. A tracker with no redact
  records never scrubs; one carrying pre-act-8d1d records scrubs once on upgrade.

**Rejected: `PRAGMA secure_delete` and FTS5's `secure-delete`.** They would zero freed content as
it goes, with no trigger at all, but they change every index write for every tracker to serve a
rare event, they do not reach frames already in the WAL, and "the file is gone" is a property a
test can assert where "every freed page was zeroed" is not.

## 8. What still holds the text after a clean run

Said plainly, here and in `act help redact`:

1. **The nested repo's git history.** Every commit made before the redaction — in this checkout's
   `.act/.git`, in the tracker's bare on the mini, in any `origin-upstream`, in every other clone —
   still contains the original op file; `git -C .act log -p -S<string> -- ops` prints it and
   reflogs keep it reachable. `act redact` does not rewrite history. That is out of scope for this
   ticket and held on Andrew's answer elsewhere; until it is done, treat the string as readable by
   anyone who can read those repositories, and rotate it if it can be rotated.
2. **Backups** of any of those repositories and of any checkout taken before it synced.
3. **Checkouts that have not synced**, and worker copies or stale clones that never will. The
   command reports on the checkout it ran in and on origin, nothing else.
4. **The issue id**, when the string was in an issue's *create* op. The id is the first 6 hex
   digits (24 bits) of a hash over the original create payload, and after redaction everything else
   in that payload is in the re-issued file. Candidates can be tested against it: of the 10^10
   ten-digit numbers about 600 fit a given id; of the 10^8 eight-digit ones, about 6. It cannot be
   removed without retiring the id, which host-repo `Act-Id:` trailers and other tickets' edges
   point at. Strings that entered through a later `act update` leave no such trace.
5. **Anything copied out of the tracker**: host-repo commit messages, handoff docs, session
   transcripts, the patterns file itself (the caller deletes it).

## 9. Help text

New topic `act help redact` (alias `redaction`), constant `helpRedact` in `cmd/act/help.go`,
verbatim:

```
act — redact

WHAT IT DOES
  'act redact' removes a string from every stored op file that holds it.
  An edit with 'act update' only adds a newer op; the older files, the
  create op included, keep the old text, and 'act log' still prints it.
  Redaction replaces the string with [redacted] inside those files:
  each is written again as a new op file, the old file is deleted, and
  a 'redact' op on each issue records that it happened. 'act show' and
  'act log' then render [redacted] in its place.

USAGE
    act redact --patterns-file PATH [--dry-run] [--json]

  PATH lists the strings to remove, one per line; '-' reads stdin. They
  are never taken from the command line: a positional argument is
  refused (exit 2) without being echoed. act prints counts only — never
  a pattern, the text it matched, or the text around it.

  Patterns are literal and case-sensitive, not regular expressions.
  Surrounding spaces and tabs are trimmed and blank lines skipped. Each
  must be at least 8 bytes; give a shorter secret together with the
  words written next to it. List every written form (with and without
  dashes or spaces) on its own line. To build the file without
  displaying the secret, extract it straight into the file, e.g.
    grep -rhoE '<its shape>' .act/ops | sort -u > /tmp/patterns
  and delete the file afterwards.

  --dry-run prints the counts a run would produce and changes nothing.

WHAT IT REWRITES
  Titles, descriptions, acceptance criteria, close/reopen/unclaim
  reasons and external dep refs, in every issue, plus the op files act
  set aside under .act/.failed-ops/ and .act/.collisions/. It then
  deletes and rebuilds .act/index.db. If a string also occurs somewhere
  act does not rewrite (an assignee, a machine label, a hook script),
  nothing is changed and the command exits 1 (redact_residual), naming
  the files. Exit 0 means a final scan found none of the strings in any
  file under .act/ outside .act/.git. Running it again is a no-op and
  is how to check.

OTHER MACHINES
  The rewrite is one commit, pushed like any write; origin must be
  reachable or nothing is changed (exit 3). Another checkout drops the
  old files and receives the rewritten ones on its next sync, and
  rebuilds its index on its next read; until then it still holds and
  shows the old text. Run the same command on any checkout that had
  unpublished work. 'act doctor' reports an old file that reappears
  (redaction-leftover; 'act doctor --fix' removes it).

  Exit 6 (redact_unconfirmed): this checkout is redacted but the push
  did not reach origin, so origin and every other checkout still hold
  the text. Re-run the same command once origin is reachable.

WHAT IT DOES NOT REMOVE
  act redact does not rewrite git history. Every commit of the nested
  .act/ repo made before the redaction still holds the original text —
  in this checkout, on origin, on any upstream, in every clone and in
  every backup of them — and 'git -C .act log -p' prints it. Treat a
  redacted secret as still readable by anyone who can read those
  repositories, and rotate it if it can be rotated.
  An issue's id is derived from its original create op, so for a string
  that was in an issue's first title or description the id remains a
  24-bit fingerprint of it.
  Text copied out of the tracker (host-repo commits, handoff docs,
  transcripts) is outside act.

EXIT CODES
  0  done and verified (or nothing matched)
  1  redact_residual, redact_failed
  2  usage: bad flag, positional argument, bad patterns file
  3  origin unreachable (nothing changed), no tracker, file not found
  6  redact_unconfirmed
```

Also in `cmd/act/help.go`: `act help redact` added to DEEPER DIVES (`# remove a string from stored
op files`), `redact` added to the overview's subcommand list and to `bareUsageMsg` in `main.go`,
the unknown-topic line lists `redact`; `helpOpsModel`'s "Files are never modified after creation"
becomes "A file is never modified after creation. 'act redact' is the one command that replaces
files: it writes each affected op again under a new name and deletes the old one (act help
redact)."; `helpErrors` EXIT CODES gains `redact_unconfirmed` on the exit 6 line and the three
codes in its list. The flag help strings: `--patterns-file` = `file listing the strings to remove,
one per line (UTF-8); use - for stdin. Strings are never taken from the command line`; `--dry-run`
= `print the counts a run would produce and change nothing`.

## 10. Implementation plan

Five units, in order. Each lands with its spec and help edits and its `TestDocClaim_*` rows in
`internal/cli/docs_sweep_test.go` in the same commit (AGENTS.md "Documentation discipline"), and
each ends with `go test ./...` green. Rationale comments go beside the mechanism (the slot rule at
`discoverAndParse`, rename-not-rewrite at the re-issue function), not only here. U1 first because
it makes a re-issued file safe to exist before anything writes one; U2 is pure and testable without
git; U3 is the command; U4 is what makes a second checkout clean; U5 is detection and the one
file-copy door. The ticket's four accept criteria are met at the end of U4 (script in §10.6); U5 is
required by §6 and is not optional.

### U1 — fold: the slot rule

Code: `internal/fold/fold.go` — in `discoverAndParse`, after the walk, group by slot and drop
superseded members per §4 (peek the payload with a one-field struct, only for groups ≥ 2). Export
`fold.SlotState(rootOps, issueID string) (superseded []string, conflicts [][]string, err error)`
(`issueID == ""` = every issue; `conflicts` = groups left with two or more *different* bodies at a
non-zero maximum) and a small exported predicate over a set of envelopes that `internal/cli/log.go`
uses to filter its own walk. No change to `Envelope`, `hashInput`, `ApplyDispatch` or any apply
function.

Docs: spec "Op-fold algorithm" (a step between Parse and Sort, and a paragraph), "Determinism
contract" (one sentence), the §3 conflict-table `redact` row and the "Redact of an already-redacted
field" edge case (both describe the removed command; replace with pointers to the new section),
clarification 5.A.2 (mark superseded).

Accept: `go test ./internal/fold -run 'TestSupersede|TestSlotState' -v` passes and lists
`TestSupersede_MarkedOpHidesUnmarkedSlotMate` (fold of {original create, re-issued create} renders
byte-identical to fold of {re-issued create} alone, for four different replacement tokens — the §1
experiment), `TestSupersede_HigherGenerationWins`, `TestSupersede_UnmarkedSlotMatesFoldAsBefore`
(two byte-identical unmarked files: output unchanged from today),
`TestSupersede_EqualGenerationBothLive`, `TestSupersede_PermutationInvariant` (50 seeds, shuffled
file order), `TestSlotState_ReportsSupersededAndConflicts`.
`go test ./cmd/act -run TestDocClaim_Redact_LogOmitsSupersededFiles` passes (plant both files in a
scratch tracker; `act show` and `act log --json` contain the replacement and not the original).

### U2 — `internal/redaction`: patterns, apply, plan, execute

A new package with no git and no CLI in it (named `redaction`, not `redact`:
`internal/gitops/redact.go` already owns that word for URL-credential masking).

- `LoadPatterns(r io.Reader) ([]string, error)` — §5.3; errors carry a line number, never text.
- `Apply(env op.Envelope, patterns []string) (out op.Envelope, replacements int, err error)` — §5.2;
  does not set the marker. `Reissue(env, generation)` sets `payload.redacted`.
- `NewPlan(stateRoot string, patterns []string, own map[string]bool) (*Plan, error)` — §5.4 scan
  and per-slot decisions, residual paths (§5.6), counts. Reads only.
- `(*Plan).Execute() (touchedIssues []string, perIssue map[string]Counts, err error)` — writes then
  deletes, adds new paths to `own`. No records, no staging.
- `Scan(stateRoot string, patterns []string) (paths []string, err error)` — the raw verification
  scan of §5.6 step 8.

Also `internal/op`: export the atomic write-with-read-back helper; add `RedactPayload` + `Validate`
+ the `ValidatePayload` case; update the comment on `ValidOpTypes` and the two tests that pin
"redact has no validator" (`payloads_test.go`).

Docs: spec "Op envelope" (the enum paragraph: `redact` has a writer again and still no apply
path), "Op type payloads" (the new `redact` payload; a note that a re-issued op of any type carries
`"redacted": N`), "Op file naming" (a re-issued op takes a new name).

Accept: `go test ./internal/redaction ./internal/op` passes, including
`TestLoadPatterns_TrimsSkipsBlanksAndDedupes`, `TestLoadPatterns_RejectsShortPatternByLineNumber`
(asserts the error text does not contain the pattern), `TestLoadPatterns_RejectsPatternInsideReplacement`,
`TestApply_TextFieldsPerOpType` (one row per row of §5.2, plus a structural field left alone),
`TestApply_NoHitRoundTripsByteIdentical` (every op under `internal/fold/testdata/golden/`),
`TestApply_MatchesAcrossJSONEscapes` (a pattern with a quote and a non-ASCII rune),
`TestPlan_ReissuesEveryLiveSlotMemberAtNextGeneration`, `TestPlan_TwoPathsOfOneOpCollapse`,
`TestPlan_OwnStaleYieldsToForeign`, `TestPlan_ResidualInStructuralField`,
`TestExecute_WritesBeforeDeleting` (fail the delete step; the tree still folds redacted),
`TestExecute_InterruptedPassIsFinishedByTheNext`.

### U3 — `act redact`

Code: `cmd/act/redact.go` (flags, positional refusal, patterns via the `descfile.go` reader),
`internal/cli/redact.go` (`RunRedact`: §5.6 steps 2–9, the record ops, stage and pathspec-scoped
commit, result and error shapes of §5.7), `cmd/act/main.go` (dispatch, `bareUsageMsg`),
`internal/cli/errors.go` (three codes), `internal/cli/publish.go` (`emitPublishDeferredWarning`
case for `redact`), `cmd/act/help.go` (§9). `internal/cli/log.go` summary note for a `redact` op:
`re-issued <N> op file(s)`. Until U4 lands, step 7 removes and rebuilds `index.db` inline.

Docs: spec — a new `### act redact` command section (surface, §5.2 table, §5.3 rules, §5.6
guarantee, output, exit codes), the error table rows and the `remote_unreachable` sentence, exit
code 6 in "Universal exit codes", `redact` in the `.pending-pushes` `op_type` list, "Hooks NEVER
run on" gains `act redact`. `docs/README.md` row for this note. `skills/act/SKILL.md`: three lines
— a secret in a ticket is fixed with `act update` then removed with `act redact`; history is not
rewritten. `CHANGELOG.md` Unreleased/Added.

Accept: `go test ./cmd/act -run 'TestDocClaim_Redact' -v` passes and lists
`…_RemovesStringFromEveryOpFile` (create + later `update_field` both hold it; afterwards
`grep -r` under `.act/ops` finds nothing, `act show` renders `[redacted]`, `act doctor` exits 0 with
0 findings, JSON counts are `ops_rewritten:2`), `…_PrintsCountsNeverTheString` (stdout and stderr
of a run, a dry run, a residual refusal and an exit-6 run do not contain the planted string),
`…_PositionalArgumentRefusedUnechoed`, `…_PatternsFromStdin`, `…_ShortPatternIsBadInput`,
`…_DryRunChangesNothing` (HEAD and `git status --porcelain` unchanged),
`…_ResidualRefusesBeforeChanging` (the string also given as a `--machine` label: exit 1,
`details.paths` names the op file, HEAD unchanged), `…_SecondRunIsANoOp` (exit 0, zero counts, HEAD
unchanged), `…_UnreachableOriginChangesNothing` (exit 3), `…_RejectedPushIsUnconfirmedThenConfirms`
(bare `origin` with a `pre-receive` that exits 1: exit 6 and the local tree clean; hook removed,
re-run: exit 0 and `git --git-dir origin.git grep <string> main -- ops` finds nothing),
`…_QuarantinedCopyIsRewritten`, `…_HelpSaysHistoryIsNotRewritten` (`act help redact` contains
`does not rewrite git history`; the overview lists the topic).
`TestDocClaim_ErrorCodes_HelpAndSpecListExactlyTheEmittedSet` passes with the three new codes.

### U4 — the index and derived files are deleted on redaction

Code: `internal/fold/signature.go` (`Signatures.RedactKey`), `internal/index/index.go`
(`ScrubDerived`, the `redact_key` row in `Rebuild`, the comparison at the top of `EnsureCurrent`),
`internal/cli/redact.go` (step 7 calls `ScrubDerived`).

Docs: spec `index.db` schema paragraph (`index_state` has two keys; what `redact_key` is for) and
the `act redact` section's "other checkouts" paragraph.

Accept: `go test ./internal/index -run 'TestRedactKey|TestScrubDerived' -v` passes —
`TestRedactKey_ChangeRecreatesTheIndexFile` (create a sentinel table through `idx.DB()`, add a
`…-redact.json` file under ops, `EnsureCurrent`: the sentinel table is gone and `redact_key` is
set; a second `EnsureCurrent` rebuilds nothing), `TestRedactKey_NoRecordsNeverScrubs`,
`TestScrubDerived_RemovesExactlyTheDerivedSet`. `go test ./cmd/act -run
TestDocClaim_Redact_SecondCheckoutConverges` passes: B cloned with `act state import --from-remote`
and read once while the string is the current description (the test first asserts B's `index.db`
contains it, so the check cannot pass vacuously); A redacts; B runs `git -C .act pull --rebase`
then `act list --no-fetch`; no file under `B/.act` outside `.git` contains the string, and
`act list --json --no-fetch | jq -S .issues` and `act show <id> --json --no-fetch | jq -S
'del(.refresh)'` are identical in A and B. Then the script of §10.6 exits 0.

### U5 — doctor's `redaction` check and the harvest guard

Code: `internal/cli/doctor.go` (check `redaction`, findings `redaction-leftover` /
`redaction-conflict`, `--fix` for the first; registered as fold-dependent),
`internal/cli/harvest.go` (the guard of §6.2, shared by `act state export`).

Docs: spec doctor table row and §7.6 list, `act doctor` flag text for `--fix` ("drives
`index-divergence`, `index-schema`, `nested-exclude` and `redaction`"), harvest/state-export skip
reasons; `helpOpsModel` DOCTOR paragraph, two sentences.

Accept: `go test ./internal/cli ./cmd/act -run 'Redaction|Redact_Harvest' -v` passes —
`TestDoctor_RedactionLeftover_PositiveAndNegative` (copy the pre-redaction file back into `ops/`
and commit it: `act doctor` exits 1 with one `redaction-leftover` finding and `act show` still
renders `[redacted]`; `act doctor --fix` exits 0, the file is gone from the tree and from `HEAD`,
a second `act doctor` reports 0 findings), `TestDoctor_RedactionConflict_PositiveAndNegative`,
`TestDocClaim_Redact_HarvestSkipsSupersededFiles` (worker copy made before the redaction; after it,
`act state export <worker> --json` lists the old file under `skipped_ops` with reason
`superseded_by_redaction`, and the host tree does not contain the string).

### 10.6 Ticket acceptance script

Run from the repo root after `go build -o bin/act-dev ./cmd/act`; prints `PASS` and exits 0 only if
all four ticket criteria hold. The last check asserts the stated limit, not a goal.

```bash
set -euo pipefail
ACT=$PWD/bin/act-dev; R=$(mktemp -d); export XDG_CONFIG_HOME=$R/xdg; unset ACT_TRACKER_REMOTE
S=PLANTED-4417-9902-7731
mk() { mkdir "$1"; git -C "$1" init -q .; git -C "$1" commit -q --allow-empty -m init; }
has() { grep -rqa --exclude-dir=.git "$S" "$1"; }
mk $R/A; cd $R/A; $ACT init >/dev/null
git init -q --bare $R/origin.git; git -C $R/A/.act remote add origin $R/origin.git
git -C $R/A/.act push -q -u origin main
ID=$($ACT create "Statement for $S" --description "Wire from account $S." --json | jq -r .id)
$ACT update $ID --description "Wire from account $S to savings." >/dev/null   # create AND a later update hold it
mk $R/B; cd $R/B; $ACT state import --from-remote $R/origin.git $R/B >/dev/null; $ACT list >/dev/null
has $R/B/.act                                                # premise: B holds it
cd $R/A; printf '%s\n' "$S" > $R/patterns
$ACT redact --patterns-file $R/patterns --json > $R/out.json 2> $R/err.txt
jq -e '.ok and .ops_rewritten==2 and .replacements==3 and .published' $R/out.json >/dev/null
! grep -q "$S" $R/out.json $R/err.txt                        # (3) counts only
! has $R/A/.act/ops; ! has $R/A/.act                         # (1) op files, and everything else outside .git
$ACT show $ID | grep -qF 'Wire from account [redacted] to savings.'      # (1)
$ACT doctor >/dev/null                                       # (1) exit 0
cd $R/B; $ACT ready --fresh >/dev/null                       # B syncs
! has $R/B/.act                                              # (2)
diff <(cd $R/A; $ACT list --json --no-fetch | jq -S .issues) <(cd $R/B; $ACT list --json --no-fetch | jq -S .issues)
diff <(cd $R/A; $ACT show $ID --json --no-fetch | jq -S 'del(.refresh)') <(cd $R/B; $ACT show $ID --json --no-fetch | jq -S 'del(.refresh)')
cd $R/A; set +e; $ACT redact "$S" > $R/pos.txt 2>&1; rc=$?; set -e
test $rc -eq 2; ! grep -q "$S" $R/pos.txt                    # (3) never on the command line
$ACT help redact | grep -qF 'does not rewrite git history'   # (4)
$ACT help | grep -qF 'act help redact'                       # (4)
git --git-dir $R/origin.git log --oneline -S"$S" -- ops | grep -q .   # the limit: history still has it
echo PASS
```

## 11. Follow-ups

- **Retiring an id.** If the nested repo's history is ever rewritten, decide then whether tickets
  whose create op held a secret get new ids (§8 item 4); that needs a purge-and-recreate operation
  this design does not have.
- **`--superseded FIELD`** (§3): the no-pattern mode, as a second `Apply` over the same plan.
- **Span-recording merge** (§6.3) and **an automatic leftover sweep** (§6.5): each has its revisit
  trigger stated where it was rejected.
- `internal/cli/cache.go` (`MaybeRefresh`) removes `index.db` after a rebase but leaves
  `index.db-wal` / `-shm` beside the fresh database. Harmless when the last handle closed cleanly;
  worth removing all four the way `ScrubDerived` does.
- Spec drift seen while reproducing §1: the spec says op files are indented multi-line JSON and
  that the create payload stores `full_id`; real op files are single-line canonical JSON with no
  `full_id`. If `full_id` is ever written, `Apply` must drop it — it is a full hash of the original
  create payload.
- Both machines should run a build with U1 before the first redaction: an older binary folds a
  re-issued op correctly but ignores the slot rule, never rebuilds its index from scratch, and
  cannot report a leftover.

## 12. Asks

None. Every call above is technical and can be revised in a later build. The one thing that is
Andrew's — whether the nested repo's history is rewritten — is already held elsewhere and is out of
scope here; §8 states what stays readable until then, including the id fingerprint that only
matters once history is gone.
