---
name: Bug report
about: Something act does that it shouldn't, or doesn't do that it should
title: ''
labels: bug
assignees: ''
---

## Describe the bug

A clear, concise description of what went wrong.

## To reproduce

Steps to reproduce the behavior:

1. `act init` (or describe the setup)
2. Run `act ...`
3. See error

## Expected behavior

What did you expect `act` to do?

## Actual behavior

What did it actually do? Include the full command output and any error envelope JSON.

## Environment

- `act` version (`act --version` or `go install ...@<version>`):
- Go version (`go version`):
- OS and architecture:
- Was `act` running inside an MCP session or directly via CLI?

## Additional context

Any other relevant context — hooks configured in `.act/config.json`, concurrent agents, unusual repo topology, etc.
