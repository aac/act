# act build DAG

Topologically ordered. Each entry: `<id> [status] <title>` with deps in parens.

The DAG is the source of truth: phases are for human readability, dependencies are encoded in the `deps:` parens. Leaf dependencies appear first; every entry's deps are guaranteed to appear earlier in this file.

## Phase 0: Foundations

- act-8411 [closed] Project skeleton and repo layout (deps: -)
- act-9cad [closed] Go module and minimal CI (deps: act-8411)

## Phase 1: Core primitives

- act-b545 [closed] Canonical JSON serialization (deps: act-9cad)
- act-9cae [closed] Hybrid Logical Clock implementation (deps: act-9cad)
- act-1396 [closed] On-disk layout and .act/config.json (deps: act-9cad)
- act-bd70 [closed] ID generation and nonce protocol (deps: act-b545, act-1396)
- act-ba09 [closed] Op envelope schema and validation (deps: act-b545, act-9cae, act-bd70)
- act-3bbe [closed] Op type payloads and write-time validation (deps: act-ba09)
- act-6ec9 [closed] Op file naming and shard probe (deps: act-ba09, act-1396)
- act-6991 [closed] Shortest-unique-prefix display and resolution (deps: act-bd70, act-1396)

## Phase 2: Fold engine

- act-9362 [closed] Op-fold algorithm core (deps: act-3bbe, act-9cae, act-6ec9)
- act-c9f0 [closed] Per-op-type apply functions (deps: act-9362)
- act-296e [closed] Per-field LWW with status/accept/deps exceptions (deps: act-c9f0)
- act-9824 [closed] Atomic claim protocol (deps: act-296e)
- act-a1f6 [closed] Fold checkpoint (deps: act-296e)
- act-912f [closed] SQLite index schema and rebuild (deps: act-296e, act-a1f6)

## Phase 3: Auto-commit, hooks, schema migration

- act-5ca9 [closed] Auto-commit and push policy (deps: act-6ec9, act-9824)
- act-ce9f [open] Hooks runtime contract (deps: act-c9f0, act-5ca9)
- act-5af9 [closed] Op-schema migration (deps: act-3bbe, act-c9f0)

## Phase 4: CLI commands

- act-b0b9 [closed] act init command (deps: act-1396, act-9cae)
- act-65e6 [open] act create command (deps: act-bd70, act-3bbe, act-5ca9, act-912f, act-ce9f)
- act-5bf7 [open] act list command (deps: act-912f, act-6991)
- act-beca [open] act show command (deps: act-912f, act-6991)
- act-5651 [open] act update command (deps: act-3bbe, act-5ca9, act-912f, act-ce9f)
- act-bdc8 [open] act close command (deps: act-3bbe, act-5ca9, act-912f, act-ce9f, act-296e)
- act-03f6 [open] act dep add command (deps: act-3bbe, act-5ca9, act-912f)
- act-e1d4 [open] act ready command (deps: act-912f, act-6991, act-296e)
- act-0a22 [open] act search command (deps: act-912f)
- act-5515 [closed] act log command (deps: act-6ec9, act-6991)
- act-2aa3 [closed] act version command (deps: act-9cad)

## Phase 5: Doctor, bootstrap, compaction

- act-40ae [open] Doctor checks (deps: act-912f, act-9824, act-296e, act-5ca9)
- act-6eff [open] Bootstrap importer (deps: act-65e6, act-5651, act-bdc8, act-03f6)
- act-a0ad [open] Compaction (deps: act-a1f6, act-c9f0, act-5ca9)

## Phase 6: MCP surface

- act-380d [open] act mcp server scaffold (deps: act-65e6, act-5651, act-bdc8, act-e1d4, act-beca, act-5bf7, act-0a22, act-5515, act-03f6)
- act-2f81 [open] MCP composed tools act_next act_finish act_block (deps: act-380d, act-9824, act-40ae)

## Phase 7: Tests and release

- act-a64e [open] Property tests and fuzzer (deps: act-9362, act-296e, act-9cae, act-b545)
- act-9b55 [open] Golden tests for fold determinism (deps: act-9362, act-c9f0, act-a1f6)
- act-0c76 [open] Concurrency and rebase contention tests (deps: act-9824, act-5ca9, act-2f81)
- act-2e8d [open] CI matrix for three target environments (deps: act-9cad, act-a64e, act-9b55, act-0c76)
- act-64af [open] Cross-platform builds and release pipeline (deps: act-2e8d, act-2aa3, act-2f81, act-40ae, act-6eff, act-a0ad)
