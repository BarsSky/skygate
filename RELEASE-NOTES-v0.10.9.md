# Skygate v0.10.9

> **Full bot i18n, hostname in node list, /add_device platform picker, MIT license.**

---

## 🇬🇧 English

### What is Skygate?

A small Go web portal for [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale). Read the
v0.10.7 notes for the full intro.

### What's new in v0.10.9?

* **MIT license** — the project is now properly open-source.
  The `LICENSE` file is the standard MIT text. The previous
  state ("proprietary" badge, no LICENSE file) meant the code
  was effectively all-rights-reserved; the new LICENSE makes
  the rights explicit.

* **Full Telegram bot i18n** — every bot reply goes through
  the i18n catalog now. The v0.10.4–v0.10.8 versions added
  bilingual support for `/help` and admin-only paths, but
  the user-scope command outputs (`/my_status`, `/my_nodes`,
  `/my_rules`, `/my_quota`, `/myexitnodes`, `/add_device`,
  `/add_rule`, `/delrule`, `/setdefaultdevice`, `/setexitnode`,
  `/bind`, `/unbind`, …) were still hardcoded English. With
  v0.10.9, switching to `/lang ru` actually changes the
  output. The admin helpers (`/status`, `/nodes`, `/rules`,
  `/audit`, `/exit_nodes`, `/quota`, `/ack`, `/restart`) are
  bilingual too.

* **Hostname in `/my_nodes` and `/nodes`** — both reply
  formats now render `hostname (node_id) [tag]` instead of
  the bare Tailscale node_id. Users recognise their devices
  by the friendly name they set in `tailscale up --hostname=`,
  not by an opaque id. The hostname is backfilled from
  headscale on the next backfill pass and stored in a new
  `node_owner_map.hostname` column (migration v0.34).

* **`/add_device` platform picker** — after the preauth key
  is issued, the bot shows an inline-keyboard prompt with
  five platforms: Linux / Windows / macOS / iOS / Android.
  Tapping a platform sends the per-platform install
  instructions, including the exact `tailscale up
  --login-server <HEADSCALE_URL> --authkey <key>` command
  line the user can paste into the device's terminal. The
  preauth key is resolved by `chat_id` when the callback
  arrives (a small new helper, `GetLastPreauthKeyForChatID`).

* **"Speak your name" → "Send your name"** in the welcome
  card. Telegram is text-only, so the "speak" verb was
  misleading.

* **Release-notes hygiene** — v0.10.7's release notes
  referenced real relay-node names (the operator's
  infrastructure). v0.10.9 replaces them with generic
  `relay-a / relay-b / relay-c` so the public release artifact
  doesn't leak deployment details. v0.10.8's release notes
  were also trimmed (the long bot-internals section was
  moved to AGENTS.md, where it belongs).

### Test status on VM

* `go test -count=1 -short ./...` — 12 / 12 packages PASS
* `bash scripts/smoke.sh` — 118 / 118 (59 ru + 59 en)
* `python3 scripts/check_exit_nodes.py` — 3 / 3 relay nodes OK
* New tests: 5 envelope tests in `commands_test.go` + 12 v2
  API tests in `personality_test.go` (carried over from
  v0.10.8). New `/add_device` platform picker is covered by
  `TestBuildPlatformPicker` and the bilingual catalog
  parity test (`TestCatalogsParity`).

### What we're working on next

* **`/clearrules` i18n** — the body helper still has
  hardcoded English. The catalog has all the keys; just
  needs the body touched. v0.10.10 follow-up.
* **Butler voice v3** — urgency marks in the header
  (`🪶` / `🪶!` / `🪶!!`) and inline status marks in the
  body. Defer until user feedback on v2 lands.
* **Personal API token rotation** — TTL + auto-rotate for
  the AI integration. v0.10.10 candidate.

---

## 🇷🇺 Русский

### Что такое Skygate?

Небольшой Go-портал для [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale). Полное
введение — в заметках к v0.10.7.

### Что нового в v0.10.9?

* **Лицензия MIT** — проект теперь корректно открытый.
  Файл `LICENSE` — стандартный MIT-текст. До этого был
  бейдж "proprietary" и не было файла лицензии — то есть
  код был фактически "all rights reserved"; теперь права
  прописаны явно.

* **Полная i18n для Telegram-бота** — каждый ответ бота
  теперь идёт через i18n-каталог. В v0.10.4–v0.10.8 была
  двуязычная поддержка для `/help` и admin-only путей,
  но user-scope вывод команд (`/my_status`, `/my_nodes`,
  `/my_rules`, `/my_quota`, `/myexitnodes`, `/add_device`,
  `/add_rule`, `/delrule`, `/setdefaultdevice`,
  `/setexitnode`, `/bind`, `/unbind`, …) был всё ещё
  захардкожен английским. С v0.10.9 переключение на
  `/lang ru` действительно меняет вывод. Админские
  помощники (`/status`, `/nodes`, `/rules`, `/audit`,
  `/exit_nodes`, `/quota`, `/ack`, `/restart`) тоже
  двуязычные.

* **Hostname в `/my_nodes` и `/nodes`** — оба формата
  ответа теперь рендерят `hostname (node_id) [tag]`
  вместо голого Tailscale node_id. Пользователи узнают
  свои устройства по friendly-имени, заданному в
  `tailscale up --hostname=`, а не по непрозрачному
  идентификатору. Hostname подтягивается из headscale
  при следующем backfill-проходе и сохраняется в новой
  колонке `node_owner_map.hostname` (миграция v0.34).

* **Platform picker в `/add_device`** — после выдачи
  preauth-ключа бот показывает inline-клавиатуру с пятью
  платформами: Linux / Windows / macOS / iOS / Android.
  При нажатии на платформу бот отправляет per-platform
  инструкции, включая точную команду `tailscale up
  --login-server <HEADSCALE_URL> --authkey <key>`,
  которую пользователь может скопировать в терминал
  устройства. Preauth-ключ резолвится по `chat_id` при
  приходе callback'а (новый helper
  `GetLastPreauthKeyForChatID`).

* **"Speak your name" → "Send your name"** в приветственной
  карточке. Telegram — текстовый, слово "speak" вводило
  в заблуждение.

* **Гигиена release notes** — в v0.10.7 release notes
  содержали реальные имена relay-нод (инфраструктура
  оператора). v0.10.9 заменяет их на обобщённые
  `relay-a / relay-b / relay-c`, чтобы публичный
  релизный артефакт не утекал детали деплоя. v0.10.8
  release notes также сокращены (длинная секция про
  бота перенесена в AGENTS.md, где ей и место).

### Тест-статус на VM

* `go test -count=1 -short ./...` — 12 / 12 пакетов PASS
* `bash scripts/smoke.sh` — 118 / 118 (59 ru + 59 en)
* `python3 scripts/check_exit_nodes.py` — 3 / 3 relay-ноды OK
* Новые тесты: 5 envelope-тестов в `commands_test.go` +
  12 тестов v2 API в `personality_test.go` (из v0.10.8).
  `/add_device` platform picker покрыт тестом
  `TestBuildPlatformPicker` + тестом паритета каталога
  `TestCatalogsParity`.

### Что делаем дальше

* **i18n для `/clearrules`** — body-helper всё ещё с
  захардкоженным английским. Ключи в каталоге есть,
  нужно только заменить. Кандидат на v0.10.10.
* **Butler voice v3** — отметки срочности в заголовке
  (`🪶` / `🪶!` / `🪶!!`) и статуса в теле. Отложим до
  обратной связи по v2.
* **Ротация персональных API-токенов** — TTL + auto-rotate.
  Кандидат на v0.10.10.

---

## v0.10.8 → v0.10.9

* **Commit range**: `0387d85..f234379` (1 commit, 20 files)
* **Lines changed**: 627 insertions, 514 deletions
* **Schema**: `v0.34` adds `node_owner_map.hostname` (TEXT,
  default ''). Idempotent — safe to re-run on a DB that
  already has the column.
* **Smoke**: 118 / 118 (unchanged)
* **Packages**: 12 / 12 (unchanged)
* **License**: now MIT (was: implicit all-rights-reserved)

---

## Migration from v0.10.8

No breaking changes. The schema migration `v0.34` runs
automatically on container start. After upgrading, run
`/admin/devices` "Backfill" to populate `hostname` for
existing nodes — without that, the new "hostname (node_id)"
format falls back to the bare node_id.
