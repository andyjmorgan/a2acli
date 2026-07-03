# a2acli

A generic, scriptable command-line client for [A2A](https://a2a-protocol.org) agents:
discovery, auth, message sending, task handles, polling, and cancellation. Works against
any compliant A2A server; non-streaming first by design.

## Install

One-liner (downloads the latest release for your OS/arch, verifies checksums, installs to `~/.local/bin`):

```bash
curl -fsSL https://raw.githubusercontent.com/andyjmorgan/a2acli/main/install.sh | sh
```

While the repository is private that raw URL needs auth — use an authenticated `gh` instead
(the installer falls back to `gh` for the artifact download automatically):

```bash
gh api repos/andyjmorgan/a2acli/contents/install.sh -H "Accept: application/vnd.github.raw" | sh
```

Pin a version or change the target directory with `VERSION=v0.1.0` / `INSTALL_DIR=/usr/local/bin` before the `sh`. Or build from source:

```bash
go install github.com/andyjmorgan/a2acli/cmd/a2acli@latest
```

Keep it current with `a2acli update` (`--check` just reports). Released builds also
print a passive one-line notice on stderr when a newer version is known (interactive
terminals only; disable with `A2ACLI_NO_UPDATE_CHECK=1`).

## Quick start

```bash
# Register an agent (placement is derived from its Agent Card)
a2acli agent add prod https://agent.example.com --secret name:prod-agent

# Provide the secret: harness env var, OS keyring, or 0600 secrets file
a2acli agent set-key prod                 # prompts (no echo), stores in the keyring
# ...or: export A2ACLI_SECRET_PROD_AGENT=...

a2acli card prod
a2acli agents describe          # every registered agent + card summary (--json for harnesses)
a2acli send prod "quick question"
a2acli send prod "long job" --return-immediately --json
a2acli task watch prod <task-id> --interval 2 --out /tmp/task.jsonl
a2acli task cancel prod <task-id>
```

Raw URLs work everywhere aliases do: `a2acli send http://localhost:8420 "hi"`.

## Credentials

Config (`~/.config/a2acli/config.toml`, 0600) names a secret; it never holds one.

```toml
[agents.prod]
url = "https://agent.example.com"

[agents.prod.auth]
header = "Authorization"      # optional — derived from the Agent Card when omitted
value  = "Bearer $secret"     # template; also "Token $secret", "$secret", ...
secret = "prod-agent"         # resolution walks env => keyring => 0600 secrets file
```

For short-lived tokens use a command instead — any executable that prints the
token to stdout (`secret_cmd = "your-vault oauth get prod"`). Its result is cached
per process; a 401 invalidates the cache, re-executes, and retries once.

One-time override on any command: `--secret cmd:"..."`, `--secret name:other`, or a literal.

## Output contract

- `--json` — one normalized JSON object on stdout (`type: message | task | busy`).
- `--out FILE` — appends JSONL `task_snapshot` events (task watch).
- `task watch` exits 0 on completed, non-zero on failed/cancelled
  (`--no-fail-on-task-failure` suppresses).
- Ctrl-C in `task watch` stops observing; it never cancels the remote task.

## Development

```bash
go test ./...           # unit + e2e (e2e drives the compiled binary against internal/mocka2a)
go test -race ./...
```

Layout: `cmd/a2acli` (main), `internal/{config,auth,client,normalize,cli}`,
`internal/mocka2a` (in-process mock A2A server), `test/e2e` (binary-level tests).
