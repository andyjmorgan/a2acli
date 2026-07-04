#!/usr/bin/env bash
# Code-sign and notarize a single darwin binary in place, invoked as a
# goreleaser post-build hook (once per built binary). Signing keeps the
# release pipeline honest for macOS: without it, Gatekeeper quarantines
# every curl-downloaded copy.
#
# Usage: sign-macos.sh <binary-path> <goos>
#
# Behavior:
#   - Non-darwin builds: immediate no-op (the hook fires for every arch).
#   - darwin with the signing env unset: WARN and skip, so local
#     `goreleaser build` and secret-less releases still succeed (unsigned).
#   - darwin with the env set: hard-fail on any signing/notarization error.
#
# Required env when signing (mirrors the dwvault sign-macos action):
#   MACOS_DEVID_P12_BASE64   base64 Developer ID Application .p12
#   MACOS_DEVID_P12_PASSWORD .p12 export password
#   MACOS_NOTARY_P8          App Store Connect API key (.p8 contents)
#   MACOS_NOTARY_ISSUER_ID   issuer id (not secret)
#   MACOS_NOTARY_KEY_ID      key id (not secret)
set -euo pipefail

bin="${1:?binary path required}"
goos="${2:?goos required}"

if [[ "$goos" != "darwin" ]]; then
  exit 0
fi

if [[ -z "${MACOS_DEVID_P12_BASE64:-}" ]]; then
  echo "sign-macos: no signing credentials in env; leaving $bin UNSIGNED" >&2
  exit 0
fi

: "${MACOS_DEVID_P12_PASSWORD:?}"
: "${MACOS_NOTARY_P8:?}"
: "${MACOS_NOTARY_ISSUER_ID:?}"
: "${MACOS_NOTARY_KEY_ID:?}"

if ! command -v rcodesign >/dev/null 2>&1; then
  echo "sign-macos: rcodesign not on PATH" >&2
  exit 1
fi

umask 077
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "$MACOS_DEVID_P12_BASE64" | base64 -d > "$work/devid.p12"
printf '%s' "$MACOS_NOTARY_P8" > "$work/AuthKey.p8"
rcodesign encode-app-store-connect-api-key \
  -o "$work/notary-key.json" \
  "$MACOS_NOTARY_ISSUER_ID" "$MACOS_NOTARY_KEY_ID" "$work/AuthKey.p8"

echo "sign-macos: signing $bin" >&2
rcodesign sign --for-notarization \
  --code-signature-flags runtime \
  --p12-file "$work/devid.p12" --p12-password "$MACOS_DEVID_P12_PASSWORD" \
  "$bin"

# A bare Mach-O executable cannot be stapled, so notarize a zip of it.
# Gatekeeper then validates the notarization online on first run.
zip -qj "$work/bin.zip" "$bin"
echo "sign-macos: notarizing $bin (waiting for Apple)" >&2
rcodesign notary-submit --api-key-file "$work/notary-key.json" --wait "$work/bin.zip"

echo "sign-macos: $bin signed + notarized" >&2
