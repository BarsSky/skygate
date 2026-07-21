# v0.15.5 — admin body butler-voice polish + /help de-duplication

**Date**: 2026-07-16
**Branch**: `feature/v0.10.12-bot-ux`
**Build**: post-`7f4fabe` (v0.15.4 per-command icons)

The "make admin replies sound like a butler, not like a log" release +
the "/help column alignment fix" follow-up. After v0.15.4 wired
per-command icons into the gate envelope header, the bodies underneath
still carried log-voice prefixes (`sync_nodes:`, `audit:`,
`exit_nodes_health:`, `restart:`, `add_rule:`, `delrule:`, `clearrules:`)
that v0.10.8's butler voice was supposed to retire. And the /help
listing had a broken command column (12-char gutter that didn't fit
`/exit_nodes_health` 17 chars), duplicated the command name in every
description (`` `/login <key>` — bind this chat to your skygate account ``
when `/login` was already in the gutter), and was missing `/unbind_self`
from the auth section.

This release finishes both cleanups. Every admin / power-user reply
now reads as a real sentence — capital first letter, no command-name
colon prefix — while keeping every ✓ / ⚠ status marker, every
technical field (`target:`, `rule_ids=`, `ACL v#`), and every
test-pinned substring the smoke and unit tests depend on. /help is
aligned, de-duplicated, and complete.

## What's in

### Body butler-voice rewrite (RU + EN, ~50 catalog keys)

* `sync_nodes.ok` — `"Готово. Подтянул из headscale: N новых, M обновлено
  (из X всего)."` / `"Done. Synced from headscale: ..."`. The ✓-prefix
  log marker is gone; the new line reads as a completed action.
* `audit.*` (header, row, empty, db_error, scan_error) — RU +
  EN. The 4-arg row template now drops the trailing `\n  %s` from
  the format string and appends the detail on its own indented line
  in `commands_phase2.go`, so long details don't fight the row gutter.
* `exit_nodes_health.*` (header, all_offline_warning, three bucket
  labels, row, empty, db_error) — RU + EN. Per-bucket labels in
  Russian: "Офлайн", "Деградировавшие", "Работают". English keeps
  "Offline", "Degraded", "Online" with the all-offline warning now
  ending in a full stop like every other body sentence.
* `restart.*` (confirm_prompt, invalid_token, corrupt, expired,
  mint_failed, confirmed) — RU + EN. The `restart:` log prefix is
  gone; the new prompts read as a proper confirm / error.
* `add_rule.*` (~20 keys) + `delrule.*` (~10 keys) + `clearrules.*`
  (~20 keys) — RU + EN. Every error / info / success message
  dropped its `add_rule:` / `delrule:` / `clearrules:` log prefix
  and got a capital first letter. The `✓` / `⚠` markers stay
  where they were (those are status, not log-voice), and the
  technical fields (`target:`, `rule_ids=`, `ACL v#`) stay
  verbatim because the smoke and unit tests pin them.

### Test fixes (~15 case-sensitive substring updates)

The substring assertions in `commands_test.go` were written against
the old log-voice prefix format ("add_rule: extra args", "delrule:
deleted 1 rule", "clearrules: cleared 2 rule", etc.). After the
catalog rewrite, those substrings now start with capital letters
("Extra args", "Deleted 1 rule", "Cleared 2 rule"), and the tests
are case-sensitive. Updated every assertion to match the new
butler-voice capitalization. No behaviour change.

* `TestDelRuleReplyRejectsNonAdminForOtherUser` — "extra args" → "Extra args"
* `TestDelRuleIsAliasOfDeleteRule` (×2) — "deleted 1 rule" → "Deleted 1 rule"
* `TestDelRuleReplyUsageHint` — "usage" → "Usage"
* `TestDelRuleReplyRejectsBadArg` / `TestDelRuleReplyRejectsUnknownID` —
  "no valid ids" → "No valid ids"
* `TestDelRuleReplySingleSuccess` / `TestDelRuleReplyMultiSuccess` /
  `TestDelRuleReplyDomainCascade` / `TestDelRuleReplyAdminForOtherUser` —
  "deleted N rule" → "Deleted N rule"
* `TestAddRuleReplyUsageHint` — "usage" → "Usage"
* `TestAddRuleReplyRejectsPerUserLimit` — "user limit reached" → "User limit reached"
* `TestAddRuleReplyRejectsPerDeviceLimit` — "per-device limit" → "Per-device limit"
* `TestAddRuleReplyRejectsTotalLimit` — "system-wide limit" → "System-wide limit"
* `TestAddRuleReplySuccessIP` — "added" → "Added" (substring still present
  in "Added 1 rule(s) for ...")
* `TestAddRuleReplyAdminForOtherUser` — same
* `TestAddRuleReplyRejectsNonAdminForOtherUser` — "extra args" → "Extra args"
* `TestClearRulesReplyRejectsNonAdminForOtherUser` — "extra args" → "Extra args"
* `TestClearRulesReplyConfirmWithoutPending` — "no pending clear request" →
  "No pending clear request"
* `TestClearRulesReplyFullMintAndConfirm` — "cleared 2 rule" → "Cleared 2 rule",
  "no pending" → "No pending"
* `TestClearRulesReplyAdminMintAndConfirm` — "cleared 1 rule" → "Cleared 1 rule"
* `TestClearRulesReplyDomainCascade` — "cleared 4 rule" → "Cleared 4 rule"
* `TestClearRulesReplyRussianNoPending` — "нет pending-запроса" → "Нет pending-запроса"
* `TestClearRulesReplyRussianMintPrompt` — "это удалит ВСЕ" → "Это удалит ВСЕ"
* `TestClearRulesReplyRussianAppliedOk` — "✓ очищено" → "✓ Очищено"
* `TestHandleCommandAudit` — "audit_log" → "audit log" (dropped the
  underscore from the header).
* `TestHandleCommandRestartIssuesToken` — "confirm by sending within 30s" →
  "Confirm by sending within 30s" (the new catalog capitalises the first
  word).

## What stayed

* **Gate envelope** — `🪶 ═══ Skygate ═══` / `═══ — Ваш Дворецкий ═══`
  from v0.15.2. Every reply still wears it.
* **Per-command icons** — `📊 Реестр` / `📊 The Registry` etc. from
  v0.15.4. The admin context headers (`❌ Removed`, `❌ Готово — удалено`,
  `✅ Added`, `✅ Готово — добавлено`) are unchanged.
* **Greeting + signoff** — time-of-day greeting (`Доброе утро`, ...) +
  `— Ваш Дворецкий` signoff. Optional via `WithNoGreeting()` /
  `WithNoSignoff()` for short replies.
* **Telegram Bot API 7.0+** — `copy_text` as a typed object
  (`{"text": "..."}`) for the platform picker, `editMessageText` for
  inline-keyboard navigation (button taps rewrite the same message
  instead of stacking new ones).
* **No SQL schema changes**, no Go API surface changes, no breaking
  config moves.
* **No behavior changes** — every command still does exactly what it
  did before. Only the printed message text changed.

### /help de-duplication + alignment (RU + EN)

After v0.14.0 split /help into three sections (🔐 Auth / ✦ Your data /
🛠 Admin), the table-row formatter had a 12-char gutter that didn't
fit `/exit_nodes_health` (17 chars) — the row got pushed 5 chars to
the right and the description column no longer lined up with the
rest of the section. The EN description column also duplicated the
command name in every row: `\`<cmd>\` — <explanation>`, even though
the gutter already carried the command. RU had the same problem in
em-dash form: "Привязать чат к аккаунту skygate — /login <ключ>".

* **`commands.go` — `helpReply()` row formatter** now pads every
  command to 18 chars (1 past the longest, `/exit_nodes_health`),
  so the description column lands on the same offset for every row
  in every section. The gutter now shows just the command name
  (`/login`, `/add_rule`, `/clearrules`, ...) — no `<key>` /
  `<target>` argument list in the gutter itself, because the
  description carries the args hint.
* **All 27 `bot.help.*` descriptions rewritten** (RU + EN, 54 catalog
  keys total). The new format is `<explanation>  [args: <hint>]`
  with back-ticked sub-commands inline where the explanation
  references one (e.g. the `/clearrules` description still shows
  the exact `/clearrules confirm` form the user must type).
* **`/unbind_self` added** to the Auth section. It was in the
  command dispatch table since v0.14.0 but never listed in /help.
  New `bot.help.auth_unbind_self` keys (RU + EN).
* **`/exit_nodes_health` and `/sync_nodes` descriptions** picked up
  the same back-tick convention as the rest of /help
  (`/exit_nodes`, `/node_owner_map`).

New test `TestHelpReplyV0155Layout` pins the contract:
  * /unbind_self is in the listing
  * `/exit_nodes_health` is padded to 18 chars
  * `/status` (a short command) is padded to the same width
  * No description column contains `` `/status` — ``, the old
    backticked-duplicate pattern

## Verification

* 12/12 packages green (`go test -count=1 ./...`)
* `TestCatalogsParity` + `TestPlaceholderOrder` green (every new key
  has both RU and EN, both have the same `%s` / `%d` arg counts)
* All `TestHandleCommand*` + `TestClearRules*` + `TestDelRule*` +
  `TestAddRule*` green after the substring updates
* `TestHelpReplyAdminShowsAllCategories` + `TestHelpReplyUserHidesAdmin`
  + new `TestHelpReplyV0155Layout` all green
* `TestHandleCommandRestartIssuesToken` and `TestHandleCommandRestartConfirmHappy`
  green (the `confirm by sending within 30s` / `SIGTERM in 200ms`
  substrings now use the capital-first-letter butler-voice variants)
* Pending: VM `make test` (smoke 118/118) before push to GitHub
