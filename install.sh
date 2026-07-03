#!/bin/sh
# a2acli installer — downloads the right prebuilt binary from GitHub releases,
# verifies it against the published checksums, and installs it.
#
#   curl -fsSL https://raw.githubusercontent.com/andyjmorgan/a2acli/main/install.sh | sh
#
# While the repository is private the public download URLs 404; in that case
# the installer falls back to an authenticated `gh` CLI automatically:
#
#   gh api repos/andyjmorgan/a2acli/contents/install.sh -H "Accept: application/vnd.github.raw" | sh
#
# Options via environment:
#   INSTALL_DIR  target directory (default: ~/.local/bin)
#   VERSION      release tag to install, e.g. v0.1.0 (default: latest)
set -eu

REPO="andyjmorgan/a2acli"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

fail() { echo "install: $*" >&2; exit 1; }

case "$(uname -s)" in
  Linux) os="linux" ;;
  Darwin) os="darwin" ;;
  *) fail "unsupported OS: $(uname -s) (linux and darwin only)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

have_gh() { command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; }

# Resolve the release tag: VERSION override, anonymous API, then gh.
tag="${VERSION:-}"
if [ -z "$tag" ]; then
  tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
    | grep '"tag_name"' | head -1 | cut -d'"' -f4 || true)
fi
if [ -z "$tag" ] && have_gh; then
  tag=$(gh release view --repo "$REPO" --json tagName --jq .tagName 2>/dev/null || true)
fi
[ -n "$tag" ] || fail "could not determine the release tag (private repo? authenticate gh, or set VERSION=vX.Y.Z)"
ver="${tag#v}"

asset="a2acli_${ver}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading a2acli $tag ($os/$arch)..."
if curl -fsSL -o "$tmp/$asset" "$base/$asset" 2>/dev/null; then
  curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "download failed: checksums.txt"
elif have_gh; then
  gh release download "$tag" --repo "$REPO" --dir "$tmp" -p "$asset" -p checksums.txt \
    || fail "gh release download failed"
else
  fail "cannot download $asset (private repo? authenticate gh and re-run)"
fi

(
  cd "$tmp"
  if command -v sha256sum >/dev/null 2>&1; then
    grep " $asset\$" checksums.txt | sha256sum -c - >/dev/null
  else
    grep " $asset\$" checksums.txt | shasum -a 256 -c - >/dev/null
  fi
) || fail "checksum verification failed for $asset"

tar -xzf "$tmp/$asset" -C "$tmp" a2acli
mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/a2acli" "$INSTALL_DIR/a2acli"

echo "Installed $("$INSTALL_DIR/a2acli" --version) to $INSTALL_DIR/a2acli"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "note: $INSTALL_DIR is not on your PATH" ;;
esac
