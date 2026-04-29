---
title: "Canonical JSON serialization"
deps: [act-9cad]
acceptance_criteria:
  - "`canonicaljson.Marshal(map[string]any{\"b\":1,\"a\":2})` returns bytes equal to `{\"a\": 1,\\n  \"b\": 2}` is INCORRECT — actual expected output for the nested form is `{\\n  \"a\": 2,\\n  \"b\": 1\\n}` with two-space indent and LF newlines, no trailing newline."
  - "Top-level op envelopes preserve the fixed key order `op_version, schema_version, writer_version, op_type, issue_id, node_id, hlc, payload`; nested objects are sorted lexicographically by Unicode code point."
  - "Strings escape exactly `\\\"`, `\\\\`, `\\b`, `\\f`, `\\n`, `\\r`, `\\t`, and emit `\\u00xx` for other C0/C1; non-ASCII printable code points emitted raw UTF-8."
  - "Output ends WITHOUT a trailing newline; empty arrays/objects render as `[]` / `{}` on a single line."
  - "No floats are accepted: `Marshal` returns an error if any number in the input is non-integer."
  - "Round-trip determinism test: for 10 000 random envelopes, `Marshal(Unmarshal(b))` returns bytes equal to `b`."
  - "`act fmt-op` subcommand reads any op file and rewrites it through `Marshal`; run twice in a row, the second invocation produces no diff."
status: open
created_at: 2026-04-29T00:00:00Z
---

# Canonical JSON serialization

## Context
The spec (§Op envelope, byte-exact JSON serialization rules) makes
byte-identical op files a hard requirement: fold determinism, op_hash
computation, and `act doctor --check op-canonical` all depend on a single
canonical encoding. Standard `encoding/json` cannot deliver this because it
neither preserves a custom top-level key order nor sorts nested keys
deterministically with the required escape rules.

## Scope
- New package `internal/canonicaljson` with:
  - `Marshal(v any) ([]byte, error)` — top-level envelope writer that honors
    the fixed envelope key order when given an `*op.Envelope`-shaped struct,
    and lexicographic order otherwise.
  - `MarshalPayload(v any) ([]byte, error)` — same rules, lex order at the top.
  - `Unmarshal(b []byte, v any) error` — strict parser that rejects unknown
    keys and non-integer numbers.
- A small `act fmt-op <path>` CLI surface (single command, no flags) that
  reads, marshals, writes back; used in tests and doctor.

## Out of scope
- The op envelope struct itself (act-ba09).
- Integration with the writer pipeline (act-6ec9).
- Doctor's `op-canonical` check wiring (act-40ae).

## Implementation notes
- Indent: two spaces per nesting level. LF newlines (not CRLF). No trailing
  newline.
- Whitespace exactly: `: ` after keys, `,\n` between siblings, `\n` after
  any `{` or `[` that has children. Empty containers stay on one line.
- Numbers: only `int64` / unsigned variants accepted; floats produce
  `ErrFloatNotAllowed`.
- Escape table: per RFC 8259 short escapes for the listed control chars; all
  other code points <0x20 and 0x7F-0x9F as `\u00xx` (lowercase hex);
  everything else verbatim UTF-8.
- Provide `WriteCanonical(w io.Writer, v any) error` to avoid double-buffering
  large payloads.

## Test plan
- Table-driven tests covering: nested key order, escape edge cases (NUL,
  DEL, BOM in a string, surrogate pairs), empty containers, integer
  bounds (`int64` min/max), unicode (e.g. `"ümlaut"` round-trip).
- Property test: random JSON-compatible Go values, assert
  `Marshal -> Unmarshal -> Marshal` is byte-identical.
- Golden-file test: a fixed envelope vector under `testdata/canonical/`
  with checked-in expected bytes; failing this is a release blocker.
- Negative test: any `float64`, `NaN`, `+Inf` returns `ErrFloatNotAllowed`.
