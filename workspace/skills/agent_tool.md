# Agent Tool Skill

A CLI tool for communicating with the user via Telegram and managing scheduled jobs.

**Invoke with:**
```bash
python /Users/moloch/projects/claude-code-agent-runner/agent_tool.py <command> [args]
```

## notify

Send a Telegram message to the user. Use this to inform, alert, or deliver results.

```bash
python /Users/moloch/projects/claude-code-agent-runner/agent_tool.py notify "Your message here"
```

**When to use:**
- After completing a task or job
- To report errors or warnings
- To send summaries, results, or status updates
- Whenever the user should be informed of something

**Tips:**
- Keep messages concise and clear
- Use Markdown formatting (bold: `*text*`, code: `` `text` ``)
- For multi-line messages, use quotes: `notify "Line 1\nLine 2"`

## jobs list

List all available job files in `jobs/`.

```bash
python /Users/moloch/projects/claude-code-agent-runner/agent_tool.py jobs list
```

## cron list

List all currently scheduled cron jobs.

```bash
python /Users/moloch/projects/claude-code-agent-runner/agent_tool.py cron list
```

## cron create

Schedule a job to run on a cron schedule. Requires a 5-field cron expression and a job name (matching a `.md` file in `jobs/`).

```bash
python /Users/moloch/projects/claude-code-agent-runner/agent_tool.py cron create "<cron_expression>" <job_name>
```

**Cron expression examples:**
- `"0 9 * * *"` — Daily at 9am
- `"0 * * * *"` — Every hour
- `"*/30 * * * *"` — Every 30 minutes
- `"0 9 * * 1-5"` — Weekdays at 9am
- `"0 0 1 * *"` — First of every month at midnight

## cron remove

Remove a scheduled job from crontab.

```bash
python /Users/moloch/projects/claude-code-agent-runner/agent_tool.py cron remove <job_name>
```

## Creating new jobs

To create and schedule a new job:

1. Write a `.md` file in `jobs/` describing what the agent should do
2. Schedule it with `cron create`

**Job file tips:**
- Each job runs as a fresh Claude session with the AGENT.md system prompt
- Jobs have access to all tools including this agent tool
- Always include "notify the user" in the job prompt if you want results sent via Telegram
- Keep job prompts clear and specific about what to do and what to report
