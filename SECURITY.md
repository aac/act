# Security Policy

## What act does (and does not do)

`act` is a **local-only** tool. It has no embedded network client, no telemetry, no analytics, and no server component. The only network operations it performs are the git commands the user explicitly invokes (`act init` may run `git init`; `act close` with `--push` runs `git push`; `act update --claim` may run `git pull` on the nested `.act/` repo). All of these are standard git operations directed at remotes the user already controls.

`act` mutates local git state:
- It reads and writes files inside the `.act/` nested git repository.
- It runs `git commit` and (when instructed) `git push` in the `.act/` repo.
- It runs `git commit` in the host repo when `Act-Id:` trailers are embedded in work commits.
- It executes user-defined hooks configured in `.act/config.json` (pre-close, post-close, etc.). These hooks run with the permissions of the invoking process.

`act` also embeds an MCP server (`act mcp`, stdio transport). The MCP server exposes the same operations as the CLI; it does not open any TCP listener and is only reachable via the stdio channel of the process that launched it. There is no authentication layer beyond process-level access.

**In short:** `act` has the same blast radius as a local git client. It does not exfiltrate data, phone home, or open listening ports.

## Supported versions

| Version | Supported |
| ------- | --------- |
| 0.2.x   | Yes       |
| 0.1.x   | No        |

`act` is pre-v1. Only the latest release receives security fixes. We recommend always running the latest version.

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Report via GitHub's private security advisory mechanism:
1. Navigate to the repository on GitHub.
2. Click the **Security** tab.
3. Click **Report a vulnerability** under "Private vulnerability reporting".

We will acknowledge reports within 72 hours and aim to publish a fix within 14 days for confirmed vulnerabilities. If a CVE is warranted we will request one at that time.

If private vulnerability reporting is not yet enabled on this repository, file a confidential report by emailing the repository owner. (See the repository's commit history for a contact address, or file a placeholder report via the Security tab and we will reach out.)

## Scope notes

Given that `act` runs user-defined hooks and shells out to git, the most relevant attack surfaces are:

- **Hook injection**: a malicious `.act/config.json` (e.g. from a compromised remote) could specify hook commands that run with the user's privileges. Treat the `.act/` repo as trusted only if you trust its remote.
- **Path traversal in op files**: op files written by a compromised `.act/` remote could attempt to write outside `.act/`. The op-file reader validates filenames; reports of bypasses are in-scope.
- **MCP stdio channel**: any process that can read/write the stdin/stdout of `act mcp` can drive all act operations. Scope the MCP server's stdio to trusted callers.
