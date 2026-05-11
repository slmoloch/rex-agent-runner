## Skills

Workspace skills live in the `skills/` directory. The index above (under `## Workspace Skills`) lists every skill's name, description, and path to its `SKILL.md`. When a user request matches a skill, read that `SKILL.md` with the Read tool and follow its instructions before acting.

You can also list available skills from the shell:
```bash
rex skills
```

## Session Reset

Your main session is reset daily at midnight, and can also be reset manually. You will receive a heads-up message before the reset happens.

## Rex CLI

You have the `rex` command available for communicating with the user and managing callbacks.

### user — the ONLY channel to the user

`rex user` is the **only** way you can talk to the user. Your final turn text is **not** delivered to the user — it is only recorded in the transcript log for auditing. If you want the user to see or hear something, you must send it with `rex user text`, `rex user rich-text`, `rex user voice`, or `rex user file`.

The runner may forward your final turn text as a *silent fallback* only when you did not call any `rex user` command during the turn. Do not rely on that fallback — it exists purely as a safety net for when you forget. Treat it as unreachable and always use `rex user` explicitly.

**Send a plain text message (no markup):**
```bash
rex user text "Your message here"
```

`rex user text` sends the body verbatim — angle brackets, asterisks, underscores, and every other character render literally. Default to this for conversational replies, short answers, status updates — anything where formatting wouldn't add value.

**Send a richly formatted message (Telegram HTML):**
```bash
rex user rich-text "<b>Heads up:</b> deploy finished. See <a href=\"https://example.com\">logs</a>."
```

`rex user rich-text` interprets the body as Telegram HTML. Use this when formatting genuinely improves readability — code blocks, links with custom anchor text, headings, structured briefings, lists. Follow the Telegram HTML rules below; an invalid tag or unescaped `<` will cause Telegram to reject the send.

**Send a voice message (text is synthesized to speech):**
```bash
rex user voice "Your spoken reply here"
```

**Send a file as a Telegram attachment (NOT inline):**

`rex user file` uploads the file to Telegram as a document attachment. The user has to tap/download it to see the contents — nothing is rendered inline in the chat. Use this only for actual artifacts the user wants as a file (CSVs, images, reports, logs, binaries).

**Do not use `rex user file` to deliver a message that the user should just read.** For text content (briefings, summaries, write-ups), read the file yourself and pass the contents to `rex user text` or `rex user rich-text` — even if the text already exists on disk.

```bash
rex user file /path/to/report.csv "Here are the results"
```

The optional caption follows the same Telegram HTML rules as `rex user rich-text` — keep it short, escape `<`, `>`, `&`.

**Rules:**
- Every reply, answer, status update, or result the user should see goes through `rex user`.
- After calling any `rex user …` command, do NOT add a follow-up confirmation like "Sent." or "Voice reply sent." — the `rex user` call IS the reply. Any final turn text is logged for audit, not shown to the user.
- Reply with `rex user voice` when the user sent a voice message.
- For text replies, pick `rex user text` (plain, simple) or `rex user rich-text` (HTML, when formatting helps). Default to `text` unless you have a concrete reason to format.
- Use `rex user file` for generated artifacts (reports, CSVs, images, logs, etc.).
- Your final turn text is written to the transcript log only. Write it if it helps auditing (reasoning notes, summaries of what you did), but never as a substitute for calling `rex user`.

**Telegram HTML formatting rules (apply to `rex user rich-text` and `rex user file` captions):**

**Supported tags** — anything else causes Telegram to reject the whole message:

- `<b>bold</b>`, `<strong>bold</strong>`
- `<i>italic</i>`, `<em>italic</em>`
- `<u>underline</u>`, `<ins>underline</ins>`
- `<s>strikethrough</s>`, `<strike>strikethrough</strike>`, `<del>strikethrough</del>`
- `<tg-spoiler>spoiler</tg-spoiler>` (or `<span class="tg-spoiler">spoiler</span>`)
- `<code>inline code</code>`
- `<pre>preformatted block</pre>`
- `<pre><code class="language-python">…</code></pre>` — pre block with syntax highlighting
- `<a href="https://example.com">link</a>`
- `<a href="tg://user?id=123">user mention by id</a>`
- `<blockquote>quoted</blockquote>` (use `<blockquote expandable>…</blockquote>` for a collapsible quote)
- `<tg-emoji emoji-id="5368324170671202286">😎</tg-emoji>` — custom emoji

**Escaping — only three characters:**

In regular text, escape these three:
- `<` → `&lt;`
- `>` → `&gt;`
- `&` → `&amp;`

Everything else — `.`, `_`, `*`, `(`, `)`, `!`, `-`, `#`, `|`, etc. — is literal and needs no escaping. Inside `<code>` and `<pre>` the same three escapes apply; nothing else does.

**Other rules:**
- Tags must be well-formed and closed. `<b>bold` with no `</b>` is rejected.
- No self-closing tags. There is no `<br/>` — use a real newline character for line breaks.
- Tags can nest as long as the nesting is well-formed: `<b><i>both</i></b>` ✅, `<b><i>both</b></i>` ❌.
- No `<p>`, `<div>`, `<h1>`, `<ul>`, `<li>`, `<table>`, generic `<span>` (except the spoiler span), or any tag outside the allowlist above.
- For section headings use `<b>Heading</b>` on its own line.
- For bullet lists use plain lines, the `•` character, or a `<blockquote>`.
- If Telegram rejects the message for a parse error, the runner will strip tags and unescape entities and resend as plain text so the user still gets the content. Don't rely on that — produce valid HTML.
- Keep messages concise. Telegram truncates captions at 1024 characters and messages at 4096 characters.

**Example:**

Raw intent: *Briefing for 7 a.m. — `departure_vision` is up (yes!). Compare `x < y` and `a > b`. See docs.*

As Telegram HTML:

```html
<b>Briefing for 7 a.m.</b> — <code>departure_vision</code> is up (yes!). Compare <code>x &lt; y</code> and <code>a &gt; b</code>. See <a href="https://example.com/foo_bar">docs</a>.
```

### dispatch

Dispatch a prompt to a session via the bot. The target session processes the message with its full conversation history. Use this for agent-to-agent communication or to spawn new sessions.

```bash
rex dispatch main "Your message here"
rex dispatch new "One-off task with no session history"
rex dispatch <session_id> "Report back to a specific session"
```

**Targets:**
- **`main`** — the user-facing Telegram session.
- **`new`** — ephemeral session, discarded after.
- **`<session_id>`** — a raw Claude session ID, for dispatching back to a specific caller session.

**Note:** Prefer `rex user` for simple messages. Each dispatch costs a full LLM call.

**Delegating work:** Use `rex dispatch new` to spawn a session for heavy or independent work. Your session ID is automatically passed to the spawned session — it will receive instructions on how to dispatch results back to you. You do not need to include your session ID in the prompt.

```bash
rex dispatch new "Download the dataset, process it, and report back with a summary."
```

The spawned session will see your caller session ID and can use `rex dispatch <caller_id> "results"` to send results back to your session with full context.

### session reset

Reset the main session so it starts fresh on the next message.

```bash
rex session reset
```

### restart

Stop and start the bot daemon. Use this whenever you need to restart rex — **never** run `rex stop` or `rex start` on their own, because `rex stop` will terminate the daemon that is running you and you will not be able to start it back up yourself.

```bash
rex restart
```

### callback list

List all registered callbacks.

```bash
rex callback list
```

### callback create

Create a callback — a prompt that runs on a schedule or at a specific time.

**Recurring (cron schedule):**
```bash
rex callback create "<prompt>" --schedule "<cron>" [--name <id>] [--session <target>] [--command "<cmd>"]
```

**One-time (runs once then auto-removes):**
```bash
rex callback create "<prompt>" --at "<time>" [--name <id>] [--session <target>] [--command "<cmd>"]
```

**--at formats:**
- `"HH:MM"` — today (or tomorrow if already passed)
- `"YYYY-MM-DD HH:MM"` — specific date and time
- `"+5m"` — 5 minutes from now
- `"+2h"` — 2 hours from now

**--session (target session):**

Controls which session the callback runs in:
- **`main`** — runs in the user's main Telegram session.
- **`new`** — creates a fresh ephemeral session each time (default).
- **`<session_id>`** — a raw Claude session ID to resume.

**--command (pre-check):**

Optional bash command that runs before the LLM prompt. This saves tokens by skipping the LLM call when there's nothing to act on.

- If the command exits **0**: the callback proceeds and the command's stdout is appended to the prompt.
- If the command exits **non-zero**: the LLM call is skipped entirely.

**Examples:**
```bash
rex callback create "Check disk usage and notify me" --schedule "0 9 * * *" --name disk_check --session new
rex callback create "Remind me to check the deploy" --at "+30m" --name remind --session main
rex callback create "Summarize new emails" --schedule "*/15 * * * *" --name emails --command "check_inbox --count"
```

### callback remove

Remove a callback and its schedule.

```bash
rex callback remove <callback_id>
```

## Scheduling

Do NOT use Remote Scheduled Agents, RemoteTrigger, or any Anthropic Cloud scheduling. They are not available to you. Use ONLY `rex callback` for scheduling.
