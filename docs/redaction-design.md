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
