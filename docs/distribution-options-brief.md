---
type: brief
subject: Secondary distribution options — brew tap vs curl installer
created: 2026-05-27
---

# Distribution Options Brief: Secondary Paths for act

## Primary distribution (already works, agent-bootstrappable)

```sh
go install github.com/aac/act/cmd/act@latest
```

This is the canonical path. One command, no setup, any machine with Go. Agent sessions self-bootstrap from it without human involvement — the same property that makes act useful is preserved by the install mechanism. The sections below are for users who don't have Go set up. Brew and curl are secondary, not alternatives to `go install`.

---

## 1. Brew Tap

**Operator setup (one-time):** Create an empty public repo at `github.com/aac/homebrew-act`. Add a Ruby formula at `Formula/act.rb` that fetches the release tarball from GitHub Releases, computes the SHA256, builds with `go build`, or more commonly points to a pre-built bottle. Each release needs the formula updated with the new version string and SHA.

**UX:**
```sh
brew tap aac/act
brew install act
```

Or as a one-liner: `brew install aac/act/act`.

**Trade-offs:**

- Mac-only (Linux Homebrew exists but is second-class; Windows has no path). Excludes Linux-native setups.
- Discoverability: `brew search act` finds the formula once the tap is added; cold users have to know to add the tap first.
- Formula maintenance per release: every tagged release requires a PR to `homebrew-act` updating the version and SHA. This is mechanical but cannot be skipped — an out-of-date formula ships a stale binary.
- Signed bottles: distributing pre-built bottles requires signing infrastructure and hosted storage (GitHub Packages or a CDN). Without bottles, every `brew install` recompiles from source, which requires Go on the user's machine — defeating much of the purpose for non-Go users. With bottles, the signing and hosting add per-release overhead.
- Brew cache friction: Homebrew caches tarballs and bottles aggressively. Users who last ran `brew update` weeks ago may pull a stale version and not notice. `brew upgrade aac/act/act` is the correct incantation; most users just run `brew upgrade`.
- Trust model: a tap is a third-party Ruby file executed during install; users who audit installs will inspect it. Low concern for this audience, but worth noting.

---

## 2. Curl Installer

**Setup:** A shell script hosted at e.g. `https://aac.github.io/act/install.sh` (or raw GitHub). The script detects OS/arch (`uname -s`, `uname -m`), maps to a release asset name, downloads the binary from GitHub Releases, and writes it to `~/.local/bin` (or `~/bin` with a PATH note).

**UX:**
```sh
curl -fsSL https://aac.github.io/act/install.sh | sh
```

Or with version pinning:
```sh
curl -fsSL https://aac.github.io/act/install.sh | sh -s -- --version v0.9.2
```

**Trade-offs:**

- Cross-platform: works on macOS and Linux with the same script. Windows users need a separate approach (PowerShell or WSL), but the target audience for non-Go installs is Mac/Linux.
- Security concern (pipe-to-shell): the canonical objection is that the downloaded script runs with user permissions before the user can review it. Mitigations: host via HTTPS from a content-addressed release (GitHub Releases checksums), pin SHA in the install invocation, or offer `curl -fsSL ... > install.sh && sh install.sh` as an auditable form. These are well-understood, not blockers.
- Version pinning: without `--version`, the script fetches the latest tag. Agents and automated setups can pin; casual users get the latest, which is usually what they want.
- No update mechanism: `curl | sh` installs once. There is no `brew upgrade` equivalent — users re-run the install or use a version manager. For agents that run `go install @latest`, this is irrelevant; for casual human users it means they stay on the installed version until they notice a release note.
- OS/arch detection coverage: needs explicit handling for `arm64` (Apple Silicon), `amd64` (Intel/Linux), and at minimum a clean error for unsupported platforms. This is a few lines of shell but needs to be correct.
- Hosting: GitHub Pages (`aac.github.io/act/install.sh`) is zero-cost and version-controlled. The script itself lives in the repo.

---

## 3. Both in Parallel

Running brew tap and curl installer simultaneously is operationally feasible. The incremental burden:

- **One-time:** create `homebrew-act` repo (minutes), write and test the formula (one hour), write and test `install.sh` (one to two hours). Total one-time cost is roughly a half-day.
- **Per-release ongoing:** update formula SHA + version in `homebrew-act` (5 minutes), verify curl script picks up the new tag via GitHub Releases API (automatic if the script uses the latest-release redirect). Net per-release overhead: minimal once automation around the formula update is scripted or GitHub-Actioned.
- **Reach:** brew covers the install-once-and-forget Mac user; curl covers Mac and Linux, including CI environments that don't have brew. In practice their user populations overlap heavily for this tool's audience.

The main cost is the formula repo and the per-release update discipline, not engineering complexity.

---

## 4. Recommendation

**Ship the curl installer first; add the brew tap if demand surfaces.**

The curl installer covers the broadest set of non-Go users (Mac and Linux, CI environments, agent orchestration scaffolds that can't assume brew), requires no external repo to maintain, and is fully automatable. The script is a dozen lines, lives in-tree, and can be updated atomically with a release tag. Brew is Mac-only, requires a separate hosted repo, adds per-release formula-update friction, and only adds discoverability value if the tool has the kind of surface-area where `brew search` cold-discovery matters — which for a developer-tool-for-agents, it largely doesn't. Both mechanisms are secondary paths for users without Go; the curl installer handles the broader secondary audience with less maintenance surface. When a bottle-signed brew tap becomes worth the overhead (i.e., there's material inbound interest from Mac users who prefer brew), add it then.

**Cross-tool consistency stance:** apply the same choice to `ask`, `poke`, and `reach`. All four tools have identical target audiences (Go-primary, curl as fallback), identical maintenance burdens per tool, and no user-visible reason to have different install paths across sibling tools. A single `install.sh` pattern in each repo, plus a joint "install all four" convenience script at a shared landing page if wanted, keeps the experience coherent. Diverging — e.g., brew for `act` but curl for `ask` — introduces user confusion without any offsetting benefit. Pick one shape for the family; curl is the shape.
