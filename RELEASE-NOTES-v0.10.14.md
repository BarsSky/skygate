# v0.10.14 — /clearrules body i18n (закрытие RU-долга)

**Release date**: 2026-07-15
**Previous release**: v0.10.13

## What's in this release

A small, focused release that closes the last hardcoded-English
path in the bot: `/clearrules`. v0.10.9 lifted the command to
i18n in name only — the catalog had 16 `bot.clearrules.*` keys,
but the helper still built every reply with `fmt.Sprintf`
against literal English templates. v0.10.14 rewrites the
helper top-to-bottom to use `i18n.T` / `i18n.Tf` for every
visible line, and adds 5 new catalog keys for the mint-phase
prompt that was previously untranslated.

### Fixed

- **`/clearrules` body was the last English-only bot reply.**
  The mint phase's three lines ("this will delete ALL N
  rule(s) for X:", "Send /clearrules confirm within 30s…", and
  the "ignored if the request is older…" note) and the
  scan-error fallback are now catalog-driven. The audit-log
  detail strings stay in English on purpose — those are
  operator-only and the `/admin/audit` view is an
  English-by-convention surface (matches the
  `telegram_save` / `ACL_*` / `rules_clear_requested` action
  names already there). Same for the
  `Notifier.SendAlert` body on SetPolicy failure: operator
  alert, not user reply.

### Migration guide

From v0.10.13 → v0.10.14:

1. **Pull and rebuild** (operator-driven, no auto-update):
   ```bash
   cd /home/skyadmin/skygate
   git pull
   docker compose restart skygate
   ```
2. **No DB migrations.** Catalog + Go code only; the helper
   rewires the same `clearRulesReply` signature, every
   existing call site is unchanged.
3. **No env / config changes.** No new env vars; no
   `global_settings` writes; no `.env.example` updates.

### Verification

- 12/12 packages green (`go test ./...`)
- All 15 original `TestClearRulesReply*` tests still PASS
  (the `Lang: i18n.LangEN` default in `userEnv` keeps the EN
  surface byte-identical to v0.10.13)
- 6 new RU-specific tests PASS:
  - `TestClearRulesReplyRussianUnbound` — verifies the
    `bot.clearrules.not_bound` template in RU
  - `TestClearRulesReplyRussianNoRules` — verifies
    `bot.clearrules.empty` in RU + no English leak
  - `TestClearRulesReplyRussianNoPending` — verifies
    `bot.clearrules.no_pending` in RU
  - `TestClearRulesReplyRussianMintPrompt` — verifies
    the new `bot.clearrules.mint_header`,
    `bot.clearrules.mint_send_confirm`, and
    `bot.clearrules.mint_ignored_note` in RU + no English
    leak on any of the three lines
  - `TestClearRulesReplyRussianAppliedOk` — verifies the
    success branch in RU + no English leak
  - `TestClearRulesReplyRussianReadOnlyMode` — verifies
    the read-only path in RU
- Smoke 118/118 PASS on VM (`make test`) — `[ru] 59 + [en] 59`
- Build: `go build ./...` clean

### Files changed

| File | What |
|---|---|
| `internal/telegram/commands_clear.go` | full rewrite to use `i18n.T` / `i18n.Tf` on every user-visible line; audit-log detail and notifier alert text stay in English by design |
| `internal/telegram/commands_test.go` | 6 new `TestClearRulesReplyRussian*` tests |
| `internal/i18n/catalog.go` | 5 new keys: `bot.clearrules.scan_error`, `bot.clearrules.mint_header`, `bot.clearrules.mint_more_suffix`, `bot.clearrules.mint_send_confirm`, `bot.clearrules.mint_ignored_note` (× 2 languages) |

## RU

# v0.10.14 — `/clearrules` body i18n (закрытие RU-долга)

**Дата релиза**: 2026-07-15
**Предыдущий релиз**: v0.10.13

## Что в этом релизе

Маленький, сфокусированный релиз, который закрывает последний
жёстко-прописанный английский путь в боте: `/clearrules`.
В v0.10.9 команда была i18n-изирована только по названию —
каталог имел 16 ключей `bot.clearrules.*`, но хелпер всё ещё
собирал каждый ответ через `fmt.Sprintf` с английскими
шаблонами. v0.10.14 переписывает хелпер целиком, переводя
каждую видимую строку на `i18n.T` / `i18n.Tf`, и добавляет
5 новых ключей для mint-фазы, которая раньше была совсем
непереведена.

### Исправлено

- **`/clearrules` body был последним английским путём в
  боте.** Три строки mint-фазы («this will delete ALL N
  rule(s) for X:», «Send /clearrules confirm within 30s…» и
  «ignored if the request is older…») и scan-error fallback
  теперь идут через каталог. Детали audit_log остаются на
  английском намеренно — это operator-only поверхность
  (соответствует `telegram_save` / `ACL_*` /
  `rules_clear_requested` action-именам, которые уже там).
  Так же как и тело `Notifier.SendAlert` при SetPolicy
  failure: операторский алерт, не ответ пользователю.

### Гайд по миграции

С v0.10.13 → v0.10.14:

1. **Подтянуть и пересобрать** (вручную, без
   авто-обновления):
   ```bash
   cd /home/skyadmin/skygate
   git pull
   docker compose restart skygate
   ```
2. **Миграций БД нет.** Только каталог + Go-код; хелпер
   переподключает ту же сигнатуру `clearRulesReply`, все
   существующие call-сайты без изменений.
3. **Менять `.env` / конфиг не нужно.** Новых env-ов нет;
   записей в `global_settings` нет; `.env.example` без
   изменений.

### Проверка

- 12/12 пакетов зелёные (`go test ./...`)
- Все 15 оригинальных `TestClearRulesReply*` тестов
  продолжают PASSить (умолчание `Lang: i18n.LangEN` в
  `userEnv` сохраняет EN-поверхность байт-в-байт как в
  v0.10.13)
- 6 новых RU-специфичных тестов PASSят:
  - `TestClearRulesReplyRussianUnbound` — проверяет
    шаблон `bot.clearrules.not_bound` в RU
  - `TestClearRulesReplyRussianNoRules` — проверяет
    `bot.clearrules.empty` в RU + отсутствие EN-утечки
  - `TestClearRulesReplyRussianNoPending` — проверяет
    `bot.clearrules.no_pending` в RU
  - `TestClearRulesReplyRussianMintPrompt` — проверяет
    новые `bot.clearrules.mint_header`,
    `bot.clearrules.mint_send_confirm` и
    `bot.clearrules.mint_ignored_note` в RU + отсутствие
    EN-утечки ни в одной из трёх строк
  - `TestClearRulesReplyRussianAppliedOk` — проверяет
    success-ветку в RU + отсутствие EN-утечки
  - `TestClearRulesReplyRussianReadOnlyMode` — проверяет
    read-only путь в RU
- Smoke 118/118 PASS на VM (`make test`) — `[ru] 59 + [en] 59`
- Сборка: `go build ./...` чисто

### Изменённые файлы

| Файл | Что |
|---|---|
| `internal/telegram/commands_clear.go` | полная перезапись с `i18n.T` / `i18n.Tf` на каждой видимой строке; детали audit_log и notifier alert остаются на английском намеренно |
| `internal/telegram/commands_test.go` | 6 новых `TestClearRulesReplyRussian*` тестов |
| `internal/i18n/catalog.go` | 5 новых ключей: `bot.clearrules.scan_error`, `bot.clearrules.mint_header`, `bot.clearrules.mint_more_suffix`, `bot.clearrules.mint_send_confirm`, `bot.clearrules.mint_ignored_note` (× 2 языка) |
