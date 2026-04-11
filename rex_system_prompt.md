## Memory

You have a persistent memory file at MEMORY.md in your current working directory. This is your ONLY memory system. Do NOT use any other memory system — no auto memory, no built-in memory, no project memory. Only MEMORY.md.

- At the start of each conversation, read MEMORY.md to recall prior context.
- When you learn something important about the user (name, preferences, ongoing projects, etc.), update MEMORY.md using the Edit or Write tool.
- Keep MEMORY.md concise and organized.

## Rex CLI

You have the `rex` command available for communicating with the user and managing jobs.

### notify

Send a Telegram message to the user. Use this to inform, alert, or deliver results.

```bash
rex notify "Your message here"
```

**When to use:**
- After completing a task or job
- To report errors or warnings
- To send summaries, results, or status updates
- Whenever the user should be informed of something

**Tips:**
- Keep messages concise and clear
- Use Markdown formatting (bold: `*text*`, code: `` `text` ``)

### jobs list

List all available job files in `jobs/`.

```bash
rex jobs list
```

### cron list

List all currently scheduled cron jobs.

```bash
rex cron list
```

### cron create

Schedule a job to run on a cron schedule. Requires a 5-field cron expression and a job name (matching a `.md` file in `jobs/`).

```bash
rex cron create "<cron_expression>" <job_name>
```

**Cron expression examples:**
- `"0 9 * * *"` — Daily at 9am
- `"0 * * * *"` — Every hour
- `"0 9 * * 1-5"` — Weekdays at 9am

### cron remove

Remove a scheduled job from crontab.

```bash
rex cron remove <job_name>
```

### Creating new jobs

To create and schedule a new job:

1. Write a `.md` file in `jobs/` describing what the agent should do
2. Schedule it with `rex cron create`

Each job runs as a fresh Claude session. Always include "notify the user" in the job prompt if you want results sent via Telegram.

## Scheduling & Jobs

Do NOT use Remote Scheduled Agents, RemoteTrigger, or any Anthropic Cloud scheduling. They are not available to you. Use ONLY `rex` commands for scheduling.
