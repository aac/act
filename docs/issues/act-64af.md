---
title: "Cross-platform builds and release pipeline"
deps: [act-2e8d, act-2aa3, act-2f81, act-40ae, act-6eff, act-a0ad]
acceptance_criteria:
  - "Release pipeline produces signed binaries for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, and windows/amd64"
  - "Each binary's act version --json reports the same binary_version, writer_version, and schema_version as the tag"
  - "Release is gated on all three CI matrix jobs (act-2e8d) being green on the tagged commit"
  - "Tag format is vMAJOR.MINOR.PATCH; pipeline rejects tags that do not match"
  - "Release artifacts include checksums (sha256) and provenance attestation"
  - "Smoke test of each released binary runs init/create/close/list cycle and act doctor against a synthesized repo before publish"
  - "MCP smoke (act mcp tool list with composed tools from act-2f81) runs against each binary"
  - "Importer (act-6eff) and compaction (act-a0ad) smoke runs are part of release verification"
  - "Release notes are auto-drafted from the tagged commit range and require human edit before publish"
  - "Failed release leaves no partial artifacts in the published location"
status: open
created_at: 2026-04-29T00:00:00Z
---

# Cross-platform builds and release pipeline

## Context
Implements the release surface implied by spec-v2 §7.8 (CI matrix gates the merge; humans sign off on the final tag) plus the cross-platform expectations from §"act version" and the writer-version reader gate. This issue lands the build-and-publish pipeline that turns a green tagged commit into signed binaries plus checksums plus a draft release.

## Scope
- Build matrix targets: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`.
- Tag-triggered pipeline: `vMAJOR.MINOR.PATCH` regex; non-matching tags fail fast.
- Per-target build: reproducible Go build with `-trimpath`, `-buildvcs=false` overrides where appropriate, ldflags injecting `binary_version`, `writer_version`, `schema_version` (from act-2aa3).
- Per-target verification:
  - `act version --json` reports the expected versions.
  - Smoke: init → create → claim → close → `list --json` against a fresh tempdir.
  - `act doctor` (act-40ae) on a seeded repo, asserting all 8 checks pass.
  - `act mcp` tool-list including the composed tools from act-2f81.
  - Importer (act-6eff) idempotent run.
  - Compaction (act-a0ad) trigger on a synthesized > 50-op issue.
- Artifact bundling: per-target tarball/zip plus sha256 checksums plus a provenance attestation (SLSA-style or equivalent).
- Release-notes draft: pipeline opens a draft GitHub release with auto-generated commit list; human edits and publishes manually.
- Atomicity: a failed step does not leave partial artifacts in the published location; uploads are either complete or absent.

## Out of scope
- macOS notarization or Windows code-signing certificates (separate ops issue; placeholder hook present).
- Homebrew tap, Scoop manifest, apt/yum repos (downstream packaging; future work).
- Auto-publishing the release (humans push the publish button).

## Implementation notes
- The pipeline reuses the CI matrix container images where possible to keep the build environment consistent with test.
- Reader-gate self-check: each binary refuses to operate on ops with `writer_version > self`; the smoke step exercises this by injecting a too-new op and asserting `version_skew` exit 4.
- Cross-compilation uses `GOOS`/`GOARCH`; cgo is disabled so SQLite (act-912f) must be the pure-Go driver or statically-linked variant — confirm during scaffold.
- Provenance attestation captures the source git sha, the toolchain version, and the workflow run id.
- Release-notes draft groups commits by `act-op:` / `act-import:` / `act-init:` prefixes so the human reviewer sees a structured changelog.

## Test plan
- Tag-format guard: push a malformed tag in a fork; assert pipeline exits 2 with a clear message.
- Per-target smoke: in CI, exercise each target's binary via QEMU (for foreign archs) or a real container (matching arch).
- Reader-gate test: synthesize an op with a future writer_version; assert each binary fails with `version_skew` exit 4.
- MCP smoke: run `act mcp` and assert tools/list contains all 12 1:1 tools plus `act_next`, `act_finish`, `act_block`.
- Atomicity: simulate an upload failure mid-pipeline; assert no artifacts persist at the destination.
- Cite spec §7.8 for the gate that the three matrix jobs must be green on the tagged commit before this pipeline produces artifacts.
