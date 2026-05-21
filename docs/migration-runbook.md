# act Phase 1 migration runbook

One-shot migration from the legacy single-repo `.act/`-in-host layout to
the Phase 1 nested-repo layout. Required reading before running
`act migrate-to-nested` on any repo that already has `.act/` checked in
to the host.

For the design context, see `docs/coordination-plane-design.md` v2.1
(Phase 1 delta item 6, "Migration").

## What changed

**Before (legacy).** Each act repo had a single `.act/` directory tracked
by the host git repo. Every op (claim, close, add_dep, etc.) produced an
`act-op: (act-XXXX) <type>` commit in the host's log — heavy bookkeeping
noise on every PR diff.

**After (Phase 1, nested-repo).** `.act/` has its own `.git` directory.
Op commits land in the nested repo; the host repo's log only sees
human-meaningful work commits. Work commits embed an `Act-Id: act-XXXXXX`
trailer in the commit body (a body trailer, not a subject suffix —
trailers are ignored by conventional-commit linters, semantic-release,
and CHANGELOG generators, which is what makes the host log "outside
contributors see exactly the code"-clean).

Three additional host-side artifacts ship with the migration:

1. `.act/` is added to host `.gitignore` so future op writes don't
   accidentally re-track.
2. A host pre-commit hook (`.git/hooks/pre-commit`) rejects any commit
   that stages a `.act/*` addition or modification. Belt-and-suspenders
   against accidental re-tracking.
   Staged deletions of `.act/*` are permitted so the migrate-to-nested
   untrack commit (and manual `git rm -r --cached .act/` carries to
   sibling branches) succeed under a normal `git commit` even when the
   hook is already installed.
3. When the host has a public-looking remote (github/gitlab/bitbucket),
   `CONTRIBUTING.md` gets an `Act-Id:` trailer explainer for external
   contributors who shouldn't need to learn the convention.

## Steps

One command, idempotent:

```
cd <your-repo>
act migrate-to-nested
```

Output on a successful migration enumerates what landed. The command:

1. Verifies `.act/.git` doesn't already exist (idempotency gate).
2. `git init -b main` inside `.act/` and commits every existing op file
   as the initial commit. Pre-migration ids stay reachable from the
   nested repo's history — doctor's marker scan reads both sides.
3. `git rm -r --cached .act/` in the host repo to un-track existing
   op files. Files stay on disk; only the index is touched.
4. Appends `.act/` to host `.gitignore` (idempotent — line-match
   trim-space).
5. Installs the host pre-commit hook (or augments an existing one).
6. Appends the `Act-Id:` stanza to `CONTRIBUTING.md` if applicable.
7. Commits the host-side changes (`.gitignore`, untrack, CONTRIBUTING)
   as a single commit with subject
   `act migrate: untrack .act/ from host, set up nested-repo + pre-commit hook + CONTRIBUTING`.

Verify with `act doctor --check nested-layout` — should report zero
findings. Either `act doctor` (default set) or `act doctor --strict` is
the CI-friendly form.

## Nested-git pain points (acknowledged)

Real interactions the migration introduces. Document for your team
before turning the lights on.

### IDE source-control views

VSCode, JetBrains, and similar IDEs auto-detect nested `.git/`
directories and may present competing source-control panels — one for
the host, one for `.act/`. Two mitigations:

- Tell developers to ignore the `.act/` SCM view; agents own the
  nested repo, humans should treat it as opaque state.
- Add `.act/` to your editor's watch-exclusions if the dual-panel UI
  is annoying. Example for VSCode (`.vscode/settings.json`):
  ```
  {
    "files.watcherExclude": { "**/.act/**": true },
    "git.ignoredRepositories": ["${workspaceFolder}/.act"]
  }
  ```

### `git clean -fdx` will delete `.act/`

`-x` adds gitignored files back into the scope of `git clean`. Running
`git clean -fdx` in the host repo will destroy the entire nested `.act/`
including its `.git` directory.

Recovery under Phase 1 (no remote yet — that's Phase 2): restore from
filesystem backup or re-import from a sibling clone of the same repo.
The op-log is essentially the source of truth; if you've lost it without
backup, you've lost the act state and will need a fresh `act init`.

Mitigation: avoid `git clean -fdx`. `git clean -fd` (without `-x`)
respects gitignore and is safe. If you find yourself needing `-x`,
exclude `.act/` explicitly: `git clean -fdx --exclude=.act`.

### Worktrees

Each `git worktree` of the host repo gets a **separate** working tree.
Because `.act/` is gitignored, the nested act state is **per-checkout**
— a new worktree starts with an empty `.act/` (or no `.act/` at all)
and operates as a fresh act install unless you explicitly seed it.

This is usually what you want for sub-agent isolation: agents in
separate worktrees claim independently and don't race on `.act/index.db`.
The agent CLAUDE.md for this repo mandates `isolation: worktree` for
sub-agents and this is what makes that safe.

When sub-agents need to share claim state with the parent (rare — only
for explicitly-coordinated swarms), point them at the parent's `.act/`
directly. Today that means running act commands from the parent
checkout's path; a future `--act-state-path` plumbing is filed.

### `rg` / `grep` default behavior

`rg` and `git grep` respect `.gitignore` by default, so they'll skip
`.act/` when run from the host repo. For most agent work this is
correct — agents don't search their own op log when working on host
code.

When you do need to search `.act/` (debugging act itself, archaeology
on op envelopes): `rg --no-ignore .act/` or `rg -uu .act/`.

### Submodule-shaped confusion

`.act/` is NOT a git submodule, despite having its own `.git`. There is
no `.gitmodules` entry, no submodule tracking, no `git submodule update`
needed. Treating it as a submodule (e.g. running `git submodule add` on
it) will create a real submodule entry that conflicts with the nested-
repo design.

If a developer asks "is `.act/` a submodule?", the answer is no — it's a
nested git repository that the host repo gitignores. Two completely
separate things git can do; we're using one and not the other.

## Verification

After running `act migrate-to-nested`, three checks confirm a complete
migration:

```
act doctor --check nested-layout
# zero findings on success
```

```
git -C .act log --oneline | head -1
# should show "act init: nested act state bootstrap (migrated from host-tracked .act/)"
```

```
git ls-files -- .act/
# should print nothing — no .act/ paths tracked by host
```

For an end-to-end shake-down on a freshly migrated repo, run one
canonical loop iteration:

```
act ready
act update --claim <some-issue>
# work
act close <some-issue> --reason "verify post-migration loop"
git commit -a -m "verify" -m "Act-Id: act-XXXXXX"
git push
git log -1 --format=%B  # confirm Act-Id trailer in body
```

No `act-op:` commits should land in the host log going forward.

## Downstream-repo recipe

For each downstream repo that uses act (inbox-triage, aac-website, sift,
poke, ask, and any others Andrew picks up): same one-liner.

```
cd <repo>
act migrate-to-nested
act doctor --check nested-layout   # verify
```

If `act doctor --strict` is enabled in the repo's CI, that will pick up
any partial-migration anomaly automatically going forward.

## Phase 1.5 → Phase 2 cutover

Phase 1.5 runs the dispatcher on copy-based bootstrap: the orchestrator
seeds each worker's `.act/` with `act bootstrap-worker <path>` and runs
`act harvest <path>` at teardown. Phase 2 keeps that path as a fallback
but adds a push-attached delivery channel — workers push ops directly
to the orchestrator's `.act/.git` during execution and the orchestrator
replicates to a configured upstream.

### One-time per-project setup

Run on the orchestrator's checkout, once per project:

```
act remote enable
```

Writes `act.role=orchestrator` into `.act/.git/config`, sets
`receive.denyCurrentBranch=updateInstead` so push-into-checked-out-branch
works, and installs the post-receive hook that invokes `act remote sync`
on every push.

Optional, for upstream replication:

```
act remote add-upstream <url>
```

Wires the upstream remote that the orchestrator's post-receive hook
syncs to after each re-fold. Refuses public URLs unless `--force-public`
is passed (see `docs/spec-v2.md` for the `upstream_public` error code).

### Cutover order

1. **Enable on the orchestrator first.** Run `act remote enable` (and
   optionally `act remote add-upstream`) on the orchestrator's checkout.
   Existing Phase 1.5 workers continue to operate unchanged — the
   copy-source bootstrap is still wired and their commits still land
   via harvest at teardown.
2. **New dispatches use `--from-remote`.** Future passes seed workers
   with `act bootstrap-worker --from-remote <url> <path>`. The
   `<url>` is the orchestrator's `.act/.git` path (or a network URL if
   you've wired one). Workers push to the orchestrator during
   execution; no per-cycle harvest is needed.
3. **Mixed waves are safe.** Phase 1.5 workers (copy-source) and
   Phase 2 workers (push-attached) can coexist in the same pass. The
   orchestrator's view converges on the same fold either way.

### Rollback

```
act remote disable
```

Removes the post-receive hook file and unsets `act.role`. New workers
fall back to Phase 1.5 copy-source bootstrap automatically; in-flight
Phase 2 workers will fail their next push and surface a clean error,
recoverable by harvesting them via the fallback path. `act remote
disable` is idempotent — running it twice exits zero both times.

### Common failures and recovery

- `bootstrap_timeout` — the worker's initial clone from `<url>` exceeded
  the timeout. Extend `act.bootstrapTimeoutSeconds` in
  `.act/.git/config` (default is conservative for local-disk URLs;
  remote network URLs may need 30s+).
- `remote_unreachable` — the worker can't reach the orchestrator's
  `<url>`. Verify the URL is correct (filesystem path or network), that
  the orchestrator is up, and that no firewall is dropping the
  connection. For filesystem URLs, confirm the path exists and the
  worker process has read access.
- `target_not_empty` — the worker's `<path>` already has a `.act/`. Use
  `--force` to overwrite (acceptable when the prior bootstrap failed
  mid-stream and left a partial tree) or dispatch to a fresh path.
- `push_exhausted` — surfaced when the worker's commit-time push to
  the orchestrator retried past the configured budget. Rare; usually
  means another worker held the receive lock for an unusual length of
  time. Inspect `.act/.sync-log` on the orchestrator for the
  contention timeline. The worker's commit is local-only at this point;
  a subsequent `act harvest <path>` will pick it up.
