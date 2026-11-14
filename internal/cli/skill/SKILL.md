---
name: a2acli
description: Delegate work to remote A2A (Agent2Agent) protocol agents via the a2acli CLI — enumerate agents, send messages, stream progress, follow/answer/cancel long-running tasks, fetch artifacts, and recall past delegations. Use whenever a task involves contacting an A2A agent/server, delegating work to a remote agent, checking a remote task's progress, or when the user mentions a2a, agent cards, or a registered agent alias.
---

# a2acli — delegate to A2A agents

`a2acli` is built for exactly this situation: you (an AI agent) handing work to a
remote agent and needing to trust what comes back. Every command accepts a
registered alias or a raw URL. Always use `--json` — outputs are normalized and
stable. This skill matches the CLI it shipped in; re-sync with `a2acli --skill --claude`
after `a2acli update`.

## Exit codes — branch on these, don't parse stderr

| code | meaning | your next action |
|---|---|---|
| 0 | message / completed / busy / truthful snapshot | read the JSON |
| 1 | task ended failed / cancelled / rejected, or the agent gave a definitive protocol error (task not found, unsupported operation/version, required extension) — stderr carries a hint | report failure / follow the hint |
| 3 | task paused **input-required** — the agent asked a question | answer: `send <alias> "<answer>" --task-id <id>` |
| 4 | task paused **auth-required** | supply/fix credentials, then continue with `--task-id` |
| 5 | observation failed (network/timeout, after retries) — **the remote task may still be running** | re-observe; do NOT assume the task died |
| 6 | credential failure (secret unresolvable, 401) | fix auth: `agent set-key`, env var, or `--secret` |

## 1. Discover who you can talk to

```bash
a2acli agents describe --json     # every registered agent + card summary (name, description, skills)
a2acli card <alias-or-url> --json # one agent's FULL card (describe truncates at 140 chars)
a2acli card <alias> --extended --json  # authenticated extended card (more skills), if advertised
a2acli agent list                 # registry rows only (alias, url, auth source)
```

`agents describe` never fails on unreachable agents — they carry an `error` field instead.

## 2. Send a message

```bash
a2acli send <alias> "message" --json
a2acli send <alias> "follow-up" --context-id <ctx> --json    # continue a conversation
a2acli send <alias> "long job" --return-immediately --json   # force a task handle, don't wait
a2acli send <alias> "medium job" --wait 30 --json            # poll up to 30s for completion
a2acli send <alias> "the answer" --task-id <id> --json       # continue/answer an existing task
a2acli send <alias> "review this" --file ./diff.patch --json # attach files (inline, ≤10 MiB, repeatable)
a2acli send <alias> "apply" --data '{"env":"prod"}' --json   # structured-data part (or --data @file.json)
a2acli send <alias> "report" --accept application/json --json # declare output media types you handle
```

The `--json` result is exactly one of three shapes, discriminated by `type`:

- `{"type":"message","text":...,"contextId":...}` — final answer, done.
- `{"type":"task","taskId":...,"contextId":...,"status":...,"text":...}` — long-running;
  follow via task commands. If `status` is `input-required`, `text` is the agent's
  question (exit code 3).
- `{"type":"busy","error":"context_busy","activeTaskId":...}` — the context is busy with
  another task (Agentlings servers). Wait and retry, or watch the active task.

Reuse `contextId` across sends to stay in one conversation.

## 3. Delegate-and-forget (the core loop)

```bash
# 1. Fire, get a handle immediately
a2acli send research "audit the flaky tests" --return-immediately --json   # -> taskId

# 2a. Block quietly until it settles — ONE final JSON object, nothing else
a2acli task wait research <task-id> --timeout 600 --json

# 2b. Or background the observation and tail the file
a2acli task watch research <task-id> --out /tmp/task.jsonl &   # JSONL per transition
a2acli task get research <task-id> --json                      # cheap point-in-time check

# 3. If exit code was 3, answer the question and wait again
a2acli send research "yes, include CI logs" --task-id <task-id> --json
a2acli task wait research <task-id> --json

# 4. Change your mind explicitly (Ctrl-C / dropped watch never cancels)
a2acli task cancel research <task-id>
```

`task wait` is built for you: silent while the task runs, one normalized object at
the end, honest exit code. `--timeout N` bounds every observe verb (exit 5 on
expiry; the task keeps running). Transient poll errors retry with backoff
automatically; statuses: `submitted`/`working` → running, `input-required`/
`auth-required` → paused on you, `completed`/`failed`/`cancelled`/`rejected` → terminal.

## 4. Your delegation memory: the ledger

Every task handle you have ever observed is recorded locally. You do not need to
remember task ids or which agent ran what:

```bash
a2acli task list --json               # everything: {ts,target,taskId,state,text}
a2acli task list --active --refresh   # still-running tasks, states re-polled now
a2acli task get <task-id> --json      # one arg: the agent is recalled from the ledger
a2acli task wait <task-id-prefix>     # unique prefixes work everywhere a task-id does
```

The ledger only knows what *this machine* observed. To ask the agent itself
(A2A ListTasks — includes tasks created elsewhere):

```bash
a2acli task list --remote <alias> --status working --json   # {tasks:[...],totalSize,nextPageToken}
a2acli task list --remote <alias> --context-id <ctx> --limit 20 --page-token <tok> --json
```

## 5. Streaming: live progress into JSONL

```bash
a2acli stream <alias> "run diagnostics" --out /tmp/run.jsonl
a2acli task subscribe <task-id>       # re-attach to a running task's stream
```

One JSON line per event: `event` is `task | status_update | artifact_update | message | busy`.
Status updates carry extension metadata verbatim (e.g. Agentlings tool progress:
which tool is EXECUTING/SUCCEEDED — activated automatically when the agent
advertises it). Activate other extensions with `--extension <uri>` (repeatable;
only sent if the card advertises it — `a2acli card` lists them). Non-streaming servers fall back to polling with
the same shapes; a dropped stream continues via polling automatically — exit codes
stay truthful either way.

## 6. Artifacts: the task's durable outputs

```bash
a2acli task get <alias> <task-id> --json           # .artifacts[] lists id/name/text/files/dataParts
a2acli task artifacts <task-id> --save ./outputs   # download text/bytes/data/URI parts to files
```

An agent that produced files puts them in artifacts, not the message text — always
check `artifacts` on completed tasks.

## 7. Protect your context window

```bash
a2acli --max-text 400 task wait <task-id> --json   # cap every text field; marker shows truncation
```

Use `--max-text` on watch/stream/wait when output could be huge; fetch full text
later with `task get` (uncapped) only if you need it.

## 8. Auth and secrets — never print them

Registered agents resolve their credential automatically: env →
OS keyring → 0600 secrets file, plus optional `secret_cmd` execution. When you must
supply a secret:

```bash
# inject via env for the session (name is uppercased: secret "decliner" => A2ACLI_SECRET_DECLINER)
export A2ACLI_SECRET_<NAME>=$(command-that-prints-the-key)

# or one-time override on any command — prefer cmd:/name: forms over literals
a2acli send <alias> "hi" --secret cmd:"vault-cli get key"
a2acli send <alias> "hi" --secret name:other-secret
```

Never echo a secret into output or logs; pass it via `$(...)` substitution or env only.
A 401 on a `secret_cmd` agent auto-refreshes once; a 401 on a static key means the key
is wrong — do not retry.

## 9. Register a new agent

```bash
a2acli agent add <alias> <url> --secret name:<secret-name>   # placement derives from the agent card
a2acli agent show <alias>                                    # prints the secret SOURCE, never the value
a2acli agent remove <alias>
a2acli update                                                # self-update, then: a2acli --skill --claude
```

## Rules of thumb

- Prefer `--return-immediately` + `task wait` over a long blocking `send`: you keep the handle.
- Interruption is not cancellation: killing a watch/stream/wait leaves the remote task
  running. Only `task cancel` cancels (and confirm with `task watch` — cancellation can
  be briefly non-terminal).
- Exit 5 means YOUR view failed, not the task — re-observe before drawing conclusions.
- stdout carries only the requested output; diagnostics go to stderr.
