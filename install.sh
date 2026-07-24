#!/bin/sh
# Kagi CLI universal installer (macOS + Linux).
#
# Intended to be hosted at https://get.kagi.pw and run as:
#   curl -sSf https://get.kagi.pw | sh
#
# Downloads the latest 'kagi' release tarball from
# github.com/senseylabs/kagi-cli, verifies it against the published
# checksums file, and installs the 'kagi' binary onto PATH.
set -e

REPO="senseylabs/kagi-cli"
PROJECT="kagi-cli"
BINARY="kagi"

err() {
  echo "error: $*" >&2
  exit 1
}

info() {
  echo "==> $*"
}

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    err "required command '$1' not found in PATH"
  fi
}

need_cmd curl
need_cmd tar
need_cmd uname
need_cmd grep
need_cmd sed
need_cmd mktemp

# --- detect platform ---------------------------------------------------

detect_os() {
  os_raw="$(uname -s)"
  case "$os_raw" in
    Darwin) echo "darwin" ;;
    Linux) echo "linux" ;;
    *) err "unsupported operating system: $os_raw (kagi ships prebuilt binaries for macOS and Linux only; for Windows use install.ps1)" ;;
  esac
}

detect_arch() {
  arch_raw="$(uname -m)"
  case "$arch_raw" in
    x86_64 | amd64) echo "amd64" ;;
    arm64 | aarch64) echo "arm64" ;;
    *) err "unsupported CPU architecture: $arch_raw (kagi ships amd64 and arm64 binaries only)" ;;
  esac
}

OS="$(detect_os)"
ARCH="$(detect_arch)"

info "detected platform: ${OS}/${ARCH}"

# --- resolve latest release tag ----------------------------------------

API_URL="https://api.github.com/repos/${REPO}/releases/latest"

info "resolving latest release from ${API_URL}"

RELEASE_JSON="$(curl -fsSL "$API_URL")" || err "failed to reach GitHub API at ${API_URL} (network issue or repo not public yet)"

TAG="$(echo "$RELEASE_JSON" | grep '"tag_name"' | head -n1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"

[ -n "$TAG" ] || err "could not parse a release tag from the GitHub API response; the repo may have no releases yet"

# goreleaser's {{.Version}} strips a leading 'v' from the tag.
VERSION="${TAG#v}"

info "latest release: ${TAG} (version ${VERSION})"

# --- download archive + checksums ---------------------------------------

ARCHIVE="${PROJECT}_${VERSION}_${OS}_${ARCH}.tar.gz"
CHECKSUMS="${PROJECT}_${VERSION}_checksums.txt"

BASE_URL="https://github.com/${REPO}/releases/download/${TAG}"
ARCHIVE_URL="${BASE_URL}/${ARCHIVE}"
CHECKSUMS_URL="${BASE_URL}/${CHECKSUMS}"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT INT TERM

info "downloading ${ARCHIVE_URL}"
curl -fsSL -o "${WORKDIR}/${ARCHIVE}" "$ARCHIVE_URL" \
  || err "failed to download ${ARCHIVE_URL} (no release asset for ${OS}/${ARCH}, or network error)"

info "downloading ${CHECKSUMS_URL}"
curl -fsSL -o "${WORKDIR}/${CHECKSUMS}" "$CHECKSUMS_URL" \
  || err "failed to download checksums file ${CHECKSUMS_URL}"

# --- verify checksum ------------------------------------------------------

info "verifying checksum"

EXPECTED_SUM="$(grep " ${ARCHIVE}\$" "${WORKDIR}/${CHECKSUMS}" | awk '{print $1}')"
[ -n "$EXPECTED_SUM" ] || err "no checksum entry found for ${ARCHIVE} in ${CHECKSUMS}"

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL_SUM="$(sha256sum "${WORKDIR}/${ARCHIVE}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL_SUM="$(shasum -a 256 "${WORKDIR}/${ARCHIVE}" | awk '{print $1}')"
else
  err "neither sha256sum nor shasum found; cannot verify archive integrity"
fi

[ "$EXPECTED_SUM" = "$ACTUAL_SUM" ] \
  || err "checksum mismatch for ${ARCHIVE}: expected ${EXPECTED_SUM}, got ${ACTUAL_SUM} (download may be corrupted or tampered with)"

info "checksum OK"

# --- extract ---------------------------------------------------------------

info "extracting ${BINARY}"
tar -xzf "${WORKDIR}/${ARCHIVE}" -C "$WORKDIR" "$BINARY" \
  || err "failed to extract '${BINARY}' from ${ARCHIVE}"

[ -f "${WORKDIR}/${BINARY}" ] || err "extracted archive did not contain a '${BINARY}' binary"

chmod +x "${WORKDIR}/${BINARY}"

# --- install -----------------------------------------------------------

INSTALL_DIR="/usr/local/bin"
if [ ! -w "$INSTALL_DIR" ] 2>/dev/null; then
  INSTALL_DIR="${HOME}/.local/bin"
  mkdir -p "$INSTALL_DIR" || err "failed to create fallback install directory ${INSTALL_DIR}"
fi

info "installing to ${INSTALL_DIR}/${BINARY}"

if ! mv "${WORKDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}" 2>/dev/null; then
  # /usr/local/bin existed but wasn't actually writable (e.g. root-owned) -
  # fall back rather than failing silently.
  if [ "$INSTALL_DIR" = "/usr/local/bin" ]; then
    INSTALL_DIR="${HOME}/.local/bin"
    mkdir -p "$INSTALL_DIR" || err "failed to create fallback install directory ${INSTALL_DIR}"
    info "no write access to /usr/local/bin, installing to ${INSTALL_DIR}/${BINARY} instead"
    mv "${WORKDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}" \
      || err "failed to move binary into ${INSTALL_DIR}"
  else
    err "failed to move binary into ${INSTALL_DIR}"
  fi
fi

chmod +x "${INSTALL_DIR}/${BINARY}"

case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *) echo "warning: ${INSTALL_DIR} is not on your PATH. Add it with:" >&2
     echo "  export PATH=\"${INSTALL_DIR}:\$PATH\"" >&2
     ;;
esac

# --- verify ---------------------------------------------------------

INSTALLED_VERSION="$("${INSTALL_DIR}/${BINARY}" --version 2>&1)" \
  || err "installed binary at ${INSTALL_DIR}/${BINARY} failed to run '--version'"

info "installed: ${INSTALLED_VERSION}"
info "kagi ${VERSION} installed successfully to ${INSTALL_DIR}/${BINARY}"
