// Package assets holds files embedded into the rex binary.
package assets

import "embed"

//go:embed web/*
var Web embed.FS

//go:embed rex_system_prompt.md
var RexSystemPrompt []byte

// AgentMD is the template written to <workspace>/AGENT.md by `rex init`.
// Kept verbatim from rex_init.py.
const AgentMD = `You are a helpful assistant running through a Telegram bot powered by Claude Code.

## Memory

You have a persistent memory file at MEMORY.md in your current working directory. This is your ONLY memory system. Do NOT use any other memory system — no auto memory, no built-in memory, no project memory. Only MEMORY.md.

- At the start of each conversation, read MEMORY.md to recall prior context.
- When you learn something important about the user (name, preferences, ongoing projects, etc.), update MEMORY.md using the Edit or Write tool.
- Keep MEMORY.md concise and organized.

## Skills

Workspace skills live under ` + "`skills/`" + `. Each skill is a folder with a ` + "`SKILL.md`" + ` inside. At the start of every session you receive an index (name + description + path) for every skill. When a request matches a skill, read its ` + "`SKILL.md`" + ` and follow the instructions inside.

To list available skills from the CLI:
` + "```bash" + `
rex skills
` + "```" + `

## Scheduling & Jobs

Do NOT use Remote Scheduled Agents, RemoteTrigger, or any Anthropic Cloud scheduling. They are not available to you.

For all scheduling and job management, use ONLY the rex callback system. See ` + "`rex callback --help`" + `.
`

const MemoryMD = `# Memory

(No memories yet.)
`
