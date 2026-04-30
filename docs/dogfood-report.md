# act v0.1.0 dogfood + usability findings

## Workflow attempted
Built `/tmp/act`, initialized a fresh git tempdir, ran `act init --json`, created three issues with `-p 0/1/2` before the title, added a `dep add` edge (act-7767 blocks act-a9fa), called `ready --json`, claimed act-a9fa with `update --claim --isolated --json`, ran `show` in both modes, closed it with `--reason "done"`, then `log` and `doctor --json`. Also probed an unknown id (`act show act-zzzz`) in both human and JSON modes.

## What worked
- Flag-before-positional parsing works: `act create -p 0 --json "fix bug"` parses cleanly, no "unknown flag after positional" surprise.
- `dep add` prints a nice ASCII edge (`act-7767 --[blocks]--> act-a9fa`), instantly readable.
- `log` output is compact and informative: timestamp, op kind, commit short hash, issue tag, plus a final count line.
- Error envelope on `show --json act-zzzz` is well-formed (`{"error":"issue_not_found","message":...}`) with exit code 3, distinct from success.

## Friction points
1. **`-p 0` is silently ignored.** Command: `act create -p 0 --json "fix bug"`. Expected: created issue has `priority: 0`. Actual: `show --json` returns `priority: 1` (also for `-p 0` on a second test issue act-955d, and for an issue created with no `-p` flag — both come back as 1). Either 0 is being coerced to the default, or default is being applied unconditionally. Severity: **critical** (priority is load-bearing for `ready` ordering and the whole point of `-p`).
2. **`ready --json` orders by something other than priority, and the priority field is wrong anyway.** Even taking the (buggy) priorities as displayed, the ready list returned act-a9fa (p1) before act-13c8 (p2) — fine — but with all priorities collapsed to 1 the user can't tell what the ordering actually is. Severity: **critical** as a consequence of #1, otherwise annoying.
3. **`init --json` envelope is inconsistent with the rest.** `init` returns `{"ok":true,"act_dir":...,"node_id":...}`, `update --claim` returns `{"ok":true,...}`, but `create`/`show`/`ready`/`close`/`doctor` omit `ok` entirely. Severity: **annoying** — scripts have to special-case which commands carry an `ok` field.
4. **`close --json` shape diverges from `show --json`.** `close` returns `{"id","short_id","ops_written":1,"committed":true,"reason":...}` (ops_written as int), but `update --claim` returns `"ops_written":["claim"]` (array of strings). Same field name, different types. Severity: **annoying** (breaks any consumer that types this field).
5. **`doctor --json` returns `"findings":null` instead of `[]` for empty.** Forces every consumer to null-check before iterating. Severity: **nice** to fix.
6. **`show` human output is bare `key: value` lines with no header/blank line, no list of accept criteria when empty, and no indication that the issue is closed beyond a `status:` line.** No emphasis on the headline title. Severity: **nice**.
7. **`show --json` for a closed issue includes `closed_at`/`closed_reason` but `show --json` for an open issue silently omits those keys** rather than emitting `null`. Optional-vs-absent inconsistency. Severity: **annoying**.
8. **No way to discover the current node id from a non-`init` command** — `init --json` exposes `node_id` once and then it's invisible; `update --claim` shows it as `winner` but with a different field name. Severity: **nice**.

## JSON envelope consistency
Mixed. There are at least three shapes in active use: an `{"ok":true,...}` envelope (init, claim), a flat object with no envelope (show, create, close, ready), and a results-keyed wrapper (`{"ready":[...],"count":N}`, `{"findings":...,"count":N}`). Error responses do use a consistent `{"error","message"}` shape with non-zero exit, which is good. Field naming is mostly consistent (`id`/`short_id` everywhere), but `ops_written` is typed as an int in `close` and a `[]string` in `update`, and node identity is `node_id` in `init` vs. `winner` in `update --claim`. A single documented envelope (e.g. `{"ok":bool,"data":{...},"error":...}`) would remove the per-command branching that scripts currently need.

## Composed MCP tools verdict
Reading `internal/mcp/server.go`: `act_next` wraps ready+claim+show with bounded retry, `act_finish` wraps close with the commit-message marker that doctor's orphan-close correlator needs, and `act_block` atomically does update+dep_add in one commit (per §5.D.2). For an agent driving this, those composites are clearly the right call: `act_next` collapses three round-trips and handles the claim-loss race I'd otherwise have to write myself; `act_finish` is the only way to keep doctor happy without me hand-formatting commit messages; `act_block` avoids a torn write between marking blocked and recording the edge. I'd reach for the 1:1 tools only as escape hatches (e.g. bulk update, listing without claiming) — the composites are designed to be the default and the descriptions even say so ("Recommended:" / "Escape hatch:"). The split is correct.

## Verdict
Not ship-quality at v0.1.0: the `-p` priority flag silently dropping (#1) is a critical correctness bug that breaks the ready-set ordering contract. JSON envelope inconsistencies (#3, #4, #5, #7) are fixable in an afternoon and should land before any external consumers script against it.
