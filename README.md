# a2acli

**A generic, scriptable command-line client for [A2A](https://a2a-protocol.org) agents** — discovery, auth, messaging, and first-class handling of long-running tasks. Works against any compliant A2A server.

```bash
a2acli send prod "run the full diagnostic suite" --return-immediately --json
# {"type":"task","taskId":"task_123","contextId":"ctx_abc","status":"working"}

a2acli task watch prod task_123 --out /tmp/task.jsonl &
tail -f /tmp/task.jsonl
```

## Why this exists

A2A is becoming the protocol for agent-to-agent work: agents advertise themselves with a card, accept messages, and hand back **task handles** for anything that takes real time — with contexts, polling, and cancellation as protocol primitives. But actually *talking* to an A2A server today means hand-rolling JSON-RPC envelopes with curl, or writing a bespoke client per experiment.

The tempting shortcut — wrapping A2A behind an MCP server — is a bad interface for this protocol:

- **MCP models synchronous tool calls; A2A's core value is the asynchronous task.** Flattening a task lifecycle into a blocking tool call either ties up the caller for the task's whole duration or forces bespoke polling glue into every wrapper.
- **Task handles disappear.** Contexts, task ids, cancellation, and progress are exactly what an orchestrating agent needs to see — an MCP façade hides them behind one opaque call.
- **It couples you to one harness.** An MCP wrapper only helps clients that speak MCP. A CLI is universal: every agent harness, script, and CI job can exec a binary, background it, and tail a file.

a2acli is the missing ergonomic client: one binary that makes A2A's actual shape — cards, sends, tasks, contexts — directly usable by humans, scripts, and other agents.

## Built for delegation

The primary consumer is an **agent delegating work to another agent as a background task**:

```bash
# 1. What can I delegate to?
a2acli agents describe --json

# 2. Fire and get a handle back immediately
a2acli send research "investigate the flaky checkout test" --return-immediately --json

# 3. Background the watch; every state transition lands as one JSON line
a2acli task watch research task_123 --out /tmp/task.jsonl &

# 4. Check in whenever you like — no connection to hold open
tail -n 3 /tmp/task.jsonl
a2acli task get research task_123 --json

# 5. Change your mind explicitly
a2acli task cancel research task_123
```

Five properties make this reliable for automation:

- **A closed output contract.** Every send resolves to exactly one of three JSON shapes, discriminated by `type`: a final `message`, a `task` handle, or a `busy` signal (an [Agentlings](https://github.com/andyjmorgan/DonkeyWork-Agentlings)-compatible backpressure envelope, normalized when detected). No screen-scraping.
- **Branchable exit codes.** `0` completed, `1` task failed/cancelled, `3` input-required (the agent asked a question — answer with `send --task-id`), `4` auth-required, `5` observation failed (network — the task may still be running), `6` credential failure. An orchestrator's next move is a `case`, not a diagnosis.
- **Interruption is not cancellation.** Ctrl-C stops *observing*; the remote task keeps running until you cancel it explicitly. A dropped watcher never kills delegated work.
- **It never hangs and never lies on a blip.** Default timeouts on discovery and every RPC, `--timeout` on every observe verb, transient poll errors retry with backoff, and a dropped stream continues via polling — a network hiccup is never reported as a task verdict.
- **It remembers your delegations.** Every task handle ever observed lands in a local ledger: `a2acli task list --active --refresh` answers "what did I delegate, where, and how is it doing" across sessions — and any task command takes a bare (even prefix-abbreviated) task id, recalling the agent from the ledger.

```bash
a2acli task wait research task_123 --timeout 600 --json   # block silently; ONE final JSON line
a2acli send research "yes, use prod data" --task-id task_123    # answer an input-required pause
a2acli task artifacts task_123 --save ./outputs                 # download the durable outputs
a2acli --max-text 400 task watch task_123                       # cap text: agent context is money
```

There's also an embedded agent skill so harnesses learn all of this in one command:

```bash
a2acli --skill            # print the bundled SKILL.md
a2acli --skill --claude   # install/update ~/.claude/skills/a2acli/SKILL.md
a2acli --skill --codex    # install/update ~/.codex/skills/a2acli/SKILL.md
```

The skill ships inside the binary, so it always matches the CLI surface you actually have — re-run after `a2acli update` to keep it in lockstep.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/andyjmorgan/a2acli/main/install.sh | sh
```

Detects OS/arch (linux/darwin, amd64/arm64), verifies checksums, installs to `~/.local/bin` (`INSTALL_DIR=` to change, `VERSION=v0.4.0` to pin). While the repository is private the raw URL needs auth — use `gh` instead:

```bash
gh api repos/andyjmorgan/a2acli/contents/install.sh -H "Accept: application/vnd.github.raw" | sh
```

Or build from source: `go install github.com/andyjmorgan/a2acli/cmd/a2acli@latest`

Stay current with `a2acli update` (`--check` reports only). Released builds also print a passive one-line stderr notice when a newer version is known — interactive terminals only, never in scripts; disable with `A2ACLI_NO_UPDATE_CHECK=1`.

## Quick start

```bash
# Register an agent — auth placement is derived from its Agent Card
a2acli agent add prod https://agent.example.com --secret name:prod-agent
a2acli agent set-key prod            # prompt (no echo) => OS keyring; --file for headless boxes

a2acli agents describe               # every registered agent + card summary
a2acli card prod                     # one agent's full card
a2acli send prod "quick question"    # immediate answers print directly
a2acli send prod "long job" --wait 30
a2acli send prod "follow-up" --context-id ctx_abc   # stay in one conversation
a2acli send prod "answer" --task-id task_123        # continue a paused/running task
a2acli send prod "review" --file ./diff.patch --data '{"strict":true}' --accept application/json
a2acli task list --active --refresh  # every delegation this machine has made
a2acli task list --remote prod --status working     # the agent's own task list (A2A ListTasks)
a2acli card prod --extended          # authenticated extended card, when advertised
```

Every command takes an alias **or a raw URL** — `a2acli card http://localhost:8420` works with zero setup, resolving the card from `/.well-known/agent-card.json` (with fallbacks, including a direct A2A endpoint).

## Credentials

The config (`~/.config/a2acli/config.toml`, 0600) names secrets — it never contains one:

```toml
[agents.prod]
url = "https://agent.example.com"

[agents.prod.auth]
header = "Authorization"      # optional — derived from the Agent Card when omitted
value  = "Bearer $secret"     # a template: "Token $secret", "$secret", anything
secret = "prod-agent"         # resolution walks env => OS keyring => 0600 secrets file
```

- **Named secrets** resolve at request time: `$A2ACLI_SECRET_PROD_AGENT` (ideal when a harness injects the credential), then the OS keyring, then `~/.config/a2acli/secrets.toml` (0600). A full miss errors with every place it looked.
- **Short-lived tokens** use a command instead — any executable that prints the token to stdout: `secret_cmd = "your-vault oauth get prod"`. Cached per process; a 401 invalidates, re-executes, and retries exactly once, so expiring OAuth tokens refresh transparently.
- **One-time override** on any command: `--secret cmd:"…"`, `--secret name:other`, or a literal.
- **Placement** follows the card's declared schemes: API keys in a header, query parameter or cookie, HTTP auth, and OAuth2/OIDC as a bearer token from `secret_cmd`. Override with `header`/`value`, and `in = "query"` or `"cookie"` to place it there.
- **Credentials stay with the agent:** they're attached only to the agent's own hosts (its URL and card interfaces). Artifact downloads from other hosts, and redirects to them, never receive them.
- Auth header values are never logged, shown by `agent show`, or written to transcripts.

## Output contract

- `--json` → exactly one normalized JSON object on stdout.
- `task watch --out file` → append-only JSONL, one `task_snapshot` per state transition.
- Human output and errors never contaminate machine output: errors go to stderr.

```json
{"type":"message","text":"final answer","contextId":"ctx_abc"}
{"type":"task","taskId":"task_123","contextId":"ctx_abc","status":"working"}
{"type":"busy","error":"context_busy","activeTaskId":"task_123","message":"Retry shortly."}
```

## Streaming

```bash
a2acli stream prod "run the diagnostics" --out /tmp/run.jsonl
a2acli task subscribe prod <task-id>          # re-attach to a running task's stream
```

`stream` sends over `SendStreamingMessage` and prints **one JSON line per event**: the task handle, every status update, artifacts, and the final result. Extension metadata rides along verbatim — an Agentlings server with tool-progress enabled streams each tool call's lifecycle:

```json
{"event":"status_update","status":"working","text":"Get office cluster node count",
 "metadata":{"agentling":{"extension":"https://donkeywork.dev/a2a/extensions/tool-progress/v1",
 "tool_name":"bash","status":"EXECUTING","tool_call_id":"toolu_017k..."}}}
```

**Extensions** are activated per spec through the `A2A-Extensions` header, and only when the agent's card advertises them. Tool-progress is on by default. Add others per call with `--extension <uri>` (repeatable) or per agent with `extensions = ["<uri>"]` in config. If the agent requires an extension you haven't activated, a2acli says so before the call fails.

Servers that don't advertise streaming fall back to send-plus-polling **transparently**, emitting the same JSONL shapes (a note goes to stderr) — scripts work identically against both. If a live stream drops mid-task, observation continues via polling with the same event shapes. Exit codes match `task watch`, and interrupting a stream never cancels the task.

Also on the roadmap: rich rendering of tool-progress events, an interactive chat mode, and a guided TUI for `agent add`.

## Development

```bash
go test ./...           # unit + e2e (e2e drives the compiled binary against an in-process mock A2A server)
go test -race ./...     # what CI runs
```

Layout: `cmd/a2acli` (main) · `internal/{config,auth,client,normalize,cli,ledger}` · `internal/selfupdate` · `internal/mocka2a` (mock A2A server on the SDK's a2asrv handlers) · `test/e2e` (50+ binary-level scenarios: auth walks, 401 refresh, busy envelopes, JSONL shapes, exit codes, continuation, artifacts, reconnect).

Built on the official [a2a-go](https://github.com/a2aproject/a2a-go) SDK v2.8 (A2A v1.0.1, JSON-RPC + REST transports). Not yet supported: push notification configs, agent card signature verification, and the gRPC binding. CI runs on every PR (linux race tests + windows/darwin cross-builds + SKILL.md lockstep check); tags cut checksum-verified releases via goreleaser, with darwin binaries signed + notarized when signing secrets are configured (see [docs/RELEASING.md](docs/RELEASING.md)).
