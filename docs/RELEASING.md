# Releasing a2acli

Releases are cut by pushing a `vX.Y.Z` tag. The `Release` workflow
(`.github/workflows/release.yml`) runs the test suite, then goreleaser builds
the four platform tarballs (`linux`/`darwin` × `amd64`/`arm64`), a
`checksums.txt`, and publishes a GitHub Release.

```bash
git checkout main && git pull
git tag v0.4.1
git push origin v0.4.1
```

CI (`ci.yml`) must be green on `main` first: linux race tests, windows/darwin
cross-builds, and the SKILL.md lockstep check.

## macOS code signing & notarization

Darwin binaries are signed and notarized **in place before archiving** by a
goreleaser post-build hook (`scripts/sign-macos.sh`), using
[`rcodesign`](https://github.com/indygreg/apple-platform-rs) from the Linux
runner — no macOS runner required. Because signing happens before the tarball
is built, `checksums.txt` covers the signed binary.

**The hook is a no-op** for linux, and for darwin when the signing secrets are
absent (it prints an `UNSIGNED` warning and continues). So releases always
succeed; signing switches on the moment the secrets exist.

### Configuration

Non-secret identifiers are repo **variables** (already set):

| Variable | Value | Meaning |
|---|---|---|
| `MACOS_NOTARY_ISSUER_ID` | `69a6de86-…` | App Store Connect issuer id |
| `MACOS_NOTARY_KEY_ID` | `84826SWSQ2` | App Store Connect key id |

To activate signing, set these three repo **secrets** — the **personal
DonkeyWork** Developer ID + App Store Connect key (the same identity dwvault
signs with, *not* any other Apple account):

```bash
gh secret set MACOS_DEVID_P12_BASE64   -R andyjmorgan/a2acli < devid.p12.b64
gh secret set MACOS_DEVID_P12_PASSWORD -R andyjmorgan/a2acli        # prompts
gh secret set MACOS_NOTARY_P8          -R andyjmorgan/a2acli < AuthKey_84826SWSQ2.p8
```

- `MACOS_DEVID_P12_BASE64` — `base64 -w0 DeveloperIDApplication.p12`
- `MACOS_DEVID_P12_PASSWORD` — the `.p12` export password
- `MACOS_NOTARY_P8` — raw contents of the App Store Connect `.p8` API key

A bare Mach-O executable cannot be stapled, so the hook notarizes a zip of the
binary; Gatekeeper validates the notarization online on first run.

## Verifying a release

```bash
# From a released older binary, prove the self-update path:
a2acli update            # downloads latest, verifies sha256, atomic in-place swap
a2acli --version

# On macOS, confirm a signed build is accepted:
spctl -a -vvv --type install ./a2acli      # once signing secrets are live
codesign -dv --verbose=4 ./a2acli
```

## Self-update mechanics

`a2acli update` fetches the release for the current platform, verifies its
sha256 against `checksums.txt`, extracts the binary, and atomically renames it
over the running executable. PATH and shell profiles are never touched. While
the repo is private, anonymous downloads 404 and the updater falls back to an
authenticated `gh`. `A2ACLI_REPO` overrides the release source (forks/testing).
