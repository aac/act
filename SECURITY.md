# Security Policy

## Reporting a vulnerability

Found a security issue in `act`? Please report it privately using the **Report a vulnerability** button on the **Security** tab of this repository, rather than opening a public issue.

`act` is maintained by one person. Reports are genuinely welcome and I'll look into them.

`act` is a local-only tool with roughly the same blast radius as a local git client: no telemetry, no listening ports, and no network calls beyond the git operations you explicitly invoke. It does run user-defined hooks from `.act/config.json`, so treat a `.act/` repo as trusted only if you trust its remote.
