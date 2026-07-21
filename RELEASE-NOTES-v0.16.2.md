# v0.16.2 — "more HTML" pass bug fix

2026-07-16

Hotfix for v0.16.1. The "more HTML" pass shipped
Field()/Section()/PreLinesRaw() formatting in eight
bot replies but **forgot to set `parse_mode=HTML` on
the sendMessage payload**, so the `<b>`, `<i>`, `<pre>`,
`<code>` tags showed up as raw source text in the chat.

## Symptoms

Before this fix, `/version` rendered like this:

```
════ Skygate ════
📦 Версия

Версия Skygate

<b>Билд:</b> <code>v0.16.0-1-g006f3d5</code>
<b>Go:</b> <code>go1.23.12</code>
<b>Схема БД:</b> <code>v0.32</code>

═══ — Ваш Дворецкий ═══
```

(The `<b>...</b>` and `<code>...</code>` should have
been rendered as bold labels and monospace values,
respectively.)

After this fix:

```
════ Skygate ════
📦 Версия

Версия Skygate

Билд:      v0.16.0-1-g006f3d5
Go:        go1.23.12
Схема БД:  v0.32

═══ — Ваш Дворецкий ═══
```

(With `Билд:`/`Go:`/`Схема БД:` in **bold**, the values
in monospace, all on aligned columns.)

## What changed

### 1. New `markHTMLReply()` helper

`internal/telegram/commands.go` (1 helper, ~15 lines):

```go
func markHTMLReply() {
    if pendingReplyForCurrentMessage == nil {
        pendingReplyForCurrentMessage = &PendingReply{ParseMode: "HTML"}
    } else {
        pendingReplyForCurrentMessage.ParseMode = "HTML"
    }
}
```

Sets the next reply's `parse_mode` to `"HTML"` so
Telegram renders the tags. Preserves any existing
inline-keyboard (so `/myexitnodes` keeps its tap-to-set
buttons).

### 2. 8 reply functions now call `markHTMLReply()`

Each function calls `markHTMLReply()` at the top:

| Reply | File |
|-------|------|
| `myStatusReply` | `commands_user.go` |
| `myNodesReply` | `commands_user.go` |
| `myRulesReply` | `commands_user.go` |
| `myQuotaReply` | `commands_user.go` |
| `myExitNodesReply` | `commands_user.go` |
| `versionReply` | `commands_phase4.go` |
| `auditReply` | `commands_phase2.go` |
| `exitNodesHealthReply` | `commands_exit_node_health.go` |

`addDeviceReply` was already covered by
`buildPlatformPicker` (the picker sets `ParseMode=HTML`
on the PendingReply it returns).

### 3. Bug fix: `myExitNodesReply` was wiping ParseMode

The function does TWO things to the pending slot:
1. `markHTMLReply()` at the top sets ParseMode=HTML
2. Later, the inline-keyboard rows are attached via
   `pendingReplyForCurrentMessage = &PendingReply{InlineKeyboard: btnRows}`

The second line created a fresh struct without copying
ParseMode, so the parse mode set by (1) was lost. Fixed
by setting ParseMode explicitly on the new struct:

```go
pendingReplyForCurrentMessage = &PendingReply{
    InlineKeyboard: btnRows,
    ParseMode:      "HTML",
}
```

### 4. Two new tests

- `TestHTMLRepliesMarkParseMode` — 8 sub-cases, one per
  HTML reply. Each checks that after the reply function
  runs, `pendingReplyForCurrentMessage.ParseMode == "HTML"`.
- `TestMarkHTMLReplyPreservesKeyboard` — pin for
  `myExitNodesReply`'s dual-set behavior (ParseMode +
  InlineKeyboard both present).

## Why opt-in instead of a global default

Many bot replies use literal `<` characters as
placeholders in their catalog text (`<id>`, `<chat_id>`,
`<username>`, `<target>`, etc. — see `bot.help_detail.*`
and `bot.help.*`). Telegram **rejects** messages with
unbalanced `<` or `&` in `parse_mode=HTML`. So a global
default would break those replies.

The opt-in keeps HTML off for literal-text replies and
on for the structured ones. The set of "structured"
replies is small and stable (the 8 listed above + 
`addDeviceReply`); new HTML replies just need to call
`markHTMLReply()` at the top.

## Catalog changes

None. This is a pure code/parse-mode fix; the v0.16.1
catalog is unchanged.

## Tests

- 12/12 packages green
- 1 new test file with 2 functions (9 sub-cases total)
- All existing v0.16.1 tests still pass

## Operator impact

After `docker compose up -d --force-recreate --no-deps
skygate`:

- `/my_status`, `/my_rules`, `/my_quota`, `/myexitnodes`,
  `/my_nodes`, `/version`, `/audit`, `/exit_nodes_health`
  will now render with proper **bold** labels and
  monospace values
- `/myexitnodes` keeps the tap-to-set inline-keyboard
- All other replies (including `/help` with its
  literal `<placeholder>` text) continue to render as
  plain text — no breakage
- No config changes, no schema migration, no ACL changes
