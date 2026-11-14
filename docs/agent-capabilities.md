# What each AI app can show from Bookmeet

Bookmeet's assistant has two front ends over the same actions: Telegram (J's bot) and MCP,
which any AI app that speaks MCP can connect to (Claude, ChatGPT, and others). Each MCP reply
is drawn by the app, so what the owner sees depends on the app. This sheet records what each
app supports, so a new assistant feature can be made to look and feel as close to Telegram as
that app allows.

**Re-check this from each app's current documentation at the start of every assistant
feature.** These apps change monthly. Do not fill a row from memory.

## The three levels

| Level | What the owner sees | Built? |
|---|---|---|
| 3. Text | The agent's own words, from the tool's facts and `text` fields | Yes. Always works |
| 2. The app's own prompts | The app asks before a tool that changes something (from the tool's annotations); a pick-one question if the app supports MCP elicitation | Annotations yes; elicitation no |
| 1. A Bookmeet card in the chat | An interactive card with buttons, drawn by Bookmeet inside the app (MCP Apps) | No |

A button drawn inside an agent app is still pressed through the agent. It never counts as the
owner's own approval: exceptions to the owner's availability stay on Telegram, or a future
Bookmeet approval page.

## The apps (checked 2026-11-13)

| App | Connects to remote MCP | Interactive cards (MCP Apps) | Pick-one prompts (elicitation) | Asks before writes | Source |
|---|---|---|---|---|---|
| Claude (web, desktop) | Yes, as a connector | Yes, since 2026-01-26 | Not in Claude.ai (open request); Claude Code supports form mode | Yes | [MCP Apps launch](https://blog.modelcontextprotocol.io/posts/2026-01-26-mcp-apps/), [claude-ai-mcp#153](https://github.com/anthropics/claude-ai-mcp/issues/153) |
| ChatGPT | Yes (connectors; developer mode for write tools) | Yes, from late January 2026 | Not verified | Not verified | [MCP Apps launch](https://blog.modelcontextprotocol.io/posts/2026-01-26-mcp-apps/), [OpenAI community](https://community.openai.com/t/mcp-server-tools-now-in-chatgpt-developer-mode/1357233) |
| Microsoft 365 Copilot, VS Code | Yes | Reported yes (secondary source) | VS Code: form and URL mode | Not verified | [antlr-interactive](https://www.antlr-interactive.com/blog/mcp-apps), claude-ai-mcp#153 |
| "Claude Duet", "Facebook Muse" | Unknown | Unknown | Unknown | Unknown | J named these; not identified yet |

## What this means for the next steps

- Interactive cards work in both apps J named first, so a level 1 card for the details panel
  (meeting, days, times, Create link) is worth building. It is the closest match to Telegram's
  panel. Not built yet.
- Elicitation is not available in Claude.ai, so do not depend on it. Level 3 must stay
  complete on its own.
