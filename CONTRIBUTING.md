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
