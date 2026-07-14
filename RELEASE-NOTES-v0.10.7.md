# Skygate v0.10.7

> **First official release.** Tailscale / headscale self-service web portal
> with a Telegram butler bot. Production-ready: bilingual (ru + en), single
> container, ~5-min Go build, no external dependencies beyond headscale 0.29.

---

## 🇬🇧 English

### What is Skygate?

A small Go web portal for [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale) that gives every user a
friendly UI for the things they'd otherwise have to ask the admin to run
on the headscale CLI: preauth keys, device list, per-device exit-node
rules, and a Telegram butler for the same actions from a phone. The admin
keeps the headscale control plane; Skygate adds a thin layer on top that
talks to it through the headscale API + the local SQLite DB.

### What's in v0.10.7?

This release collects everything we shipped between `v0.10.0` and
`v0.10.7` and is the first one we call production-ready.

* **Tailscale in the skygate image** (Этап 14 v2). The skygate
  container runs `tailscaled` in its own network namespace. No
  sidecar, no shared-network hack. The container joins the tailnet
  with `--accept-routes` only (no `--exit-node`), and a relay VPS
  advertises the canonical Telegram IP ranges as subnet routes so
  `api.telegram.org` traffic for the bot flows through the relay
  while everything else (headscale, host services) stays direct.

* **Telegram reachability probe + banner** (Этап 14 v2). The admin
  Telegram page runs a 5 s `GET api.telegram.org` probe on every
  page load and shows one of three banner states: `ok_direct`,
  `ok_relay`, `unreachable`. The check is per-IP via
  `ip route get <ip>` (kernel routing table is the source of truth
  — tailscaled running is not enough).

* **Tailscale exit-node unification** (Этап 14 v7). All three
  relay nodes (emilia, sharlotta, karolina) advertise both
  `exit-node = true` and a shared set of subnet routes. Clients
  pick any exit, Tailscale handles failover by metric. There is
  no "primary" exit and no "Telegram-special" routing — every
  relay is interchangeable.

* **Backup config via UI** (v0.10.6). Replaces the env-var
  `BACKUP_DIR` with a per-deployment configuration in
  `global_settings`. Admin sets the destination, protocol
  (local / SMB / NFS / SFTP), mountpoint, credentials, retention
  count, schedule, and master switches from `/admin/backup`. Two
  schedulers consume the same config: an in-app goroutine
  (every 60 s) and a system-cron entry via
  `skygate backup-run`. The cron subcommand reads the config
  from the DB and runs the backup — single source of truth.

* **Telegram bot — butler-gatekeeper voice + i18n** (v0.10.4 +
  v0.10.5). The bot speaks in a butler-gatekeeper register
  ("Хранитель Порога" / "Threshold Warden"). Russian and English
  catalogs, per-chat preference stored in
  `telegram_bindings.lang`, auto-detect from
  `message.from.language_code` on first `/login`. Commands
  covered: `/start`, `/help`, `/lang`, `/login`, `/unbind_self`,
  `/status`, `/nodes`, `/rules`, `/audit`, `/exit_nodes`,
  `/quota`, `/ack`, `/version`, `/restart`.

* **Admin SSH into the relay VPSes** (v0.10.7). New SSH rule in
  the headscale ACL: `skyadmin@tsnet.skynas.ru → tag:public (root)`.
  Combined with the existing `tag:exit-node` rule, the operator
  can now manage emilia, sharlotta, and karolina from the
  tailnet without needing public-IP SSH on the host firewall.
  Triggered a `tag:exit-node` entry in `tagOwners` that was
  missing and was breaking reapply.

### Deployment

```bash
# 1. On the headscale host
git clone https://github.com/BarsSky/skygate.git
cd skygate
cp .env.example .env
# Edit .env: SKYGATE_ADMIN_PASS=<password>, HEADSCALE_URL=http://headscale:50444,
#           HEADSCALE_API_KEY=<key>, TS_AUTHKEY_FILE=/run/secrets/ts_authkey
# Optional: BOT_TOKEN, BACKUP_DIR, etc.

# 2. Build + start
docker compose up -d

# 3. Verify
curl -s -X POST http://localhost:8080/login -d "username=skyadmin&password=$PASS" -c /tmp/ck.txt
curl -s -b /tmp/ck.txt http://localhost:8080/dashboard -o /dev/null -w "%{http_code}\n"
# → 200

# 4. Smoke
make test
# → 118/118 smoke (59 ru + 59 en)
```

### Functionality matrix

| Surface | Path | Auth | Notes |
|---|---|---|---|
| Login / lang / logout | `/login`, `/lang`, `/logout` | public | cookie-based session |
| Dashboard | `/dashboard` | user | aggregated metrics |
| Devices | `/my/devices` | user | own tag:private nodes |
| Exit-rules CRUD | `/my/exit-rules` | user | per-device, multi-delete, cascade |
| Preauth keys | `/my/preauth`, `/my/keys` | user | 1h single-use + listing |
| API tokens | `/my/tokens` | user | Bearer auth for AI |
| Account | `/my/account` | user | password change |
| Telegram binding | `/my/telegram` | user | bind-by-QR, revoke, lang picker |
| Exit-nodes | `/my/exit-nodes` | user | Tailscale IP + country |
| Help | `/my/exit-rules/help`, `/help` | user | bilingual API reference |
| Admin users | `/admin/users` | admin | CRUD + reset password |
| Admin devices | `/admin/devices` | admin | tag/untag nodes |
| Admin ACL | `/admin/acls`, `/admin/exit-rules` | admin | view + rollback + reapply |
| Admin DERP | `/admin/derp` | admin | relay status snapshot |
| Admin audit | `/admin/audit` | admin | filtered log |
| Admin telegram | `/admin/telegram` | admin | token, probe, "Send test" |
| Admin backup | `/admin/backup` | admin | create/restore + UI config |
| Admin settings | `/admin/settings` | admin | URL, key, theme |
| Admin exit-nodes | `/admin/exit-nodes` | admin | exit-node CRUD |
| REST API | `/my/exit-rules/api` | Bearer | JSON for AI integration |
| Public REST | `/api/...` | rate-limited IP | future |

### Operational requirements

* **headscale 0.29** with a pre-created API key (admin level)
* **Tailscale** binary in the skygate image (auto-bundled)
* **SQLite** (CGO, `github.com/mattn/go-sqlite3`)
* **Go 1.23+** to build
* **Docker** for the runtime (compose file in repo)
* **Tailscale authkey** for the in-image `tailscaled` (optional —
  the container falls back to non-RF mode without it)

### Test status on VM

* `go test ./...` — race-free
* `bash scripts/smoke.sh` — **118 / 118** (59 ru + 59 en)
* `python3 scripts/check_exit_nodes.py` — 3 / 3 relay nodes
  advertise `0.0.0.0/0` + `::/0`
* Tailscale ACL has **2 SSH rules** (admin → tag:exit-node and
  admin → tag:public, both as root)

### Known limitations

* `go test ./...` requires running as a user with read access to
  `./data/ts/files/` (the Tailscale state dir). On a fresh host,
  `sudo chown -R $USER data/ts` first.
* `i18n` catalogs are auto-generated from EN. Russian
  translations live in `internal/i18n/catalog.go` and have been
  reviewed by a native speaker; new English keys added later
  must be re-translated (the parity test in
  `internal/i18n/i18n_test.go` will fail until you do).
* `internal/backup/` has SFTP via sshfs and SMB via mount.cifs
  — Linux-only. On Windows dev (where the binary is also
  compiled) `Mount` returns a clear error and the runner
  gracefully skips. Designed for the Linux container, not the
  dev host.

### What we're working on next

* **Butler voice v2** for the Telegram bot — cleaner formatting,
  a one-line header per reply with the request status, and a
  sign-off line at the end of long responses. The aim is for
  every message to read as if a real butler handed it to you at
  the door: brief, well-formed, never sloppy.
* v0.10.8 will follow.

---

## 🇷🇺 Русский

### Что такое Skygate?

Небольшой Go-портал для [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale), дающий каждому
пользователю удобный UI для вещей, которые иначе пришлось бы
делать через CLI администратора: preauth-ключи, список устройств,
per-device exit-node правила, и Telegram-дворецкий для тех же
действий с телефона. Администратор держит control plane headscale;
Skygate — тонкий слой поверх, который говорит с ним через API
headscale и локальную SQLite.

### Что в v0.10.7?

Этот релиз собирает всё, что мы отгрузили между `v0.10.0` и
`v0.10.7`, и первый, который мы называем production-ready.

* **Tailscale внутри образа skygate** (Этап 14 v2). Контейнер
  skygate запускает `tailscaled` в своём network namespace. Без
  sidecar-а и без shared-network-хака. Контейнер подключается
  к tailnet только с `--accept-routes` (без `--exit-node`), а
  relay-VPS анонсирует канонические Telegram IP-диапазоны как
  subnet routes — так трафик `api.telegram.org` для бота идёт
  через relay, всё остальное (headscale, host-сервисы)
  остаётся direct.

* **Probe достижимости Telegram + баннер** (Этап 14 v2). На
  странице `/admin/telegram` каждые 5 секунд делается
  `GET api.telegram.org` и показывается один из трёх статусов:
  `ok_direct`, `ok_relay`, `unreachable`. Проверка per-IP через
  `ip route get <ip>` (таблица маршрутизации ядра — источник
  истины; одного запущенного tailscaled мало).

* **Унификация exit-node в Tailscale** (Этап 14 v7). Все три
  relay-ноды (emilia, sharlotta, karolina) анонсируют и
  `exit-node = true`, и общий набор subnet routes. Клиент
  выбирает любой, Tailscale делает failover по metric. Нет
  «основного» exit-node, нет «специальной» маршрутизации для
  Telegram — все relay равноправные.

* **Конфиг бэкапа через UI** (v0.10.6). Заменяет env-var
  `BACKUP_DIR` на per-deployment конфиг в `global_settings`.
  Администратор задаёт destination, протокол (local / SMB /
  NFS / SFTP), mountpoint, credentials, retention, schedule и
  master switches со страницы `/admin/backup`. Два
  планировщика используют один конфиг: in-app горутина
  (каждые 60 с) и system-cron через `skygate backup-run`.
  Cron-сабкоманда читает конфиг из БД и запускает бэкап —
  единый источник истины.

* **Telegram-бот — голос дворецкого + i18n** (v0.10.4 +
  v0.10.5). Бот говорит регистром дворецкого-привратника
  («Хранитель Порога»). Каталоги на русском и английском,
  per-chat preference в `telegram_bindings.lang`, авто-детект
  по `message.from.language_code` при первом `/login`.
  Покрытые команды: `/start`, `/help`, `/lang`, `/login`,
  `/unbind_self`, `/status`, `/nodes`, `/rules`, `/audit`,
  `/exit_nodes`, `/quota`, `/ack`, `/version`, `/restart`.

* **Админ-SSH на relay VPS** (v0.10.7). Новое SSH-правило в
  headscale ACL: `skyadmin@tsnet.skynas.ru → tag:public (root)`.
  Вместе с существующим правилом `tag:exit-node` администратор
  может управлять emilia, sharlotta и karolina прямо из
  tailnet без публичного SSH на host-firewall. Потребовало
  добавить `tag:exit-node` в `tagOwners` (раньше
  отсутствовал и ломал reapply).

### Развёртывание

```bash
# 1. На хосте с headscale
git clone https://github.com/BarsSky/skygate.git
cd skygate
cp .env.example .env
# В .env отредактировать: SKYGATE_ADMIN_PASS=<пароль>,
#   HEADSCALE_URL=http://headscale:50444,
#   HEADSCALE_API_KEY=<ключ>,
#   TS_AUTHKEY_FILE=/run/secrets/ts_authkey
# Опционально: BOT_TOKEN, BACKUP_DIR и т.д.

# 2. Сборка и запуск
docker compose up -d

# 3. Проверка
curl -s -X POST http://localhost:8080/login -d "username=skyadmin&password=$PASS" -c /tmp/ck.txt
curl -s -b /tmp/ck.txt http://localhost:8080/dashboard -o /dev/null -w "%{http_code}\n"
# → 200

# 4. Smoke
make test
# → 118/118 smoke (59 ru + 59 en)
```

### Функциональная матрица

| Поверхность | Путь | Доступ | Заметки |
|---|---|---|---|
| Login / lang / logout | `/login`, `/lang`, `/logout` | public | cookie-сессия |
| Dashboard | `/dashboard` | user | сводные метрики |
| Devices | `/my/devices` | user | свои tag:private ноды |
| Exit-rules CRUD | `/my/exit-rules` | user | per-device, multi-delete, каскад |
| Preauth ключи | `/my/preauth`, `/my/keys` | user | 1ч single-use + листинг |
| API tokens | `/my/tokens` | user | Bearer auth для AI |
| Account | `/my/account` | user | смена пароля |
| Telegram binding | `/my/telegram` | user | bind-by-QR, revoke, выбор языка |
| Exit-nodes | `/my/exit-nodes` | user | Tailscale IP + страна |
| Help | `/my/exit-rules/help`, `/help` | user | двуязычный API-референс |
| Admin users | `/admin/users` | admin | CRUD + сброс пароля |
| Admin devices | `/admin/devices` | admin | tag/untag нод |
| Admin ACL | `/admin/acls`, `/admin/exit-rules` | admin | view + rollback + reapply |
| Admin DERP | `/admin/derp` | admin | снапшот статуса relay-ов |
| Admin audit | `/admin/audit` | admin | фильтрованный лог |
| Admin telegram | `/admin/telegram` | admin | токен, probe, «Send test» |
| Admin backup | `/admin/backup` | admin | create/restore + UI-конфиг |
| Admin settings | `/admin/settings` | admin | URL, ключ, тема |
| Admin exit-nodes | `/admin/exit-nodes` | admin | CRUD exit-node |
| REST API | `/my/exit-rules/api` | Bearer | JSON для AI-интеграции |
| Public REST | `/api/...` | rate-limited IP | в планах |

### Операционные требования

* **headscale 0.29** с заранее созданным API-ключом (admin-уровень)
* **Tailscale** бинарь внутри образа skygate (включён автоматически)
* **SQLite** (CGO, `github.com/mattn/go-sqlite3`)
* **Go 1.23+** для сборки
* **Docker** для рантайма (compose в репо)
* **Tailscale authkey** для in-image `tailscaled` (опционально —
  без него контейнер работает в non-RF режиме)

### Состояние тестов на VM

* `go test ./...` — race-free
* `bash scripts/smoke.sh` — **118 / 118** (59 ru + 59 en)
* `python3 scripts/check_exit_nodes.py` — 3 / 3 relay-ноды
  анонсируют `0.0.0.0/0` + `::/0`
* Tailscale ACL имеет **2 SSH-правила** (admin → tag:exit-node
  и admin → tag:public, оба как root)

### Известные ограничения

* `go test ./...` требует запуска от пользователя с доступом
  на чтение `./data/ts/files/` (Tailscale state dir). На свежем
  хосте сначала `sudo chown -R $USER data/ts`.
* `i18n` каталоги авто-генерируются из EN. Русские переводы
  живут в `internal/i18n/catalog.go` и проверены носителем;
  новые ключи, добавленные позже в EN, нужно переводить
  (тест `TestCatalogsParity` в `internal/i18n/i18n_test.go`
  упадёт, пока не переведёте).
* `internal/backup/` использует sshfs для SFTP и mount.cifs
  для SMB — только Linux. На Windows-разработке (где бинарь
  тоже компилируется) `Mount` возвращает понятную ошибку и
  runner корректно пропускает. Дизайн — для Linux-контейнера,
  не для dev-хоста.

### Что планируем дальше

* **Голос дворецкого v2** для Telegram-бота — чище
  форматирование, однострочный заголовок в каждом ответе со
  статусом запроса, и завершающая строка-подпись в конце
  длинных ответов. Цель — чтобы каждое сообщение читалось
  так, будто настоящий дворецкий подал его вам у двери:
  кратко, аккуратно, без небрежности.
* v0.10.8 выйдет следующим.

---

## 📋 Что включено (commits between v0.10.5 and v0.10.7)

* v0.10.5: Этап 14 v5 — bot i18n (ru + en, per-chat preference,
  auto-detect from `language_code`). 287 new `bot.*` keys,
  parity verified.
* v0.10.6: Этап 14 v6 — backup config via UI (local / SMB /
  NFS / SFTP). 2 schedulers, runMu serialization, new
  `internal/backup/` package, reapply endpoint.
* v0.10.7: Этап 14 v7 — Tailscale exit-node unification +
  admin SSH ACL for `tag:public` relays + reapply endpoint
  for ACL-only changes + `tag:exit-node` in `tagOwners`
  (was missing and broke reapply).

## 👥 Авторы / Authors

* Operator (`skyadmin`) — design, relay fleet, bot UX, exit-node
  topology decisions.
* Skygate — implementation, test coverage, ops docs.

## 📜 Лицензия / License

Proprietary. All rights reserved.
