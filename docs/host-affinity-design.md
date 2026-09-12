# Host affinity: a ticket says which machine it can run on

Design brief, 2026-09-12, mini. Arc id: `arc-host-affinity`. Mode at filing:
autonomous. Right-sizing: **subset (refactor-no-plan-review)** — design +
design reviews + design synth + implementation fanout + dogfood + compound, no
plan stage. Justification: five real design calls (where the constraint lives,
how a host identifies itself, default-on vs opt-in exclusion, what the wrong
host is told, which callers change) and a high blast radius in one direction —
a wrong default silently hides real work from every drain on both machines —
but the implementation is a well-trodden path in this codebase (one scalar
field through create → op payload → fold → index → ready), so a separate plan
gate and a second review round would be ceremony.

## 1. The gap

`act ready` answers "what is ready", and every consumer reads that as "what
this session can pick up". Those are different questions whenever a ticket can
only be done on one machine, and the fleet runs on two.

Measured, 2026-09-11 on the mini: `quota-floor` launched 14 `/orchestrate`
drains and 11 published `nothing-came-of-it`. All 11 went to `mac-mini-setup`,
whose entire ready set — seven tickets — needs the laptop. Eleven captains
surveyed, found nothing they could dispatch, and exited. Each cost a session
boot. Evidence: `claude-config` act-b4f12f (twelve dated launch records in its
body) and `knowledge/projects/anthropic-role/pd-metrics-recut-2026-09-12.md`.

Reproduced tonight, unchanged:

```
$ cd ~/Workspace/mac-mini-setup && act ready
act-127ab1 1 - - [LAPTOP-ONLY] Migrate Cowork scheduled tasks off the laptop ...
act-546c79 2 - - [LAPTOP-ONLY] act-sync tick: execute the recorded recommendation ...
act-04dc76 2 - - [LAPTOP-ONLY] Interactive laptop act ops still depend on ssh-agent ...
act-e6faef 2 - - [MINI-LIVE + LAPTOP] The reach registry is machine-local ...
act-0ea39f 2 - - [LAPTOP-ONLY] Inventory the Claude Desktop scheduled routines ...
act-267ca2 2 - - [LAPTOP-ONLY] Inventory the laptop's scheduled launchd jobs ...
act-f548d8 3 - - [LAPTOP-ONLY] Confirm the laptop has no reader of the retired July setup token ...
```

Those `[LAPTOP-ONLY]` prefixes are the ad-hoc convention this feature replaces.
They are a human-readable *hint* that a machine has to sniff out of prose.

**State of the art as of tonight.** `claude-config` commit `7136fcd`
(2026-09-11 22:13) taught `quota-floor` to discount them with a regex:
`_MACHINE_WORD = re.compile(r"(?<![A-Za-z])(MINI|LAPTOP)(?![A-Za-z])")`, applied
to the title, capitals only. It works — a dry pick that night skipped
`mac-mini-setup` and chose `financial` — and act-b4f12f was closed on it. It is
also exactly the wrong home for the rule, for three reasons:

1. **It fixes one caller.** `/orchestrate` reads `act ready` directly
   (`~/.claude/commands/orchestrate.md` line 47) and still sees every laptop
   ticket as ready on the mini. So does every human and agent who types
   `act ready`. A second caller would need a second copy of the regex — the
   copy-paste failure `rules/02` names.
2. **It reads prose as a contract.** `rules/09`: a human-facing string offers
   no stability contract. A retitle, a lowercase "laptop", a new machine name,
   and the routing silently changes with nobody considering it a breaking
   change.
3. **It is invisible where the decision is made.** The person filing the ticket
   knows which machine it needs. Nothing at filing time records it; the
   knowledge survives only as capital letters someone downstream might grep.

The constraint is a property of the ticket. It belongs on the ticket.

## 2. What this must do

Andrew's constraints, verbatim in substance:

- **Anything that CAN run on the mini MUST run on the mini**, so the default
  for an untagged ticket is "runs anywhere". No migration may change the
  meaning of an existing ticket.
- The host constraint is **a property of the ticket, set at filing time**.
- A session learns **its own host from the machine it is on** — never from a
  flag a caller has to remember.
- `act ready` / `act next` on the wrong host **exclude the ticket and say why,
  visibly to a listing reader**, without deleting or hiding it from
  `act list` / `act show`.
- `quota-floor` and `/orchestrate` **count only what the current host can take**.

And one constraint of act's own (AGENTS.md): every user-visible claim needs a
`TestDocClaim_*` asserting it at the user-visible boundary, registered in
`internal/cli/docs_sweep_test.go`, landing in the same commit.

## 3. The design

### 3.1 The field: one scalar `host` on the issue

A new issue field, `host`, a free-form label. Empty (the default, and what every
existing issue folds to) means **runs anywhere**. Set means **runs only on the
machine whose label matches**, case-insensitively.

Surface:

```
act create --host laptop "…"          # pin at filing time
act update <id> --host laptop         # pin later
act update <id> --host ""             # unpin — back to "runs anywhere"
```

Mechanically it is one more entry in `validUpdateFields` (`internal/op/payloads.go`),
one more optional key on `CreatePayload`, one more column on the `issues` table,
one more field on `index.Row`, `ReadyIssue` and `ShowResult`. It rides act's
existing last-write-wins fold with no new op type and no new table, and old ops
fold to empty, so no migration op is needed and no existing ticket changes
meaning.

Validation at write time, mirroring `validateExternalRef`: non-empty when set,
≤64 bytes, printable ASCII without whitespace or control characters. Comparison
is on the lowercased value.

**Rejected: a set of hosts** (`--host mini,laptop`). It expresses nothing extra
today — the fleet has two machines, so a two-element set is exactly "runs
anywhere" — and it costs either two new op types (add/remove, like deps) or
list-replacement semantics `update_field` does not have. Revisit when a third
machine exists and a real ticket runs on two of the three.

**Rejected: a general label/tag subsystem.** act has none. Building one to
express a single concept is the "new component that only handles the failure
that prompted it" smell (`rules/02`). Revisit if a second orthogonal tag
dimension shows up.

**Rejected: an external dep (`--ext-add machine:laptop`).** Wrong semantics: an
open external dep makes the ticket un-ready *everywhere*, including on the
laptop where it is perfectly workable.

**Rejected: keeping the title convention and parsing it in act.** Same three
objections as §1, plus it would make a retitle a routing change.

**Two-machine tickets are not modelled.** `act-e6faef` is titled
`[MINI-LIVE + LAPTOP]`. No single host can complete it, so no value of a
single-host field is honest. The right move is the ticket author's, not the
schema's: pin it to the machine that owns the drainable half, or split it into
two tickets with a `blocks` edge. The skill will say so. Building an "and"
semantics for one ticket would be over-building.

### 3.2 Host identity: resolved from the machine, overridable, never from a caller flag

`act host` prints this machine's label **and where it came from**. Resolution,
in order:

1. `$ACT_HOST`, if set and non-empty.
2. `$XDG_CONFIG_HOME/act/host` (default `~/.config/act/host`), first line,
   trimmed.
3. The short hostname — `os.Hostname()` up to the first `.`, lowercased.

`act host --set <label>` writes layer 2 and prints the result, so configuring a
machine is one command rather than a chore handed to a person.

Three things make this the right shape:

- **Per machine, not per store.** There are 29 `.act/` stores on the mini.
  A label in `.act/config.json` would be 58 edits across two machines and would
  drift. One file per machine cannot drift from itself.
- **XDG, not a bespoke location.** `rules/02`: ground tool conventions in an
  external standard rather than in sibling-tool coordination.
- **No Andrew-specific mapping ships in act.** `quota-floor`'s `this_machine()`
  hardcodes `"mini" in h` / `"macbook" in h`. A shipped CLI must not carry one
  operator's fleet (the shippable-generic rule). act ships the mechanism; the
  operator supplies the label.

Measured tonight: the mini's `hostname -s` is literally `mini`, so layer 3
already produces the right label here with no configuration. The laptop's short
hostname is not known from the mini and is probably not `laptop`, so the laptop
needs `act host --set laptop` as a one-time step. That is the single
laptop-side action this whole change requires.

### 3.3 Exclusion is the default, and it is loud

`act ready` and `act next` exclude, by default, any issue whose `host` is set
and does not match this machine's label.

**Default-on, not opt-in**, for one decisive reason: `/orchestrate` is
tracker-agnostic and calls `act ready` with no act-specific flags. Default-on
fixes it with zero changes to the command, the skill, or any future caller.
Opt-in would fix only the callers someone remembers to update, which is the
failure this change exists to end.

Every excluded row is reported, on **stderr, in both human and `--json` mode** —
the placement act already uses for the truncation notice and the refresh
warning, and for the same reason (`--json | jq` swallows stdout; anything added
to stdout corrupts the row stream):

```
act ready: 7 ready issues are pinned to another machine and were excluded.
  this host: "mini" (from hostname)
  show them with: act ready --all-hosts
```

The line **names the resolved label and its source every time**. That is the
guard against the one catastrophic failure mode of this feature: a
misconfigured label matches nothing, so every pinned ticket vanishes on every
machine and the fleet quietly stops seeing real work. `rules/09` — differentiate
on cause. A reader who sees `this host: "andrews-mbp" (from hostname)` next to
7 excluded rows diagnoses it in one glance.

`--json` additionally carries a machine-readable count:

```json
{"ready": [...], "count": 3, "total": 3, "elsewhere": 7, "truncated": false}
```

`elsewhere` is the pre-exclusion count of rows dropped for host mismatch. It is
what `quota-floor` consumes. It is **not** `omitempty`: an absent key must mean
"this act does not know about hosts", not "zero were excluded" — the
consumer-side distinction `rules/09` insists on, and the one that lets
`quota-floor` tell a new binary from an old one.

**Escape hatch: `act ready --all-hosts`** (and `act next --all-hosts`) returns
everything, unfiltered. Under `--all-hosts` a pinned row renders its label in
the human line so the listing is self-explanatory:

```
act-127ab1 1 - - @laptop Migrate Cowork scheduled tasks off the laptop
```

The `@label` sits between the claimed-at column and the title, is present only
for pinned rows, and only under `--all-hosts` — the default view's format is
byte-identical to today's, because things parse it.

**Rejected: `act ready --host <label>`** ("what would be ready on the laptop?").
`--all-hosts` plus `act list` answers it, and a second flag on the hot path is
not worth it. Revisit if a laptop-dispatching session on the mini actually
needs it.

`act list` and `act show` are untouched in what they include — Andrew's
constraint. `act show` gains a `host:` line and `list --json` a `host` key, so
the property is visible where you go to read a ticket.

### 3.4 Callers

| caller | change |
|---|---|
| `/orchestrate` (command + skill) | **none.** It calls `act ready`; default-on exclusion fixes it. |
| `act next` | inherits exclusion via `RunReady`; emits the same note. |
| `quota-floor` `ready_count()` | read `elsewhere` from the JSON instead of regexing titles. Keep the ledger note. |
| act skill (`skills/act/SKILL.md`) | filing guidance: pin at filing time; default unpinned; how two-machine work is expressed. |
| `mac-mini-setup`'s 7 tickets | `--host laptop`, then drop the `[LAPTOP-ONLY]` / `[MINI-LIVE + LAPTOP]` title prefixes. |

**The `quota-floor` boundary needs care.** It resolves its own act binary
(`QUOTA_FLOOR_ACT_BIN`), and the laptop's act will be the old build until
someone installs there. So:

```
elsewhere present  →  trust it; the regex is dead code for this store.
elsewhere absent   →  old act: fall back to the title regex, exactly as today.
```

An absent key is not zero. The ledger note keeps its current wording shape so
`grep -c deficit_positive_headroom_no_launch` and the rest of the ledger's
grep-ability are unaffected; only the *source* of the discount changes.

Stripping the title prefixes is safe only **after** `quota-floor` reads the
field, and is safe on the laptop regardless: these tickets are runnable there,
so nothing excludes them whatever the old binary parses. Order is therefore:
set the field → land `quota-floor` → strip the prefixes.

### 3.5 What is deliberately not built

- No `act doctor` check for "this store has pins matching no known host". The
  stderr note already surfaces the misconfiguration at the moment it bites, and
  act cannot know the fleet's machine list. File it if the note proves too
  quiet in practice.
- No migration op. Empty = anywhere is the correct reading of every existing
  op.
- No host validation against a roster. act does not know the fleet; a typo'd
  `--host laptpo` surfaces as "excluded everywhere, this host: mini", which the
  note makes legible.

## 4. Open questions

**Resolved here.** Scalar vs set (scalar). Default-on vs opt-in (default-on).
Where host identity lives (XDG file + env, not per-store config). Whether
`list`/`show` change (show gains a line, list gains a JSON key, neither filters).

**Remaining, for the reviewers.**

1. Is default-on exclusion right, given that its worst failure — a
   misconfigured label hiding work fleet-wide — is silent-ish? Is the stderr
   note enough, or does this need a harder guard?
2. Is `elsewhere` (a count) the right contract for `quota-floor`, or should the
   excluded rows themselves be available in JSON so a caller can say *which*?
3. Is `@label` in the `--all-hosts` human line worth the format change, or
   should the default view's format be the only one?
4. Does anything else in the fleet read `act ready`'s human output positionally
   in a way the `@label` column would break?

## 5. Provenance of the decisions above

Recovered/settled before this brief: the default must be "runs anywhere"; host
identity comes from the machine; `list`/`show` must not filter; `quota-floor`
and `/orchestrate` must count only local work (all Andrew, in the dispatch).
Author-proposed here: the scalar field and its rejected alternatives, XDG host
resolution with `act host [--set]`, default-on exclusion, the stderr note's
contents, `elsewhere` as the JSON contract and its absent-vs-zero rule,
`--all-hosts`, and the ordering constraint on stripping title prefixes.
