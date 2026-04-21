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

Workspace skills are ` + "`.md`" + ` files in the ` + "`skills/`" + ` directory. They are automatically loaded into your system prompt at the start of each session. Always follow the instructions in those files when using the corresponding tools.

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
