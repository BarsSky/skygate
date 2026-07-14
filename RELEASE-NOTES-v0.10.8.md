# Skygate v0.10.8

> **Butler voice v2 for the Telegram bot.** Every bot reply now
> reads as a single folded note handed to you at the door: brief,
> well-formed, never sloppy.

---

## 🇬🇧 English

### What is Skygate?

A small Go web portal for [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale) that gives every user a
friendly UI for the things they'd otherwise have to ask the admin to run
on the headscale CLI: preauth keys, device list, per-device exit-node
rules, and a Telegram butler for the same actions from a phone.

### What's new in v0.10.8?

This release is the **butler voice v2** upgrade for the Telegram bot
(Этап 14 v9). v0.10.4 introduced the butler-gatekeeper register and
v0.10.5 wired it to the i18n catalog; v0.10.8 unifies the envelope so
**every** reply from the bot reads as a single folded note:

```
🪶  The Registry
                    ← blank line
rules: 12 / ∞
users: 2
last acl: #5
                    ← blank line (only when verbose)
— Yours in service, the Warden of the Threshold
```

#### The envelope

* **Header** — one line: `🪶  <context>`. The context names the
  topic of the reply so the user sees it before scrolling:
  `The Registry` (status / rules / quota / nodes / audit / …),
  `The Codex` (help), `The Version Scroll` (/version),
  `Acknowledged` (/ack), `Binding` (/bind), `Unbinding`
  (/unbind / /unbind_self), `Added` (/add_*), `Removed`
  (/delrule / /clearrules), `A Closed Door` (errors /
  admin-only rejections / unknown commands), `The Gate`
  (welcome card).
* **Body** — unchanged from v1. The actual answer.
* **Footer** — `— <sign-off>`. Added only when the body is long
  (more than 3 lines, or more than 300 runes). Short replies
  stay clean — the absence of a footer is itself a signal
  ("this is the whole answer, nothing more to follow up on").

#### The header map

Adding a new command is one line in `commandContext` plus a
matching i18n key (`bot.header.<context>`); no code path changes
needed in the command body.

| Command | Header |
|---|---|
| `/status`, `/nodes`, `/rules`, `/audit`, `/exit_nodes`, `/quota`, `/my_*` (most), `/defaultdevice`, `/defaultexitnode`, `/lang` | The Registry |
| `/help` | The Codex |
| `/version` | The Version Scroll |
| `/ack` | Acknowledged |
| `/bind`, `/login <key>`, `/start <key>`, `/_bind_cancel` | Binding |
| `/unbind`, `/unbind_self` | Unbinding |
| `/add_device`, `/add_rule`, `/setdefaultdevice`, `/setexitnode` | Added |
| `/delrule`, `/clearrules`, `/delete_rule` | Removed |
| `/restart`, unknown, admin-only rejections, empty input | A Closed Door |
| `/start` (no args), `/login` (no args) | The Gate (already-composed welcome card) |

#### v1 surface, kept stable

The v1 helper API (`greetingForNewChat`,
`greetingForReturningUser`, `welcome`, `gatekeeperSign`,
`gateHeader`, `sectionLabel`, `codexLine`, `iconForCaller`,
`ruleBreak`) is **unchanged**. The welcome card still composes
itself internally for backward compat with the v1 personality
tests. New code should use `headerFor(lang, context)` /
`ComposeDefault(lang, context, body)`.

#### Refactor

* `internal/telegram/personality.go`: new `Compose` /
  `ComposeDefault` / `headerFor` / `footerFor` /
  `verboseForBody` helpers. The `butlerSigil` is now 🪶
  (a quill — clearer brand distinction from v1's ✦).
* `internal/i18n/catalog.go`: 11 new `bot.header.<ctx>` keys
  + `bot.footer.signoff` in both `ru` and `en`. Parity test
  (`TestCatalogsParity`) passes.
* `internal/telegram/commands.go`: `HandleCommand` is now a
  4-line orchestrator that delegates to `dispatchCommand`.
  `dispatchCommand` runs the strict-mode and admin-only gates,
  picks the right helper, and returns the body + the envelope
  context. The wrapper applies `ComposeDefault` unless the
  body is already composed (the v1 `/start` and `/login`
  no-args paths through `loginHint`).
* `internal/telegram/commands_login.go`: `loginReply` /
  `startReply` split into `loginHint` (no-args, already
  composed) + `loginReplyBody` / `startReplyBody` (with-args,
  raw body). dispatchCommand picks the right one per branch.

#### Tests

* 5 new envelope tests in `commands_test.go` pin the header
  per context (`TestHandleCommandStatusEnvelope`,
  `TestHandleCommandVersionEnvelope`,
  `TestHandleCommandHelpEnvelope`,
  `TestHandleCommandUnknownEnvelope`,
  `TestHandleCommandAdminOnlyEnvelope`).
* `TestHandleCommandHelpDetailed` updated to assert the new
  envelope shape (header + body) instead of the old body
  prefix.
* 12 new tests in `personality_test.go` cover the v2 API
  (`TestButlerSigilStability`,
  `TestHeaderForEachContext` × 11 contexts × 2 languages,
  `TestHeaderForFallback`, `TestFooterForBothLanguages`,
  `TestComposeEnvelope`, `TestVerboseForBody`,
  `TestComposeDefault`). v1 tests kept stable.

### Test status on VM

* `go test -count=1 -short ./...` — **12 / 12 packages PASS**
  (acl, auth, backup, config, db, handlers, headscale, i18n,
  release, telegram)
* `bash scripts/smoke.sh` — **118 / 118** (59 ru + 59 en)
* `python3 scripts/check_exit_nodes.py` — 3 / 3 relay nodes
  advertise `0.0.0.0/0` + `::/0`

### What we're working on next

* **Personal API token rotation** (admin override): no expiry
  per token today, only revocation. Adding a TTL + auto-rotate
  field so the bot integration can issue 24h / 7d / 30d
  tokens. v0.10.9 candidate.
* **Butler voice v3**: even more refined. Header carries
  urgency level (`🪶` for info, `🪶!` for warnings,
  `🪶!!` for errors). Body uses subtle inline color marks
  for status. Defer until user feedback on v2 lands.

---

## 🇷🇺 Русский

### Что такое Skygate?

Небольшой Go-портал для [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale), дающий каждому
пользователю удобный UI для вещей, которые иначе пришлось бы
делать через CLI администратора: preauth-ключи, список устройств,
per-device exit-node правила, и Telegram-дворецкий для тех же
действий с телефона.

### Что нового в v0.10.8?

Этот релиз — **дворецкий v2** для Telegram-бота (Этап 14 v9).
В v0.10.4 появился «butler-gatekeeper» регистр, в v0.10.5 он
был привязан к i18n-каталогу, а v0.10.8 **унифицирует конверт**
так, что **каждый** ответ бота читается как одна сложенная
записка, поданная вам у двери:

```
🪶  Реестр
                  ← пустая строка
правил: 12 / ∞
пользователей: 2
последний ACL: #5
                  ← пустая строка (только для длинных ответов)
— Искренне Ваш, Хранитель Порога
```

#### Конверт

* **Заголовок** — одна строка: `🪶  <контекст>`. Контекст
  именует тему ответа, чтобы пользователь видел её до
  прокрутки: `Реестр` (status / rules / quota / nodes /
  audit / …), `Кодекс` (help), `Свиток версии` (/version),
  `Подтверждение` (/ack), `Привязка` (/bind), `Отвязка`
  (/unbind / /unbind_self), `Добавление` (/add_*),
  `Удаление` (/delrule / /clearrules), `Закрытая дверь`
  (ошибки / отказ в admin-only / неизвестная команда),
  `Врата` (приветственная карточка).
* **Тело** — без изменений из v1. Сам ответ.
* **Подпись** — `— <подпись>`. Добавляется только для
  длинных ответов (больше 3 строк, или больше 300 рун).
  Короткие ответы остаются чистыми — отсутствие подписи
  само по себе сигнал («это весь ответ, добавлять нечего»).

#### Карта заголовков

Добавить новую команду = одна строка в `commandContext`
плюс соответствующий ключ i18n (`bot.header.<context>`);
в коде самих команд менять ничего не нужно.

| Команда | Заголовок |
|---|---|
| `/status`, `/nodes`, `/rules`, `/audit`, `/exit_nodes`, `/quota`, большинство `/my_*`, `/defaultdevice`, `/defaultexitnode`, `/lang` | Реестр |
| `/help` | Кодекс |
| `/version` | Свиток версии |
| `/ack` | Подтверждение |
| `/bind`, `/login <key>`, `/start <key>`, `/_bind_cancel` | Привязка |
| `/unbind`, `/unbind_self` | Отвязка |
| `/add_device`, `/add_rule`, `/setdefaultdevice`, `/setexitnode` | Добавление |
| `/delrule`, `/clearrules`, `/delete_rule` | Удаление |
| `/restart`, неизвестная, отказ admin-only, пустой ввод | Закрытая дверь |
| `/start` (без аргументов), `/login` (без аргументов) | Врата (уже обёрнутая приветственная карточка) |

#### Стабильность v1

v1 API помощников (`greetingForNewChat`,
`greetingForReturningUser`, `welcome`, `gatekeeperSign`,
`gateHeader`, `sectionLabel`, `codexLine`, `iconForCaller`,
`ruleBreak`) **не изменился**. Приветственная карточка по-
прежнему оборачивает себя сама — для обратной совместимости
с тестами v1. Новый код должен использовать
`headerFor(lang, context)` / `ComposeDefault(lang, context, body)`.

#### Рефакторинг

* `internal/telegram/personality.go`: новые помощники
  `Compose` / `ComposeDefault` / `headerFor` / `footerFor` /
  `verboseForBody`. `butlerSigil` теперь 🪶 (перо — более
  явное бренд-отличие от v1's ✦).
* `internal/i18n/catalog.go`: 11 новых ключей
  `bot.header.<ctx>` + `bot.footer.signoff` в `ru` и `en`.
  Тест паритета (`TestCatalogsParity`) проходит.
* `internal/telegram/commands.go`: `HandleCommand` теперь —
  4-строчный оркестратор, делегирующий в `dispatchCommand`.
  `dispatchCommand` прогоняет strict-mode и admin-only
  шлюзы, выбирает нужный помощник и возвращает тело +
  контекст конверта. Обёртка применяет `ComposeDefault`,
  кроме случаев когда тело уже обёрнуто (v1 пути `/start`
  и `/login` без аргументов через `loginHint`).
* `internal/telegram/commands_login.go`: `loginReply` /
  `startReply` разделены на `loginHint` (без аргументов,
  уже обёрнуто) + `loginReplyBody` / `startReplyBody`
  (с аргументами, сырое тело). `dispatchCommand` выбирает
  нужную ветку.

#### Тесты

* 5 новых тестов конверта в `commands_test.go` закрепляют
  заголовок для каждого контекста
  (`TestHandleCommandStatusEnvelope`,
  `TestHandleCommandVersionEnvelope`,
  `TestHandleCommandHelpEnvelope`,
  `TestHandleCommandUnknownEnvelope`,
  `TestHandleCommandAdminOnlyEnvelope`).
* `TestHandleCommandHelpDetailed` обновлён — проверяет
  новую форму конверта (заголовок + тело) вместо старого
  префикса тела.
* 12 новых тестов в `personality_test.go` покрывают v2 API
  (`TestButlerSigilStability`,
  `TestHeaderForEachContext` × 11 контекстов × 2 языка,
  `TestHeaderForFallback`, `TestFooterForBothLanguages`,
  `TestComposeEnvelope`, `TestVerboseForBody`,
  `TestComposeDefault`). v1 тесты сохранены.

### Тест-статус на VM

* `go test -count=1 -short ./...` — **12 / 12 пакетов PASS**
  (acl, auth, backup, config, db, handlers, headscale, i18n,
  release, telegram)
* `bash scripts/smoke.sh` — **118 / 118** (59 ru + 59 en)
* `python3 scripts/check_exit_nodes.py` — 3 / 3 релейных
  ноды анонсируют `0.0.0.0/0` + `::/0`

### Что делаем дальше

* **Ротация персональных API-токенов** (admin override):
  сейчас у токена нет срока действия, только отзыв.
  Добавим TTL + auto-rotate поле, чтобы интеграция с ботом
  могла выпускать токены на 24ч / 7д / 30д. Кандидат на
  v0.10.9.
* **Butler voice v3**: ещё тоньше. В заголовке —
  уровень срочности (`🪶` для информации, `🪶!` для
  предупреждений, `🪶!!` для ошибок). В теле — тонкие
  inline-пометки статуса. Отложим до сбора обратной
  связи по v2.

---

## Migration from v0.10.7

No schema changes. No breaking changes. The `Compose` /
`ComposeDefault` envelope is **wire-compatible** with v1: the
existing v1 helper functions are still exported, still return
the same shape, and are still called by the v1 personality
tests. The v2 envelope adds a header line and (for verbose
replies) a footer line to every bot reply — both lines are
plain text, no special Telegram markup, so any client that
reads the bot will see them.

If you have custom integrations that grep bot replies for
specific substrings (e.g. "rules:", "Logged in as"),
**they still work** — the v2 envelope only ADDS lines, never
modifies the body. New commands can drop a header on top;
nothing about the body's content has changed.

For the operator, the only visible change in the bot is the
consistent envelope. v0.10.8 ships with the same commands
(33 of them), the same admin/user/auth meta-categories, and
the same bilingual reply. If your team already loved v0.10.7's
feel, v0.10.8 is the same voice — just spoken more clearly.

---

## v0.10.7 → v0.10.8

* **Commit range**: `f4c2cd3..422507e` (15 commits: 6 from
  v0.10.7 + 9 from Этап 14 v9)
* **Lines changed**: 752 insertions, 268 deletions across 6
  files (`internal/i18n/catalog.go`,
  `internal/telegram/commands.go`,
  `internal/telegram/commands_login.go`,
  `internal/telegram/commands_test.go`,
  `internal/telegram/personality.go`,
  `internal/telegram/personality_test.go`)
* **Schema**: unchanged. No migration required.
* **Smoke**: 118 / 118 (unchanged)
* **Packages**: 12 / 12 (unchanged)
