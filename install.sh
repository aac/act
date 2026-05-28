#!/bin/sh
# act installer — build binary, install skill, register MCP server.
#
# Usage:
#   ./install.sh                           # auto-detect harness
#   ./install.sh --target claude           # install for Claude Code
#   ./install.sh --target codex            # install for Codex
#   ./install.sh --prefix ~/.local         # binary destination prefix
#   ./install.sh --uninstall               # remove binary + MCP entry
#
# What it does:
#   1. Detects the target harness (claude or codex), or accepts --target flag.
#   2. Verifies Go is installed.
#   3. Builds act from source into $PREFIX/bin/act (default ~/.local/bin).
#   4. Runs act install-skill to populate ~/.claude/skills/act (or codex equiv).
#   5. Registers the act MCP server in the agent's config:
#        Claude: claude mcp add --scope user act -- act mcp
#        Codex : codex mcp add act -- act mcp
#   6. Verifies the binary and prints confirmation.
#
# Idempotent — safe to re-run. Existing binary is replaced. MCP registration
# is skipped if the entry already exists with the same command.
#
# Safety / curl-pipe posture:
#   Script prints every action before taking it. MCP registration requires
#   --yes when overwriting an existing entry that differs; new entries are
#   always added silently. Binary builds and skill installs never prompt.
#
# Uninstall:
#   --uninstall removes $PREFIX/bin/act and the MCP server entry. Skills
#   directory (~/.{claude,codex}/skills/act) is NOT removed — it may contain
#   user edits. Remove manually if desired.
#
# Env overrides:
#   ACT_REPO_DIR  Path to the act repo (default: directory containing this script)

set -eu

REPO_DIR="${ACT_REPO_DIR:-$(cd "$(dirname "$0")" && pwd)}"

# ---- helpers ----------------------------------------------------------------

say()  { printf '%s\n' "$*"; }
warn() { printf 'warn: %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

# ---- argument parsing -------------------------------------------------------

TARGET=""
PREFIX="${HOME}/.local"
UNINSTALL=false
YES=false

while [ $# -gt 0 ]; do
  case "$1" in
    --target)
      [ $# -ge 2 ] || die "--target requires a value (claude or codex)"
      TARGET="$2"
      shift 2
      ;;
    --target=*)
      TARGET="${1#--target=}"
      shift
      ;;
    --prefix)
      [ $# -ge 2 ] || die "--prefix requires a value"
      PREFIX="$2"
      shift 2
      ;;
    --prefix=*)
      PREFIX="${1#--prefix=}"
      shift
      ;;
    --uninstall)
      UNINSTALL=true
      shift
      ;;
    --yes|-y)
      YES=true
      shift
      ;;
    -h|--help)
      say "Usage: $0 [--target claude|codex] [--prefix DIR] [--uninstall] [--yes]"
      say ""
      say "Builds act from source and registers it as an MCP server in your agent harness."
      say ""
      say "Options:"
      say "  --target claude|codex   Skip auto-detection; install for the given harness."
      say "  --prefix DIR            Binary install prefix (default: ~/.local)."
      say "                          Binary is placed at PREFIX/bin/act."
      say "  --uninstall             Remove the binary and MCP registration."
      say "  --yes, -y               Auto-confirm overwriting an existing MCP entry that"
      say "                          differs. Required when running non-interactively (e.g."
      say "                          curl … | sh) if an entry already exists."
      say "  -h, --help              Show this help."
      say ""
      say "Environment:"
      say "  ACT_REPO_DIR  Path to act repo (default: directory containing this script)"
      exit 0
      ;;
    *)
      die "unknown argument: $1 (try --help)"
      ;;
  esac
done

# ---- validate target --------------------------------------------------------

if [ -n "$TARGET" ]; then
  case "$TARGET" in
    claude|codex) ;;
    *) die "invalid target: ${TARGET} (must be claude or codex)" ;;
  esac
fi

# ---- harness detection ------------------------------------------------------

detect_harness() {
  if [ -d "${HOME}/.claude" ]; then
    echo "claude"
  elif [ -d "${HOME}/.codex" ]; then
    echo "codex"
  else
    echo ""
  fi
}

if [ -z "$TARGET" ]; then
  TARGET="$(detect_harness)"
  if [ -z "$TARGET" ]; then
    die "could not detect harness (no ~/.claude or ~/.codex found). Use --target claude or --target codex."
  fi
  say "detected harness: ${TARGET}"
else
  say "target harness: ${TARGET}"
fi

BIN_DIR="${PREFIX}/bin"
BIN_PATH="${BIN_DIR}/act"

# ---- uninstall path ---------------------------------------------------------

if $UNINSTALL; then
  say "uninstalling act for ${TARGET}..."

  if [ -f "$BIN_PATH" ]; then
    say "  removing binary: ${BIN_PATH}"
    rm "$BIN_PATH"
  else
    say "  binary not found at ${BIN_PATH}; skipping"
  fi

  case "$TARGET" in
    claude)
      if claude mcp get act >/dev/null 2>&1; then
        say "  removing Claude MCP entry: act"
        claude mcp remove act -s user 2>/dev/null || warn "claude mcp remove failed"
      else
        say "  no Claude MCP entry found; skipping"
      fi
      ;;
    codex)
      if codex mcp get act >/dev/null 2>&1; then
        say "  removing Codex MCP entry: act"
        codex mcp remove act 2>/dev/null || warn "codex mcp remove failed"
      else
        say "  no Codex MCP entry found; skipping"
      fi
      ;;
  esac

  say ""
  say "act uninstalled from ${TARGET}."
  exit 0
fi

# ---- verify repo ------------------------------------------------------------

[ -f "${REPO_DIR}/go.mod" ] || die "go.mod not found in ${REPO_DIR}; run from the act repo or set ACT_REPO_DIR"
[ -d "${REPO_DIR}/cmd/act" ] || die "cmd/act not found in ${REPO_DIR}"

# ---- verify Go --------------------------------------------------------------

have go || die "go required to build act; install from https://go.dev/dl/ or wait for a release-download fallback"
say "using Go: $(go version)"

# ---- build binary -----------------------------------------------------------

say "building act..."
mkdir -p "$BIN_DIR"
(cd "$REPO_DIR" && go build -o "$BIN_PATH" ./cmd/act) || die "go build failed"
say "binary: ${BIN_PATH}"

# ---- install skill ----------------------------------------------------------

say "installing act skill..."
"$BIN_PATH" install-skill 2>/dev/null || warn "act install-skill failed (non-fatal; skill may need manual install)"

# ---- register MCP server ----------------------------------------------------

register_claude() {
  if claude mcp get act >/dev/null 2>&1; then
    say "Claude MCP entry for 'act' already exists; verifying..."
    existing_cmd="$(claude mcp get act 2>/dev/null | grep 'Command:' | sed 's/.*Command: *//')"
    existing_args="$(claude mcp get act 2>/dev/null | grep 'Args:' | sed 's/.*Args: *//')"
    if [ "$existing_cmd" = "act" ] && [ "$existing_args" = "mcp" ]; then
      say "  entry is correct; no update needed."
    else
      say "  existing entry differs:"
      say "    command: ${existing_cmd:-<none>} (want: act)"
      say "    args:    ${existing_args:-<none>} (want: mcp)"
      if $YES; then
        say "  replacing (--yes)..."
        claude mcp remove act -s user 2>/dev/null || true
        claude mcp add --scope user act -- act mcp
        say "  replaced Claude MCP entry."
      else
        die "existing MCP entry differs; re-run with --yes to overwrite, or run: claude mcp remove act -s user"
      fi
    fi
  else
    say "registering act MCP server with Claude Code (user scope)..."
    claude mcp add --scope user act -- act mcp
    say "registered."
  fi
}

register_codex() {
  if codex mcp get act >/dev/null 2>&1; then
    say "Codex MCP entry for 'act' already exists; no update needed."
  else
    say "registering act MCP server with Codex..."
    codex mcp add act -- act mcp
    say "registered."
  fi
}

case "$TARGET" in
  claude) have claude || die "claude binary not found; is Claude Code installed?"; register_claude ;;
  codex)  have codex  || die "codex binary not found; is Codex installed?"; register_codex ;;
esac

# ---- verify -----------------------------------------------------------------

say ""
"$BIN_PATH" version 2>/dev/null || "$BIN_PATH" --help 2>/dev/null | head -3 || warn "could not verify binary (check ${BIN_PATH})"

say ""
say "act installed for ${TARGET}."
say "  binary: ${BIN_PATH}"
say ""
say "Start a new session (or /clear) so the skill and MCP server are picked up."
