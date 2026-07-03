---
name: a2acli
description: Talk to A2A (Agent2Agent) protocol agents via the a2acli CLI — enumerate registered agents, send messages, follow long-running tasks, and cancel them. Use whenever a task involves contacting an A2A agent/server, delegating work to a remote agent, checking a remote agent task's progress, or when the user mentions a2a, agent cards, or a registered agent alias.
---

# a2acli — generic A2A agent client

`a2acli` is a scriptable client for A2A protocol servers. Every command accepts a
registered alias or a raw URL. Always prefer `--json` — outputs are normalized and
stable. This skill matches the CLI it shipped in; re-sync with `a2acli --skill --claude`
after upgrades.

## 1. Discover who you can talk to

```bash
a2acli agents describe --json     # every registered agent + card summary (name, description, skills)
a2acli card <alias-or-url> --json # one agent's FULL card (describe truncates at 140 chars)
a2acli agent list                 # registry rows only (alias, url, auth source)
```

`agents describe` never fails on unreachable agents — they carry an `error` field instead.

## 2. Send a message

```bash
a2acli send <alias> "message" --json
a2acli send <alias> "follow-up" --context-id <ctx> --json    # continue a conversation
a2acli send <alias> "long job" --return-immediately --json   # force a task handle, don't wait
a2acli send <alias> "medium job" --wait 30 --json            # poll up to 30s for completion
```

The `--json` result is exactly one of three shapes, discriminated by `type`:

- `{"type":"message","text":...,"contextId":...}` — final answer, done.
- `{"type":"task","taskId":...,"contextId":...,"status":...}` — long-running; follow up via task commands.
- `{"type":"busy","error":"context_busy","activeTaskId":...}` — the context is busy with
  another task (Agentlings servers). Wait a few seconds and retry, or watch the active task.

Reuse `contextId` across sends to stay in one conversation.

## 3. Follow long-running tasks

```bash
a2acli task get   <alias> <task-id> --json                    # one snapshot
a2acli task watch <alias> <task-id> --interval 2 --out /tmp/task.jsonl   # poll to terminal state
a2acli task cancel <alias> <task-id> --json                   # explicit cancellation
```

- Statuses: `submitted`, `working`, `input-required` → non-terminal; `completed`,
  `failed`, `cancelled`, `rejected` → terminal.
- `task watch` exits 0 on `completed`, non-zero on failed/cancelled (add
  `--no-fail-on-task-failure` to suppress). `--out` appends one JSON line per state
  transition — background the watch and tail the file instead of blocking:

```bash
a2acli task watch <alias> <task-id> --out /tmp/task.jsonl &
tail -n 5 /tmp/task.jsonl
```

- Cancellation is always explicit. Interrupting a watch does NOT cancel the remote task.
- After `task cancel`, the state may still read `working` briefly; confirm terminal
  state with `task watch`.

## 4. Auth and secrets — never print them

Registered agents resolve their credential automatically: env →
OS keyring → 0600 secrets file, plus optional `secret_cmd` execution. You rarely need
to touch auth. When you must supply a secret:

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

## 5. Register a new agent

```bash
a2acli agent add <alias> <url> --secret name:<secret-name>   # placement derives from the agent card
a2acli agent show <alias>                                    # prints the secret SOURCE, never the value
a2acli agent remove <alias>
```

## Exit codes

0 = success (watch: task completed). 1 = errors, and watch on failed/cancelled tasks.
Error text goes to stderr; stdout carries only the requested output.
