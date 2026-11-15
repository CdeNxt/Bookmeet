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

## The apps (checked 2026-11-14)

| App | Connects to remote MCP | Interactive cards (MCP Apps) | Bookmeet tools in voice | Approval in voice | Pick-one prompts (elicitation) | Source |
|---|---|---|---|---|---|---|
| Claude (web, desktop, mobile) | Yes, as a connector | Yes (official MCP Apps client list) | Unreliable: reported broken, then working again on iOS; issue closed "not planned" | Not documented | Not in Claude.ai (open request) | [MCP Apps overview](https://modelcontextprotocol.io/extensions/apps/overview), [claude-ai-mcp#146](https://github.com/anthropics/claude-ai-mcp/issues/146), [#153](https://github.com/anthropics/claude-ai-mcp/issues/153) |
| ChatGPT | Yes (developer mode / apps) | Announced January 2026, but not on the official MCP Apps client list: verify with a real test | Yes, in Live voice (help page updated 2026-11-12) | On screen only: "spoken approval is not supported" | Not verified | [ChatGPT Voice](https://help.openai.com/en/articles/20001274-chatgpt-voice), [MCP Apps launch](https://blog.modelcontextprotocol.io/posts/2026-01-26-mcp-apps/) |
| Microsoft 365 Copilot, VS Code, Goose | Yes | Yes (official list) | n/a | n/a | VS Code: form and URL mode | MCP Apps overview |
| "Claude Duet", "Facebook Muse" | Unknown | Unknown | Unknown | Unknown | Unknown | J named these; not identified yet |

Both apps ask the person before a tool that is not marked read-only (ChatGPT per
conversation; Claude per tool, with "Allow always"). That is the app's prompt, not Bookmeet's,
and "Allow always" turns it off, so it is never treated as the owner's approval.

## What this means

- **Cards are built** (2026-11-14): `mcp_cards.go`. Approvals are a tap on the card
  (`owner_approve`, app-only). Checked against a stand-in host in a browser; still to check in
  real Claude and ChatGPT on staging.
- **Voice:** the owner can ask by voice where the app lets connectors work in voice; the
  approval is still a tap on the card on screen. Spoken approval was researched and rejected
  (J, 2026-11-14): nothing the AI writes down can prove the owner said it.
- Elicitation is not available in Claude.ai, so nothing depends on it. Text must stay
  complete on its own.
