# a2acli — Competitive Analysis & Path Forward

**Date:** 2026-07-04 · **Scope:** full competitive landscape for A2A client CLIs, a warts-and-all audit of a2acli at HEAD `073dd27`, an A2A spec (v0.3.0 → v1.0.1) edge-case review, and a prioritized roadmap.

---

## 1. Executive summary

**The good news: our positioning is validated as a real, open gap — not a crowded niche.** The A2A ecosystem in mid-2026 is large (150+ orgs, Linux Foundation, Azure/Bedrock/Google Cloud integrations, spec v1.0 stable since March 2026) but its client tooling is startlingly weak: the official inspector is web-only with no CLI mode, the official Go SDK's `cmd/a2a` is demo-grade, and every third-party CLI is either abandoned, chat-only, automation-hostile, or frozen at spec v0.3. **Nobody — official or third-party — is building for the agent-shelling-out-to-delegate use case.** Our JSON output contract, honest exit codes, JSONL streaming capture, keyring-backed multi-agent registry, and embedded SKILL.md have no equivalent anywhere in the ecosystem.

**The bad news: we don't yet live up to our own pitch.** The delegation story dead-ends at the protocol's own pause states (no way to answer an `input-required` task), artifacts — the spec's designated channel for durable outputs — are silently dropped, `send --wait` exits 0 on failed tasks (contradicting our "honest exit codes" headline), long watches die on a single network blip because there is no retry/backoff anywhere, we speak only one of the protocol's three transports, and the binary doesn't compile on Windows.

**The strategic clock: roughly 6–12 months.** The MCP ecosystem's history says the official tooling gap closes within a year of protocol breakout — MCP's inspector grew a CLI mode and community CLIs consolidated on that timeline. The official `a2a-go` CLI is the most dangerous future competitor (same language, official channel, rides every SDK release). We should ship the credibility fixes (§6, P0) and the protocol-breadth moat (P1) before it grows verbs.

---

## 2. Ecosystem context

- A2A was donated by Google to the Linux Foundation (mid-2025). At its one-year mark (April 2026): 150+ supporting organizations, ~24.6k GitHub stars on the core repo, production SDKs in 5 languages, integrations in Azure AI Foundry, Copilot Studio, Amazon Bedrock AgentCore, and Google Cloud, plus the companion AP2 payments protocol.
- **Spec v1.0.0 (2026-03-12, current v1.0.1)** is a *hard-breaking* rewrite of the wire format: ProtoJSON serialization, `kind` discriminators removed, JSON-RPC methods renamed to PascalCase, flattened Parts, signed agent cards (JWS over JCS), multi-tenancy, version negotiation via `A2A-Version`, fully-specified `ListTasks`, modernized OAuth (device-code flow added *explicitly for CLIs*; implicit/password deprecated).
- **Transport reality:** JSON-RPC 2.0 + SSE dominates deployments; gRPC and REST are co-equal in v1.0 and the official TCK tests all three. Most client tools are already behind the spec.
- **Our baseline is v1.0.** a2acli builds on `a2a-go` v2 — the official v1.0 SDK — so we are natively on the current spec, unlike the mostly v0.3-frozen competitor field. Some deployed agents still speak v0.3.x; interop with them is an optional scoping decision (§7), not our anchor.

---

## 3. Competitive landscape

### 3.1 Official A2A project tooling

| Tool | What it is | Threat |
|---|---|---|
| **a2a-inspector** (443★, v0.1.0 Jun '25) | Web UI debugger: card inspection, compliance checks, live chat, raw JSON-RPC console. **No CLI mode**, not scriptable, no exit codes, no registry. | Medium — owns "debugging" mindshare, but doesn't serve automation. |
| **a2a-go `cmd/a2a`** (SDK 415★, v2.3.1, active) | Official Go SDK's bundled CLI: `discover`, `send`, `serve --exec` (wrap a local script as an agent — genuinely clever). Multi-transport via SDK. No registry, no config, no auth, no task verbs, no output contract. | **High** — same language, official channel; the one most likely to grow into a real competitor. |
| **a2a-samples CLI hosts** (repo 1.7k★) | Python/JS demo chat clients; what most tutorials tell newcomers to use. Clone-and-run, zero operational features. | Low-medium — incumbent "first CLI experience." |
| **a2a-tck** | Official server conformance suite (all three transports; flags "false advertising" — card claims vs behavior). Not a client. | None — but a demand signal: card-vs-reality validation matters to this ecosystem. |
| **a2a-python / a2a-js SDKs** (2k★ / 567★) | No client CLI in either. | Confirms the gap in the two biggest language ecosystems. |

### 3.2 Third-party CLIs

| Tool | Snapshot | vs us |
|---|---|---|
| **chrishayuk/a2a-cli** (PyPI `a2a-cli`, 27★, v0.2 Aug '25) | Interactive chat + send/watch/get/cancel, multi-server config, rich TUI. Frozen at spec v0.3.0. | Squats the obvious PyPI name; **no auth, no JSON contract, no exit codes**, slow-moving. |
| **ericabouaf/a2a-cli** (npm `a2a-cli`, 0★) | chat/send/get/cancel; stdin piping and file input (which we lack). Weekend project. | Namespace holder only. |
| **cnoe-io/agent-chat-cli** (org-backed, 153 commits, v0.2.16 Nov '25) | Best-engineered third-party client: robust SSE handling of all three event types with fallback, Bearer+TLS auth, Docker/uvx. | Chat-only; env-var auth; **no machine-readable output**; no task verbs; not built for agent-driving-agent. |
| **python-a2a** (themanojdesai, **999★**) | Kitchen-sink framework (server+client+UI+networks+MCP bridge) with a CLI appendage. Own protocol implementation, unclear v1.0 fidelity. | Biggest mindshare; spec-drift risk grows as the official spec moves. Medium-high threat. |
| **capiscio** (v2.7.0 May '26, active) | Validator CLI: card schema, multi-transport endpoint probing, **JWS signed-card verification with JWKS fetching**, JSON output. Not a client (no send/tasks/streaming). | Complementary — but proves demand for signed-card verification and JSON discipline at the CLI layer. |
| **shamblett/a2a (Dart)** | SDK + CLI; the only third-party client touching **push notifications** and `resubscribe`. Tiny audience. | Feature-signal, not a threat. |
| Long tail | artinet CLI (archived Jan '26), llmx desktop validator, a2a-cli-llm, VS Code client, Rust crates (no CLI yet), scaffolding CLIs (adl-cli etc.) | All sub-30★; graveyard confirms the space is hard to sustain without a wedge. |

### 3.3 Frameworks & generic tools

- **ADK, Semantic Kernel, CrewAI, AG2** have native A2A client *libraries* — no CLIs. **LangGraph has nothing built-in.** NVIDIA NeMo ships an A2A CLI plugin with OAuth, but locked inside the `nat` toolchain. Frameworks solve A2A consumption *inside their own apps*; they don't serve shell-native delegation from coding agents, ops/debugging, or cross-framework smoke testing. That's our lane, and it's untouched.
- **The real incumbent is `curl | jq` (+ `grpcurl`)** — it's what kagent, AWS Bedrock AgentCore, and LangSmith docs actually teach. It breaks down exactly where we're strong: SSE resubscription, task lifecycle, auth headers, exit codes.
- **Postman/Insomnia have no first-class A2A request type** (Postman has one for MCP). Another confirmation the client-tooling gap is real.

### 3.4 The MCP precedent

MCP's inspector shipped web-first; community CLIs (wong2/mcp-cli, mcp-tools) filled the scriptability gap and won by being (a) zero-install and (b) callable by coding agents — then the official inspector added a CLI mode and the space consolidated, all within ~a year. A2A today is where MCP was in early 2025: **the scriptable-client seat is empty, and the window is open now, not forever.**

---

## 4. Competitive advantages (validated against the field)

Every one of these was checked against the full landscape; none has an equivalent elsewhere:

1. **Closed JSON output contract + honest exit codes.** Only capiscio has disciplined JSON output, and it's a validator. Every other client is TUI/chat-first and automation-hostile.
2. **Delegation semantics as first-class design** — `--return-immediately`, `--wait`, `task watch` with "interruption is not cancellation." No competitor even gestures at the fire-a-task-and-disconnect workflow.
3. **JSONL streaming capture to file** (`--out`) — unique; the tail-a-file pattern is exactly what an orchestrating agent needs.
4. **Multi-agent registry with aliases + per-agent keyring-backed credentials.** Nobody else has OS-keyring secrets; most competitors have *no auth at all*.
5. **Embedded SKILL.md installable into Claude/Codex harnesses** — no competitor gestures at coding-agent consumption; MCP history says this is the decisive adoption channel.
6. **Single static binary + checksum-verified self-update.** Python competitors drag a runtime; the official Go CLI requires a Go toolchain.
7. **Quality floor**: clean layering (cli → client → SDK, `normalize` as an explicit contract package), race-clean, ~30 e2e scenarios against a real mock server, secret non-leakage tested, zero TODO debt. Most competitors have no tests at all.

---

## 5. Competitive gaps & warts (the honest list)

### 5.1 Protocol-semantics holes (worst first)

1. **Cannot answer `input-required` / `auth-required` tasks.** `Send` never sets `Message.TaskID`, so a paused task cannot be continued — the async-task story dead-ends at the protocol's own pause states. Watch just polls such tasks forever (no timeout). This is the single biggest hole given our positioning. (`internal/client/ops.go:17`)
2. **Artifacts are a black hole.** `TaskView` has no artifacts field; stream `artifact_update` events keep only text parts and drop artifact id/name/mime; `FileWithBytes`/`FileWithURI`/`DataPart` contents are silently discarded everywhere; there is no download command. Per spec §3.7, *artifacts are the durable output channel and messages are explicitly unreliable* — an agent that returns a file produces empty output from us. (`internal/normalize/normalize.go:85-96,218-231`)
3. **`send --wait` lies.** Exit code is 0 even when the task ends `failed`, and mid-wait `GetTask` errors are swallowed, silently returning a stale snapshot. This directly contradicts the README's "honest exit codes" promise. (`internal/cli/send.go:63-86`)
4. **No resilience for the long-running use case.** Fixed-interval polling with no retry, no backoff, no jitter: one transient blip kills a `task watch` with exit 1. Streams have **no reconnect** (never falls back to `tasks/subscribe` on drop), and a stream ending at a *non-terminal* state exits 0 — a server closing mid-task looks like success to `&&` pipelines. No `--timeout` anywhere; `--interval 0` is accepted and busy-loops. Card discovery and RPCs on the main paths have no connect timeout at all (a black-holed host hangs until Ctrl-C).
5. **JSON-RPC transport only.** The SDK we already ship (a2a-go v2.3.1) supports REST and gRPC; we register only `WithJSONRPCTransport`. Agents whose cards advertise only gRPC or REST interfaces are unusable. (`internal/client/client.go:72-77`)
6. **~50% of the protocol surface unimplemented**, all available in our SDK: `tasks/list` (undercuts "built for delegation" — you can't enumerate your outstanding delegations), all four push-notification-config operations, `agent/getAuthenticatedExtendedCard`, `historyLength`.
7. **Card handling is shallow.** Discovery is always unauthenticated (bare `http.Client`, so auth-protected cards are undiscoverable); no caching (1–3 extra round trips per invocation; spec v1 §8.6 defines ETag/Cache-Control behavior); total discovery failure silently assumes the URL is a JSON-RPC endpoint (a typo yields confusing downstream errors). Ignored card fields: `capabilities.pushNotifications`, `capabilities.extensions` (we never send `X-A2A-Extensions` — required extensions will hard-fail us with -32008 on v1 servers), `signatures` (no JWS verification), input/output modes, `supportsAuthenticatedExtendedCard`.
8. **Message composition is text-only.** One user-role text part, always. No file parts, no data parts, no stdin, no `--file`, no metadata, no `referenceTaskIds`. Even the 0-star npm competitor supports stdin and file input.

### 5.2 Auth gaps

- No OAuth2 flows (no device-code — which spec v1.0 added *specifically for CLIs* — no client-credentials, no PKCE); the whole OAuth story is "bring your own token fetcher via `cmd:`". No mTLS (cards requiring it silently fall through to an `X-API-Key` guess). No TLS knobs (self-signed lab servers unusable).
- **Nondeterministic auth placement** when a card requirement lists multiple schemes (Go map iteration, `internal/auth/placement.go:46-48`).
- 403 triggers the token-refresh retry as well as 401 (`transport.go:45`) — a permission error burns a pointless token re-fetch.
- Foot-guns: verbatim env lookup means a secret named `HOME` resolves to `$HOME`; naive `$secret` substring templating mangles `$secretx`; secrets injected into plain `http://` with no warning; literal `--secret` lands in shell history.
- Genuinely good: config never contains raw secrets, `agent show`/`list` never print values, e2e proves non-leakage, 401-refresh-once for command sources.

### 5.3 Engineering warts

- **Windows does not compile** (`syscall.SysProcAttr{Setsid: true}`, `internal/cli/update.go:167`) — and nothing noticed, because CI is Linux-only (macOS binaries ship untested too). Also Windows-hostile: `sh -c` secret commands, Unix permission checks, POSIX-only installer.
- **Non-atomic config/secrets writes** (`O_TRUNC` in place) — a crash mid-write destroys them; ironically the skill file and update-state writes *are* atomic.
- `--json` ignored by `agent add/remove/set-key` and `update`; `card --json` leaks the raw SDK struct (churns with SDK upgrades, unlike every other output).
- Busy-envelope detection is heuristic — any agent message that happens to be JSON with `status:"busy"` + `error` is reinterpreted as backpressure (false-positive risk on generic A2A servers).
- The `canceled`→`cancelled` respelling is a deliberate contract but a trap for users grepping raw protocol output (spec v1.0 standardized *American* spelling).
- Update chain: `A2ACLI_REPO` env silently redirects check *and* download with checksums from the attacker-controlled repo; artifacts unsigned (no cosign); pre-release tags (`v1.0.0-rc1`) compare as *older* than any release.
- Exit codes are only 0/1 — no differentiation of network vs auth vs usage vs task-failure despite the plumbing existing.
- SKILL.md has drifted: it doesn't mention `stream`/`task subscribe` (added in PR #5) despite promising to match the shipped CLI, and documents `input-required` with no recipe (because none exists).
- README advertises roadmap items as if near (chat mode, TUI add, detached delegation handles) that don't exist.
- Test blind spots: zero unit tests in `internal/cli`; the artifact-update path has **no coverage anywhere** (mock never emits artifact events); Ctrl-C semantics (our marquee feature) untested; `input-required` untested; legacy card fallback untested; `install.sh` unlinted.

---

## 6. Spec review: edge cases to exploit (and traps we're currently in)

The spec dossier surfaced 25 client-conformance traps; these are the ones that matter most for us:

**Traps we are currently caught in:**
- **No task continuation** — spec v1 §3.4.3 designs `input-required` continuation as same-taskId send; we can't do it (gap #1).
- **Artifacts-are-the-output** (v1 §3.7): messages are explicitly *not* a reliable delivery channel; we only render message text (gap #2).
- **Blocking-flag inversion**: v0.3 `blocking: true` ⇔ v1.0 `return_immediately: false` (default blocking). We ride the SDK here — must be explicit in our own contract and never map 1:1 across versions. Also: servers MAY reject blocking for long tasks, so a "blocking" send can still legally return a `working` snapshot — our `--wait` path must treat that as normal.
- **Stream end ≠ task end**: EOF without a terminal state must mean "resubscribe or poll", never success — we currently exit 0 (gap #4). v1 servers MAY resend a final Task snapshot before close (dedupe it); v0.3 sends `final:true` on `input-required` even though the task is continuable.
- **Mid-stream JSON-RPC error frames**: on the JSON-RPC binding, each SSE `data:` is a full JSON-RPC response — errors can arrive *inside* the stream; parse the `error` member per event.
- **Required extensions** (v1): if a card lists an extension with `required: true` and we don't declare it via the `A2A-Extensions` header, servers MUST fail us with -32008. We never send the header.
- **`canceled` spelling**: v1.0 standardized American spelling; our British respelling in JSON output is a permanent contract quirk to document loudly (or reverse before v1 adoption hardens).

**Opportunities nobody else handles (differentiation via correctness):**
- **v1.0-native correctness** — the single biggest spec-derived opportunity. The spec is 1.0, and we're one of the only clients built on the v1.0 SDK; most competitors are frozen at 0.3 and face a hard-breaking migration (PascalCase methods, `kind` discriminators removed, flattened Parts, ProtoJSON enum casing, restructured card) that we've already crossed. The moat is *depth* of v1.0 conformance: `A2A-Version` sent on every request, -32601/-32009 handled with graceful fallback, `tenant` propagation, correct ProtoJSON shapes end-to-end. Verify exactly what a2a-go v2.3.1 already covers and fill the gaps in our layer. Optional 0.3 interop for legacy agents can follow if demand shows up — it's a feature, not the anchor.
- **`ListTasks`** (v1, all transports, cursor pagination, `includeArtifacts`) → `a2acli task list`, plus a *local task ledger* to cover 0.3 agents where JSON-RPC has no list method at all. A local ledger of every task handle we've ever received is cheap and uniquely valuable for the delegation story ("what did I delegate, where, and what state is it in?").
- **Subscribe-snapshot guarantee** (v1 §3.1.6: first event MUST be a full Task snapshot) + `tasks/get` reconciliation after any reconnect — the correct reconnect loop is: resubscribe with backoff, reconcile via GetTask, dedupe. That's the engine our `watch`/`stream` resilience fix should be built on.
- **Multi-tenancy** (v1): copy `AgentInterface.tenant` into every request or break against multi-tenant gateways — brand-new surface no other CLI handles.
- **Signed-card verification** (JWS over JCS, v1 §8.4): capiscio monetizes exactly this; "verify before you delegate" is becoming a standard step. A `--verify-card` / `a2acli doctor` fits a proven need.
- **Device-code OAuth** (v1, RFC 8628): the spec added it explicitly for CLI tools; no client CLI implements it.
- **Push notifications**: only the Dart SDK client touches them. For fire-and-disconnect delegation, `task push add/list/delete` plus (later) an `a2acli listen` webhook receiver is the highest-value unclaimed feature — noting well-behaved servers SSRF-reject localhost webhooks, so a tunnel/relay story matters.
- **REST error parsing**: multiple A2A errors share HTTP 400; clients must read `google.rpc.ErrorInfo.reason`. Getting this right cheaply differentiates our REST transport when we add it.
- **Coming soon to architect for**: `SendLiveMessage` bidirectional streaming (PR #1975), official conformance-test spec (PR #1882), registered extensions (payments, RFC 9421 message signing), `QuerySkill()` on the roadmap.

---

## 7. Path forward

### P0 — Credibility: make the pitch true (do before promoting the tool)

These are the items where our README writes checks the binary doesn't cash.

1. **Task continuation**: `a2acli send <target> <msg> --task-id <id>` setting `Message.TaskID` (+ `--reference-task <id>` for refinements). Teach `watch` to exit distinctly (dedicated exit code) on `input-required`/`auth-required` with the question text, so an orchestrating agent can respond. Update SKILL.md with the answer-a-paused-task recipe.
2. **Artifacts**: add artifacts to `TaskView`; render file/data parts (name, mime, size) in normalize instead of dropping them; `a2acli task artifacts <target> <id>` to list and `--save <dir>` to download (bytes + uri fetch); include artifact id/name/mime in stream `artifact_update` JSONL; correct chunk reassembly (`append`, `lastChunk`, keyed on taskId+artifactId).
3. **Fix `send --wait`**: honest exit code on failed/cancelled/rejected; stop swallowing poll errors; share one poll engine with `watch`.
4. **Resilience engine**: retry with exponential backoff + jitter on transient errors in watch/wait/get; stream/subscribe reconnect via `tasks/resubscribe` + `tasks/get` reconciliation (per v1 §3.1.6 snapshot guarantee); `--timeout` on watch/stream/wait; non-terminal stream end exits non-zero (or reconnects); validate `--interval > 0`; default connect timeouts everywhere.
5. **Windows**: fix the `Setsid` compile break with build tags, add windows + macOS to CI (even just build). Decide the support tier explicitly rather than by accident.
6. **Contract hygiene**: `--json` on all mutation commands; normalized `card --json`; atomic config/secrets writes; differentiated exit codes (usage / network / auth / task-failed / input-required); document or revert the `cancelled` respelling.
7. **SKILL.md lockstep**: cover stream/subscribe/continuation; add a CI check that fails when the CLI surface and SKILL.md drift.

### P1 — Moat: protocol breadth nobody else has (next 1–2 quarters)

8. **`task list`**: v1 `ListTasks` (pagination, `--context-id`, `--status`, `--include-artifacts`) + a local task ledger (every handle we ever received, with target/context/last-known-state) so listing works against 0.3 agents too. The ledger also enables `a2acli task watch --all`.
9. **v1.0 conformance depth**: audit what a2a-go v2.3.1 handles (`A2A-Version` header on every request, `tenant` propagation from `supportedInterfaces`, ProtoJSON enum casing, error-detail parsing) and fill gaps in our layer; extend the mock and e2e matrix with v1.0 shapes. Handle -32008 (declare extensions via `A2A-Extensions`) and -32009 (version renegotiation). Treat v0.3 interop for legacy agents as an optional, demand-driven follow-up, not a requirement.
10. **Multi-transport**: wire the SDK's REST transport now (cheap), gRPC next; select per card `supportedInterfaces` order with fallback on failure. First mover among CLIs.
11. **Card depth**: authenticated card discovery (use the auth transport); `card --extended` (`GetExtendedAgentCard`); card caching honoring ETag/Cache-Control; `--verify-card` JWS signature verification; loud failure instead of the silent direct-endpoint guess.
12. **Push notifications**: `task push add/list/delete` (SDK has all four ops); later, `a2acli listen` webhook receiver with token validation and idempotent dedupe.
13. **Rich input**: message from stdin, `--file` attachments (bytes/uri), `--data` for DataParts, `--history-length` on `task get`.
14. **Auth breadth**: OAuth2 device-code flow (spec-blessed for CLIs) + client-credentials with token cache; mTLS client certs; deterministic multi-scheme placement (ordered, documented); restrict 401-only refresh; `--ca-cert`/`--insecure` for lab use; plaintext-http warning.
15. **`a2acli doctor <target>`**: lightweight card/endpoint/capability sanity checks (card-vs-behavior "false advertising" à la TCK) — proven demand, cheap to build on what we have.

### P2 — Distribution & polish

16. **Naming/channels**: `a2a-cli` is squatted on PyPI and npm — lean into `a2acli` as the brand; ship a Homebrew tap, install shell completions from install.sh, publish a Docker image, consider `npx`/`uvx`-equivalent zero-install story (MCP history says zero-install was decisive).
17. **Supply chain**: sign artifacts (cosign), remove or gate `A2ACLI_REPO`, fix pre-release semver comparison, show release notes on update.
18. **Busy envelope**: gate the Agentlings heuristic behind config/flag or tighten detection to avoid false positives on generic agents.
19. **Test debt**: artifact-update path coverage, Ctrl-C semantics, `input-required` flows in mock + e2e, legacy `agent.json` fallback, shellcheck for install.sh, coverage tracking, golangci-lint.
20. **Optional human UX**: a minimal `chat` mode (every human-facing reviewer will look for one) — but only after P0/P1; our wedge is automation, not chat.

### What we deliberately don't chase

- **Web UI / desktop app** — a2a-inspector owns it; complementary, not our lane.
- **Full validator/TCK** — capiscio and a2a-tck exist; `doctor` is the 20% that pays.
- **Agent frameworks/networks/routing** (python-a2a's territory) — kitchen-sink breadth is how projects in this space died.

---

## 8. Bottom line

We picked the right lane: the shell-native, automation-first A2A client for agent-to-agent delegation, with the only credible auth story and the only coding-agent skill packaging in the ecosystem. The field is fragmented, mostly spec-frozen, and chat-oriented; the incumbent is `curl | jq`. But today our own binary betrays the pitch in four places — paused tasks can't be answered, artifacts vanish, `--wait` lies about failure, and one network blip kills a watch. Those are weeks of work, not months, and they're all in the SDK we already ship. Fix P0, then spend the moat budget on v1.0 conformance depth, `task list` + local ledger, transports, and push notifications — the spec-correctness features no v0.3-frozen competitor is positioned to match — before the official Go CLI wakes up.
