# Skygate v0.10.8

> **Butler voice v2 for the Telegram bot.** Every bot reply now
> opens with a one-line context header (`🪶  <context>`) and
> closes with a sign-off line when the answer is long.

---

## 🇬🇧 English

### What is Skygate?

A small Go web portal for [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale) that gives every
user a friendly UI for the things they'd otherwise have to ask the
admin to run on the headscale CLI: preauth keys, device list,
per-device exit-node rules, and a Telegram butler for the same
actions from a phone.

### What's new in v0.10.8?

The bot now replies in a single unified envelope: a one-line
context header, the body, and an optional sign-off for verbose
replies. Eleven contexts are wired (`registry`, `codex`, `version`,
`ack`, `bind`, `unbind`, `add`, `del`, `err`, `welcome`,
`welcome_back`). The v1 helper API is kept stable for backward
compatibility.

### Test status on VM

* `go test -count=1 -short ./...` — 12 / 12 packages PASS
* `bash scripts/smoke.sh` — 118 / 118 (59 ru + 59 en)
* `python3 scripts/check_exit_nodes.py` — 3 / 3 relay nodes OK

### What we're working on next

* **Butler voice v3** — urgency marks in the header (`🪶` /
  `🪶!` / `🪶!!`) and inline status marks in the body. Defer
  until user feedback on v2 lands.
* **Personal API token rotation** — TTL + auto-rotate for the
  AI integration. v0.10.9 candidate.

---

## 🇷🇺 Русский

### Что такое Skygate?

Небольшой Go-портал для [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale), дающий
каждому пользователю удобный UI для вещей, которые иначе
пришлось бы делать через CLI администратора.

### Что нового в v0.10.8?

Бот теперь отвечает в едином конверте: одна строка заголовка
с контекстом, тело, и опциональная подпись для длинных
ответов. Подключены 11 контекстов (`registry`, `codex`,
`version`, `ack`, `bind`, `unbind`, `add`, `del`, `err`,
`welcome`, `welcome_back`). v1 API помощников не изменён.

### Тест-статус на VM

* `go test -count=1 -short ./...` — 12 / 12 пакетов PASS
* `bash scripts/smoke.sh` — 118 / 118 (59 ru + 59 en)
* `python3 scripts/check_exit_nodes.py` — 3 / 3 релейных ноды OK

### Что делаем дальше

* **Butler voice v3** — отметки срочности в заголовке
  (`🪶` / `🪶!` / `🪶!!`) и статуса в теле. Отложим до
  обратной связи по v2.
* **Ротация персональных API-токенов** — TTL + auto-rotate.
  Кандидат на v0.10.9.

---

## Migration from v0.10.7

No schema changes. No breaking changes. The envelope adds a
header line and (for verbose replies) a footer line; the body
is unchanged. Custom integrations that grep bot replies for
specific substrings (`"rules:"`, `"Logged in as"`, …) still
work — the v2 envelope only ADDS lines, never modifies the
body.

---

## v0.10.7 → v0.10.8

* **Commit range**: `f4c2cd3..422507e` (15 commits)
* **Lines changed**: 752 insertions, 268 deletions across 6 files
* **Schema**: unchanged. No migration required.
* **Smoke**: 118 / 118 (unchanged)
* **Packages**: 12 / 12 (unchanged)
