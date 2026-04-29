---
title: "Go module and minimal CI"
deps: [act-8411]
acceptance_criteria:
  - "`go.mod` declares module path `github.com/<org>/act` and Go toolchain `>= 1.22`."
  - "`go build ./...` succeeds on a fresh clone with no network access beyond module fetch."
  - "`go test ./...` runs and passes (zero tests is acceptable for this issue)."
  - "`go vet ./...` and `gofmt -l .` produce no output."
  - "GitHub Actions workflow `.github/workflows/ci.yml` runs build + vet + test on `ubuntu-latest` for every PR and push to `main`."
  - "CI uses `actions/setup-go@v5` pinned by SHA and caches the module download directory."
  - "CI fails if `go.mod` or `go.sum` would change after `go mod tidy` (drift check)."
status: closed
created_at: 2026-04-29T00:00:00Z
closed_at: 2026-04-29T11:37:40Z
---

# Go module and minimal CI

## Context
Per the spec (§6 Determinism contract, §Tests) determinism and reproducibility
are release blockers; we cannot enforce that without CI from day one. This
issue lights up the build chain so all later phases have a green-baseline to
push against. The full cross-platform matrix is deferred to act-2e8d.

## Scope
- Initialize `go.mod` at the repo root.
- Add a single GitHub Actions workflow `ci.yml` running on `ubuntu-latest`:
  - checkout, setup-go, cache modules, `go build ./...`, `go vet ./...`,
    `gofmt -l . | tee /dev/stderr | (! read)`, `go test ./...`,
    `go mod tidy && git diff --exit-code`.
- Wire the `Makefile` targets from act-8411 to call the matching `go` commands.

## Out of scope
- Linting beyond `gofmt`/`go vet` (no `golangci-lint` yet).
- Multi-OS / multi-arch matrix (act-2e8d).
- Release builds (act-64af).
- Any test code beyond what `go test ./...` discovers as zero packages.

## Implementation notes
- Pin Go toolchain via the `go` directive in `go.mod` (`go 1.22`); allow
  newer with `toolchain go1.22.x`.
- `gofmt` check uses `! gofmt -l . | grep .` so a non-empty list fails.
- Cache key includes `hashFiles('**/go.sum')`.
- Use SHA-pinned action versions, not floating tags, to keep CI reproducible.
- Workflow `permissions:` block is minimal: `contents: read` only.
- No third-party Go deps yet; `go.sum` should be empty after `go mod tidy`.

## Test plan
- CI itself is the test: opening this issue's PR must pass the workflow.
- Add a deliberate `gofmt` violation in a throwaway branch and confirm CI
  fails (manual smoke).
- Add a deliberate vet violation (`fmt.Printf("%d", "x")`) and confirm CI
  fails (manual smoke).
- Verify `go mod tidy` drift check by adding an unused import and confirming
  CI red.
