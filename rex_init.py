#!/usr/bin/env python3
"""rex init - initialize a project directory and run config setup."""

import os
import sys
from pathlib import Path

REX_HOME = Path.home() / ".config" / "rex"
REX_PROJECT_FILE = REX_HOME / "project_dir"
INSTALL_DIR = Path(os.environ.get("REX_INSTALL_DIR", Path(__file__).parent))

AGENT_MD = """\
You are a helpful assistant running through a Telegram bot powered by Claude Code.

## Memory

You have a persistent memory file at MEMORY.md in your current working directory. This is your ONLY memory system. Do NOT use any other memory system — no auto memory, no built-in memory, no project memory. Only MEMORY.md.

- At the start of each conversation, read MEMORY.md to recall prior context.
- When you learn something important about the user (name, preferences, ongoing projects, etc.), update MEMORY.md using the Edit or Write tool.
- Keep MEMORY.md concise and organized.

## Skills

Workspace skills are `.md` files in the `skills/` directory. They are automatically loaded into your system prompt at the start of each session. Always follow the instructions in those files when using the corresponding tools.

To list available skills from the CLI:
```bash
rex skills
```

## Scheduling & Jobs

Do NOT use Remote Scheduled Agents, RemoteTrigger, or any Anthropic Cloud scheduling. They are not available to you.

For all scheduling and job management, use ONLY the agent tool described in `skills/agent_tool.md`. Jobs are `.md` files in the `jobs/` directory, scheduled via local cron using the agent tool.
"""

MEMORY_MD = """\
# Memory

(No memories yet.)
"""


def agent_tool_skill(install_dir: str) -> str:
    return f"""\
# Agent Tool Skill

A CLI tool for communicating with the user via Telegram and managing scheduled jobs.

**Invoke with:**
```bash
{install_dir}/venv/bin/python {install_dir}/agent_tool.py <command> [args]
```

## notify

Send a Telegram message to the user. Use this to inform, alert, or deliver results.

```bash
{install_dir}/venv/bin/python {install_dir}/agent_tool.py notify "Your message here"
```

**When to use:**
- After completing a task or job
- To report errors or warnings
- To send summaries, results, or status updates
- Whenever the user should be informed of something

**Tips:**
- Keep messages concise and clear
- Use Markdown formatting (bold: `*text*`, code: `` `text` ``)
- For multi-line messages, use quotes: `notify "Line 1\\nLine 2"`

## jobs list

List all available job files in `jobs/`.

```bash
{install_dir}/venv/bin/python {install_dir}/agent_tool.py jobs list
```

## cron list

List all currently scheduled cron jobs.

```bash
{install_dir}/venv/bin/python {install_dir}/agent_tool.py cron list
```

## cron create

Schedule a job to run on a cron schedule. Requires a 5-field cron expression and a job name (matching a `.md` file in `jobs/`).

```bash
{install_dir}/venv/bin/python {install_dir}/agent_tool.py cron create "<cron_expression>" <job_name>
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
{install_dir}/venv/bin/python {install_dir}/agent_tool.py cron remove <job_name>
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
"""


def main() -> None:
    project_dir = Path.cwd()
    install_dir = str(INSTALL_DIR)

    print(f"Initializing rex project in: {project_dir}")
    print()

    # Create directory structure
    workspace = project_dir / "workspace"
    dirs = [
        workspace,
        workspace / "jobs",
        workspace / "skills",
        workspace / "events",
        project_dir / "logs",
    ]
    for d in dirs:
        d.mkdir(parents=True, exist_ok=True)
        print(f"  Created {d.relative_to(project_dir)}/")

    # Scaffold files (don't overwrite existing)
    files = {
        workspace / "AGENT.md": AGENT_MD,
        workspace / "MEMORY.md": MEMORY_MD,
        workspace / "skills" / "agent_tool.md": agent_tool_skill(install_dir),
    }

    for path, content in files.items():
        if path.exists():
            print(f"  Skipped {path.relative_to(project_dir)} (already exists)")
        else:
            path.write_text(content)
            print(f"  Created {path.relative_to(project_dir)}")

    # Save project dir
    REX_HOME.mkdir(parents=True, exist_ok=True)
    REX_PROJECT_FILE.write_text(str(project_dir) + "\n")
    print(f"\n  Project dir saved to {REX_PROJECT_FILE}")

    # Run config setup
    print()
    import rex_config
    rex_config.CONFIG_PATH = project_dir / "config.json"
    rex_config.cmd_setup()


if __name__ == "__main__":
    main()
