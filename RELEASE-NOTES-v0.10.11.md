# Skygate v0.10.11

> **RU/EN localisation polish + 📋 Copy button for the
> preauth key.** Three small but visible fixes that came up
> in real Telegram chats with the v0.10.9 / v0.10.10 bot.

---

## 🇬🇧 English

### What is Skygate?

A small Go web portal for [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale) that gives
every user a friendly UI for the things they'd otherwise have
to ask the admin to run on the headscale CLI. Read the v0.10.7
notes for the full intro.

### What's in v0.10.11?

Three localisation + UX fixes, each small, each visible:

* **📋 Copy button on the preauth key.** The `/add_device`
  reply used to put the key in a ` ``` ` code block — the user
  had to long-press the message, select "Copy", and then
  paste. On mobile that was awkward. v0.10.11 adds an
  inline-keyboard `📋 Copy` button (Telegram `copy_text`
  field, Bot API 7.0+) at the top of the platform picker.
  Tapping the button puts the preauth key on the clipboard
  with one tap; the user pastes it into the device's
  terminal as-is. The platform buttons (Linux / Windows /
  macOS / iOS / Android) stay below the Copy button, so the
  picker is now 3 rows instead of 2.

* **RU-locale `/help` no longer leaks English suffixes.**
  v0.10.9's i18n-isation had the per-line strings in the
  catalog, but a few code paths appended English suffixes
  outside the catalog — so the RU-locale /help output
  showed mixed RU/EN text like
  `/add_rule <target> — add an exit-rule for yourself` and
  `/clearrules — стереть ВСЕ свои exit-правила (or another
  user, admin only); requires /clearrules confirm within 30s`.
  v0.10.11 rewrites the offending `bot.help.*` catalog keys
  to include the full line (no appended EN suffix) and
  replaces the EN "last-seen" in `/exit_nodes` with the
  Russian "когда они были онлайн". A new test
  `TestHandleCommandHelpRUNoEnglishLeak` pins the language
  — any future regression that lets an English substring
  into the RU /help fails the test.

* **/lang help is clearer.** The catalog key
  `bot.help.lang` now reads
  "`/lang` — show this chat's current language; `/lang ru|en`
  — switch to Russian or English" (RU: "`/lang` — показать
  текущий язык чата; `/lang ru|en` — сменить на русский
  или английский"). The old text said `/lang [ru|en]` which
  was ambiguous about what happens when you call `/lang`
  with no args (it shows the current language, but the help
  line didn't say that).

* **Typo in /add_device reply.** "чтобы зарегистрироваться"
  → "чтобы зарегистрировать устройство" (or actually
  "чтобы зарегистрировать его в tailnet"). The old wording
  was grammatical — "зарегистрироваться" is reflexive —
  but the device doesn't "register itself", the user
  registers it. New phrasing matches the action.

### Test status on VM

* `go test -count=1 -short ./...` — 12 / 12 packages PASS
* `bash scripts/smoke.sh` — 118 / 118 (59 ru + 59 en)
* New tests:
  - `TestBuildPlatformPicker_CopyButton` — pins the
    inline-keyboard shape: 3 rows, first row is a single
    Copy button with `copy_text` carrying the preauth key
    (no `callback_data` — Telegram would call the bot on
    tap, but the action is purely client-side).
  - `TestHandleCommandHelpRUNoEnglishLeak` — asserts the
    RU /help output is free of 7 known EN substrings that
    leaked in v0.10.9.

### What we're working on next

* **`/clearrules` i18n** — body helper still has hardcoded
  English. Catalog has all the keys; just needs the body
  touched. v0.10.12 follow-up.
* **Butler voice v3** — urgency marks in the header
  (`🪶` / `🪶!` / `🪶!!`) and inline status marks in the
  body. Defer until user feedback on v2 lands.
* **Personal API token rotation** — TTL + auto-rotate for
  the AI integration. v0.10.12 candidate.

---

## 🇷🇺 Русский

### Что такое Skygate?

Небольшой Go-портал для [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale), дающий
каждому пользователю удобный UI для вещей, которые иначе
пришлось бы делать через CLI администратора.

### Что в v0.10.11?

Три небольших, но видимых фикса локализации + UX, выявленных
в реальных Telegram-чатах с ботом v0.10.9 / v0.10.10:

* **📋 Кнопка «Скопировать» для preauth-ключа.** В ответе
  `/add_device` ключ раньше лежал в блоке ` ``` ` — нужно было
  долгое нажатие, выделение, копирование. На мобильном это
  неудобно. v0.10.11 добавляет inline-кнопку `📋 Скопировать`
  (поле `copy_text` в Telegram Bot API 7.0+) наверху
  платформенного picker'а. Тап — и ключ уже в буфере
  обмена; пользователь вставляет его в терминал устройства
  as-is. Кнопки платформ (Linux / Windows / macOS / iOS /
  Android) остаются ниже, picker стал 3 ряда вместо 2.

* **RU-локализация `/help` больше не подмешивает английский.**
  В v0.10.9 i18n-изация покрыла построчные строки в каталоге,
  но несколько мест склеивали английские суффиксы
  мимо каталога — и RU-локаль показывала смесь RU/EN типа
  `/add_rule <target> — add an exit-rule for yourself` и
  `/clearrules — стереть ВСЕ свои exit-правила (or another
  user, admin only); requires /clearrules confirm within 30s`.
  v0.10.11 переписывает соответствующие `bot.help.*` ключи
  так, чтобы они содержали всю строку целиком (без EN-суффикса),
  и заменяет английское "last-seen" в `/exit_nodes` на
  русское "когда они были онлайн". Новый тест
  `TestHandleCommandHelpRUNoEnglishLeak` пинит язык — любая
  будущая регрессия, пропускающая EN-подстроку в RU /help,
  валит тест.

* **/lang help стал понятнее.** Ключ `bot.help.lang` теперь
  читается "`/lang` — показать текущий язык чата; `/lang
  ru|en` — сменить на русский или английский". Старый
  текст `/lang [ru|en]` был неоднозначен — что произойдёт
  при вызове `/lang` без аргументов (он показывает текущий
  язык, но help этого не говорил).

* **Опечатка в /add_device.** "чтобы зарегистрироваться" →
  "чтобы зарегистрировать его в tailnet". Старое слово
  грамматически было возвратным ("зарегистрироваться" —
  "register itself"), но устройство само себя не
  регистрирует — его регистрирует пользователь. Новая
  формулировка соответствует действию.

### Тест-статус на VM

* `go test -count=1 -short ./...` — 12 / 12 пакетов PASS
* `bash scripts/smoke.sh` — 118 / 118 (59 ru + 59 en)
* Новые тесты:
  - `TestBuildPlatformPicker_CopyButton` — пинит форму
    inline-клавиатуры: 3 ряда, первый ряд — единственная
    кнопка Copy с `copy_text`, несущим preauth-ключ (без
    `callback_data` — Telegram бы позвал бота по тапу, но
    действие чисто клиентское).
  - `TestHandleCommandHelpRUNoEnglishLeak` — утверждает,
    что RU /help свободен от 7 известных EN-подстрок,
    утекших в v0.10.9.

### Что делаем дальше

* **i18n для `/clearrules`** — body-helper всё ещё с
  захардкоженным английским. Ключи в каталоге есть,
  нужно только заменить. Кандидат на v0.10.12.
* **Butler voice v3** — отметки срочности в заголовке
  (`🪶` / `🪶!` / `🪶!!`) и статуса в теле. Отложим до
  обратной связи по v2.
* **Ротация персональных API-токенов** — TTL + auto-rotate.
  Кандидат на v0.10.11.

---

## v0.10.10 → v0.10.11

* **Commit range**: `d667442..<HEAD>` (1+ commits, see git log)
* **Files changed**: `internal/i18n/catalog.go`,
  `internal/telegram/commands.go`, `internal/telegram/commands_test.go`,
  `internal/telegram/commands_user.go`,
  `internal/telegram/platform_picker.go`,
  `RELEASE-NOTES-v0.10.11.md` (this file)
* **Schema**: unchanged. No migration required.
* **Smoke**: 118 / 118 (unchanged)
* **Packages**: 12 / 12 (unchanged)

---

## Migration from v0.10.10

No action required. The Copy button uses Telegram's Bot API
7.0+ `copy_text` field, which is supported by every Telegram
client in active use as of 2024. Users on older clients
(2018–2020) will see the picker without the Copy button —
they can still long-press the preauth key in the body to
copy it. The fallback is graceful.
