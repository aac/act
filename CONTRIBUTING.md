# Contributing to act

## Filing issues

Use GitHub Issues for bug reports and feature requests; templates are provided. For
design proposals — changes to behavior, the skill, or the data model — open an issue to
discuss first, so a direction that's already been set doesn't get re-litigated.

## The engineering rules are load-bearing

[`AGENTS.md`](AGENTS.md) is the engineering guide for this repo. Its
**documentation-discipline rule** — every user-visible behavior claim ships with a
boundary-asserting test in the same commit — is not a style preference; it encodes drift
bugs observed during development. Read it before changing behavior or docs.

## Pull requests

- Keep changes focused — one concern per PR.
- Run `gofmt -l`, `go vet ./...`, and `go test ./...` clean before submitting.
- Don't hand-edit the version in `.claude-plugin/plugin.json` / `.codex-plugin/plugin.json`
  for a normal change — the git tag is the source of truth and the release process owns
  versioning. If a change is release-worthy, add a `CHANGELOG.md` entry describing it.

<!-- act:contributing-stanza:start -->

## act commit-marker convention

This repo uses [act](https://github.com/aac/act) for agent task tracking.
Agent-authored commits include an `Act-Id: act-XXXX` trailer in the commit
body that pairs the commit with its tracked issue.

You don't need to interact with this convention — `Act-Id:` trailers are
ignored by conventional-commit linters, semantic-release, and CHANGELOG
generators, and have no effect on merge or review. If you'd like to add
them to your own commits, see act's docs; otherwise, ignore.

<!-- act:contributing-stanza:end -->
