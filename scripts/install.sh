#!/bin/sh
# act installer.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/aac/act/main/scripts/install.sh | sh
#
# Environment overrides:
#   ACT_VERSION   - tag to install (defaults to latest release).
#   ACT_REPO      - GitHub owner/repo slug (defaults to aac/act).
#   ACT_INSTALL_DIR - destination directory; defaults to /usr/local/bin when
#                     running as root, ~/.local/bin otherwise.
#
# The installer detects the host OS and architecture, downloads the matching
# binary plus its sha256 sidecar from the chosen GitHub release, verifies the
# checksum, and installs the binary to ACT_INSTALL_DIR/act.
#
# Exit codes:
#   0 success
#   1 unsupported platform / missing tools / checksum failure / other error

set -eu

REPO="${ACT_REPO:-aac/act}"
VERSION="${ACT_VERSION:-}"
INSTALL_DIR="${ACT_INSTALL_DIR:-}"

log() { printf '[act-install] %s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command '$1' not found in PATH"
}

# Pick a downloader: prefer curl, fall back to wget.
download_to() {
  # $1=url $2=dest
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    die "neither curl nor wget is available"
  fi
}

detect_os() {
  uname_s=$(uname -s 2>/dev/null || echo unknown)
  case "$uname_s" in
    Linux) echo linux ;;
    Darwin) echo darwin ;;
    MINGW*|MSYS*|CYGWIN*) echo windows ;;
    *) die "unsupported OS: $uname_s" ;;
  esac
}

detect_arch() {
  uname_m=$(uname -m 2>/dev/null || echo unknown)
  case "$uname_m" in
    x86_64|amd64) echo amd64 ;;
    arm64|aarch64) echo arm64 ;;
    *) die "unsupported architecture: $uname_m" ;;
  esac
}

resolve_latest_tag() {
  api_url="https://api.github.com/repos/${REPO}/releases/latest"
  tmp=$(mktemp)
  trap 'rm -f "$tmp"' EXIT
  download_to "$api_url" "$tmp" \
    || die "failed to query latest release at $api_url"
  # Extract the "tag_name" field without depending on jq.
  tag=$(grep -m 1 '"tag_name"' "$tmp" | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')
  rm -f "$tmp"
  trap - EXIT
  [ -n "$tag" ] || die "could not parse tag_name from $api_url"
  echo "$tag"
}

main() {
  os=$(detect_os)
  arch=$(detect_arch)

  ext=""
  if [ "$os" = "windows" ]; then
    ext=".exe"
  fi

  if [ -z "$VERSION" ]; then
    VERSION=$(resolve_latest_tag)
  fi
  log "installing act $VERSION for $os/$arch"

  asset="act-${os}-${arch}${ext}"
  base_url="https://github.com/${REPO}/releases/download/${VERSION}"
  bin_url="${base_url}/${asset}"
  sum_url="${base_url}/${asset}.sha256"

  workdir=$(mktemp -d)
  trap 'rm -rf "$workdir"' EXIT

  log "downloading $bin_url"
  download_to "$bin_url" "${workdir}/${asset}" \
    || die "failed to download binary from $bin_url"

  log "downloading $sum_url"
  download_to "$sum_url" "${workdir}/${asset}.sha256" \
    || die "failed to download checksum from $sum_url"

  # Verify checksum. The .sha256 file format is `<hash>  <filename>`.
  expected=$(awk '{print $1}' "${workdir}/${asset}.sha256")
  [ -n "$expected" ] || die "could not parse expected checksum"

  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "${workdir}/${asset}" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "${workdir}/${asset}" | awk '{print $1}')
  else
    die "neither sha256sum nor shasum found; cannot verify download"
  fi

  if [ "$expected" != "$actual" ]; then
    die "checksum mismatch for ${asset}: expected $expected, got $actual"
  fi
  log "checksum verified: $actual"

  if [ -z "$INSTALL_DIR" ]; then
    if [ "$(id -u 2>/dev/null || echo 1)" = "0" ]; then
      INSTALL_DIR="/usr/local/bin"
    else
      INSTALL_DIR="${HOME}/.local/bin"
    fi
  fi

  mkdir -p "$INSTALL_DIR" || die "could not create $INSTALL_DIR"
  dest="${INSTALL_DIR}/act${ext}"
  mv "${workdir}/${asset}" "$dest" || die "could not move binary to $dest"
  chmod +x "$dest"

  log "installed: $dest"
  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) ;;
    *) log "note: ${INSTALL_DIR} is not on your PATH; add it to use 'act' directly." ;;
  esac
}

main "$@"
