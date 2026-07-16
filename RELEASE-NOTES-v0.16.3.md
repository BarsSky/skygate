# v0.16.3 — "more HTML" pass for /help

2026-07-16

The v0.16.1/v0.16.2 "more HTML" pass left `/help` in
plain text, so the catalog's markdown backticks
(`<id>`, `<target>`, `<key>`, etc.) showed up as
literal characters in the chat. This release applies
the same treatment to `/help` so the command reference
reads like the rest of the bot's structured replies.

## What changed

### 1. Catalog: backticks → `<code>` (37 entries)

A one-off script (`convert_help_backticks.py`) walks
every `bot.help.*` key in `internal/i18n/catalog.go`
and rewrites `` `X` `` → `<code>X</code>`. Inside the
`<code>`, the content is HTML-escaped (so `<id>`
becomes `<code>&lt;id&gt;</code>` which renders as
monospace `<id>`).

37 RU + EN entries were converted, including:
- `bot.help.auth_login`: `` `<key>` `` → `<code>&lt;key&gt;</code>`
- `bot.help.user_top_add_rule`: `` `<target>` [deny] `` → `<code>&lt;target&gt; [deny]</code>`
- `bot.help.user_rest_clearrules`: `` `/clearrules confirm` `` → `<code>/clearrules confirm</code>`
- `bot.help.admin_top_exit_nodes_health`: `` `/exit_nodes` `` → `<code>/exit_nodes</code>`
- ... and 33 more

The script is one-off; not committed. The script's
rationale is preserved in the catalog's existing
"// 2026-07-15: v0.14.0 — help layout" block.

### 2. helpReply: tabular `<pre>` per section

`internal/telegram/commands.go` (1 function rewrite,
~150 lines):

| Section | Old format | New format |
|---------|-----------|-----------|
| Auth | `  /login  bind chat...` (proportional) | `<b>🔐 Auth — ...</b>` + `<pre>/login            bind chat...</pre>` |
| User-scope | same | `<b>✦ User data — ...</b>` + `<pre>` block |
| Admin | same | `<b>🛠 Admin — ...</b>` + `<pre>` block |

Each section is now a tabular `<pre>` block (Telegram
uses a fixed-pitch font for `<pre>`, so the columns
line up on every client). The command column is
padded to 20 chars (max command is `/exit_nodes_health`
at 17 chars + 3-char margin); the description column
is free-form.

`markHTMLReply()` is called at the top of `helpReply`
so `parse_mode=HTML` is set on the `sendMessage`
payload. (The other 8 HTML-using replies already do
this since v0.16.2.)

## Before / after

### Before (v0.16.2, plain text with backticks):

```
🪶 ═══ Skygate ═══
📖 The Codex

🪶 Bot codex (command reference)
🛡️  Commands below are grouped by section. ...

🔐 Auth — chat binding
  /login              bind this chat to your skygate account  [args: `<key>`]
  /start              same welcome as a brand-new chat ... [args: `[<key>`]`]
  /lang               show this chat's current language; `/lang ru|en` ...
  /help               this list, or detailed help for one (e.g. `/help ack`)
  /version            build, runtime, schema level
  /unbind_self        drop your own binding (no admin needed)

✦ Your data — rules, devices, defaults
  /my_status          your rules, devices, last ACL
  /my_rules           see your existing exit-rules
  /my_quota           your rule count vs cap
  ...

🛠 Admin — tailnet-wide operations
  /status             system-wide summary (rules/users/last acl)
  /nodes              every tailnet device by user+tag
  /exit_nodes_health  exit-node health ... — see also `/exit_nodes` ...
  ...
```

(Bullet on screen: the backticks `\`<key>\``, `\`<target>\``
are raw markdown that the bot didn't apply.)

### After (v0.16.3, HTML tabular):

```
🪶 ═══ Skygate ═══
📖 The Codex

<b>Bot codex (command reference)</b>
<i>🛡️  Commands below are grouped by section. ...</i>

<b>🔐 Auth — chat binding</b>
<pre>/login                bind this chat to your skygate account  [args: <code>&lt;key&gt;</code>]
/start                same welcome as a brand-new chat ... [args: <code>[&lt;key&gt;]</code>]
/lang                 show this chat's current language; <code>/lang ru|en</code> ...
/help                 this list, or detailed help for one (e.g. <code>/help ack</code>)
/version              build, runtime, schema level
/unbind_self          drop your own binding (no admin needed)</pre>

<b>✦ Your data — rules, devices, defaults</b>
<pre>/my_status            your rules, devices, last ACL
/my_rules             see your existing exit-rules
...
</pre>
```

(Raw HTML in this doc; Telegram renders `<b>` as bold,
`<i>` as italic, `<pre>` as monospace, `<code>` as
inline monospace.)

## Tests

- 1 test rewrite: `TestHelpReplyV0155Layout` (was
  checking 18-char proportional gutter; now checks
  20-char `<pre>` gutter + `<code>` count +
  `<pre>` block count + `ParseMode=HTML` on pending)
- 1 test extension: `TestHTMLRepliesMarkParseMode`
  adds the `/help` sub-case
- 12/12 packages green, smoke 118/118 expected on VM

## Operator impact

After `docker compose up -d --force-recreate --no-deps
skygate`:

- `/help` now renders with proper **bold** section
  headers, monospace tabular rows, and inline monospace
  placeholders
- The command column is now reliably aligned (was
  approximate in proportional font)
- No markdown leftovers (the `\`<id>\`` artifacts
  are gone — replaced by `<code>&lt;id&gt;</code>`
  which renders as a small monospace block)
- `/help <command>` (the long-form per-command help)
  is unchanged — it doesn't use the tabular format
- No config changes, no schema migration, no ACL
  changes
- No break of existing /help tests; the language
  isolation tests (TestHandleCommandHelpRUNoEnglishLeak)
  still pass — the catalog changes were HTML tags,
  not translations

## Backlog status

The "more HTML" pass is now complete for ALL bot
replies (read commands + the long /help). The next
formatting opportunities are:
- Per-reply inline color marks for status (v4 of
  butler voice) — deferred until operator feedback
  on v3 lands
- Error replies (db_error, scan_error) — could be
  wrapped in a Section() but they're rare and the
  short format reads fine

None of these are urgent.
