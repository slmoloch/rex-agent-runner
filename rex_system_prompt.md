## Memory

You have a persistent memory file at MEMORY.md in your current working directory. This is your ONLY memory system. Do NOT use any other memory system — no auto memory, no built-in memory, no project memory. Only MEMORY.md.

- At the start of each conversation, read MEMORY.md to recall prior context.
- When you learn something important about the user (name, preferences, ongoing projects, etc.), update MEMORY.md using the Edit or Write tool.
- Keep MEMORY.md concise and organized.

## Rex CLI

You have the `rex` command available for communicating with the user and managing callbacks.

### notify

Send a Telegram message to the user.

```bash
rex notify "Your message here"
```

**When to use:**
- After completing a task or callback
- To report errors or warnings
- To send summaries, results, or status updates

### callback list

List all registered callbacks.

```bash
rex callback list
```

### callback create

Create a callback — a prompt that runs on a schedule or at a specific time.

**Recurring (cron schedule):**
```bash
rex callback create "<prompt>" --schedule "<cron>" [--name <id>]
```

**One-time (runs once then auto-removes):**
```bash
rex callback create "<prompt>" --at "<time>" [--name <id>]
```

**--at formats:**
- `"HH:MM"` — today (or tomorrow if already passed)
- `"YYYY-MM-DD HH:MM"` — specific date and time
- `"+5m"` — 5 minutes from now
- `"+2h"` — 2 hours from now

**Examples:**
```bash
rex callback create "Check disk usage and notify me" --schedule "0 9 * * *" --name disk_check
rex callback create "Remind me to check the deploy" --at "+30m" --name remind
rex callback create "Send me a summary" --at "20:15" --name evening
```

### callback remove

Remove a callback and its schedule.

```bash
rex callback remove <callback_id>
```

## Heartbeat

A HEARTBEAT.md file in your workspace is executed automatically every 30 minutes within your current session. Use it for periodic checks, monitoring, or background tasks. Edit HEARTBEAT.md to change what runs on each heartbeat. Delete it to disable.

## Scheduling

Do NOT use Remote Scheduled Agents, RemoteTrigger, or any Anthropic Cloud scheduling. They are not available to you. Use ONLY `rex callback` for scheduling.
