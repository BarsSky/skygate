# SkyGate Adoption Audit — 2026-09-11

> **Источник:** лог живого развёртывания на `188.253.20.31` (aro, существующий Headscale на `127.0.0.1:8081`) + статический анализ `cmd/skygate`, `internal/headscale`, `internal/acl`, `internal/nodeownership`, `internal/db`, `internal/handlers`, `Dockerfile`, `Dockerfile.prebuilt`.
>
> **Цель:** понять, какие действия skygate реально предпринимает при установке рядом с живым headscale; где дыры; что доработать.

---

## 0. TL;DR

1. **Подключение к существующему headscale — есть.** Переменные `HEADSCALE_URL`, `HEADSCALE_API_KEY`, `HEADSCALE_SERVER_URL` в `.env`. Внутри — `internal/headscale/Client` (gRPC/HTTP REST), кэш на 30s, `ListUsers`, `ListNodes`, `GetACL` (с file-mode fallback).
2. **Анализ и конвертация существующих правил — НЕТ.** `acl.GenerateACL()` регенерирует политику **с нуля** из таблицы `device_rules` и **перезаписывает** существующий headscale ACL через `PUT /api/v1/policy`. `internal/feature/admin/acl_import.go` — это ручная вставка JSON через форму (`/admin/acls/import`), а не «прочитай текущий ACL → разбери по device_rules».
3. **Существующие ноды — частично.** На каждой загрузке `/my/devices` запускается `nodeownership.Backfill` с 4 стратегиями (A: PreAuthKeyID; C: 1h окно; D: tag-префикс; E: OIDC). Ни одна не ловит ноды, зарегистрированные через `headscale preauthkeys create` + `tailscale up --authkey=...` (без `/my/preauth` и без dev-tag). Админская кнопка `Sync from headscale` (`POST /admin/devices/sync-from-headscale`) загружает **все** ноды headscale → `node_owner_map`, но её надо нажать руками — нет first-run wizard, нет авто-запуска при старте.
4. **Веб-ассеты — НЕ встроены.** `templates/*.html` собраны через `//go:embed` (бинарь их содержит). А `static/css/*.css`, `static/webfonts/*.woff2`, `static/favicon.svg` отдаются через `http.ServeFile(w, r, "./static/"+clean)` (`internal/handlers/static.go:46`). **Ни `Dockerfile`, ни `Dockerfile.prebuilt` не копируют `static/` в образ.** На `prebuilt` пути (CI-билд, рекомендованный для прода) бинарь стартует, но CSS/шрифты/favicon отдают 404.
5. **Документация расходится со схемой:** `docs/db-schema.md` упоминает `node_owner_map.user_id`; реальная колонка — `headscale_user_id` + `username` (UNIQUE). `device_rules` дополнительно требует `user_name`, `device_hostname` (NOT NULL) — в API примере не показано. `docs/skygate-as-shell.md:114` явно пишет «ACL import is the missing piece» — то есть это **известная дыра v0.13.0**, не закрытая.

---

## 1. Подключение к существующему headscale — что реально работает

### 1.1 Env-контракт (cmd/skygate/main.go)

| Переменная | Назначение | Где читается |
|---|---|---|
| `HEADSCALE_URL` | Публичный URL control plane | `headscale.NewClient` (gRPC target) |
| `HEADSCALE_SERVER_URL` | Для `tailscale up --login-server` | entrypoint.sh |
| `HEADSCALE_API_KEY` | Bearer token для REST/gRPC | `c.apiKey` |
| `HEADSCALE_BASE_DOMAIN` | Для ACL `src:` / ACL грантов | `acl.GenerateACLForPlane` |
| `HEADSCALE_CONTAINER` | Имя контейнера (для CLI fallback `docker exec`) | `headscale.acl.go:74` |

### 1.2 Что умеет `internal/headscale/Client`

Методы (по `headscale.go` + соседним файлам):

- `ListUsers() ([]HSUser, error)` — кэш 30s
- `ListAllNodes() ([]NodeView, error)` — кэш 30s
- `GetACL() (string, error)` — `GET /api/v1/policy`, fallback `docker exec headscale headscale policy get`
- `SetPolicy(policy string) error` — `PUT /api/v1/policy`, file-mode fallback (`acl_policy.hujson` + `docker restart`)
- `AddTag` / `UntagNode` / `RenameNode` / `DeleteNode` / `ApproveRoutes`
- `RegisterUser` / `CreatePreAuthKey` / `ExpirePreAuthKey`

Health-check: `HEADSCALE_URL/health` пингуется в entrypoint.sh (60s ceiling, `SKYGATE_HEADSCALE_WAIT_TIMEOUT`).

### 1.3 B237.18 cron — что он реально делает

`internal/headscale/reconcile.go` (`ReconcileUsers`):

- Один раз за цикл вызывает `hs.ListUsers()` (кэш 30s → фактически free).
- Для каждого `portal_users.headscale_user_id`:
  - **ok**: ID совпал с существующим headscale user.
  - **linked**: было `NULL/0`, нашли по `username` (lowercase) → UPDATE.
  - **relinked**: ID указывал на несуществующего user-а, нашли по username → UPDATE.
  - **orphan**: ничего не нашли, аудит-запись.
- **НЕ удаляет** `portal_users` строки автоматически (только аудит).
- Опция: `SKYGATE_RECONCILE_HEADSCALE_USERS_ENABLED=true` (default).

**Это и есть единственная авто-конвертация при первом старте** — по пользователям, но не по нодам и не по ACL.

---

## 2. Что skygate делает с уже существующими правилами (ACL)

### 2.1 При каждой попытке применить ACL

`internal/acl/acl.go::GenerateACLForPlane(d, planeBaseDomain)`:

1. Читает **всю** таблицу `device_rules` для этого plane.
2. Группирует по `(user, device, exit)` → строит headscale-гранты с `via=[exit_tag]`.
3. Добавляет `tagOwners`, `groups`, `ssh` блоки.
4. Возвращает **JSON, не имеющий ничего общего с тем, что было в headscale до skygate**.

`acl.ApplyACLPipelineForPlane` (упомянут в BACKLOG и `internal/acl/acl.go`) потом:

1. `acl.GenerateACLForPlane` → JSON.
2. `SaveACLSnapshot` → пишет snapshot в БД (для rollback).
3. `hs.SetPolicy(json)` → `PUT /api/v1/policy`.
4. `AuditRow` + опционально `Notifier.SendAlert`.

**Если в headscale уже была политика** (hand-written, из Headplane, из шаблона), она **перезаписывается** на первом же Re-apply. Никакого diff, никакого merge — новая политика целиком заменяет старую.

### 2.2 `/admin/acls/import` — НЕ конвертация

`internal/feature/admin/acl_import.go` показывает форму загрузки файла/textarea. После валидации (`validateImportedACL` — только shape check: `acls`, `tagOwners`, `groups`, `ssh` есть как top-level keys) и нажатия Apply:

- `acl.SetACLForAllPlanes(...)` — перебирает все `headscale_url` planes, для каждого достаёт `headscale.Client` через `HSForUserFn(uid)` (по `portal_users.headscale_url` lookup).
- Для каждого plane: `hs.SetPolicy(policy)`.
- Audit + Telegram alert.

Это **raw JSON replace**, не парсинг существующего ACL в `device_rules`. Оператор, у которого в headscale 50 правил, должен:
1. Снять `headscale policy get > existing.json`.
2. **Вручную** распарсить его в `device_rules` (SQL INSERT) или переписать JSON руками.
3. Залить через `/admin/acls/import`.

`docs/skygate-as-shell.md:114` явно называет это «missing piece for plug-in without breaking anything»:

> ### v0.13.0 — ACL import / export (B+C in this doc)
> The big one: when an operator starts using Skygate, they have an existing headscale ACL policy (from Headscale's defaults, Headplane, or hand-written, or whatever). Skygate's `GenerateACL()` currently writes a different policy shape (per-user isolation). Importing the existing policy as-is is the missing piece for "plug in without breaking anything".

### 2.3 Что skygate НЕ делает с существующими правилами

| Действие | Статус |
|---|---|
| Прочитать текущий headscale ACL при старте | ❌ Нет (только вручную через `/admin/acls/import` форму) |
| Распарсить существующий ACL в `device_rules` | ❌ Нет |
| Слить существующий ACL со сгенерированным | ❌ Нет |
| Зарезервировать «чужие» grants (operator-set, hand-edited) | ❌ Нет — `GenerateACL` переписывает всё |
| Импортировать существующие теги узлов | ⚠️ Частично: `SyncNodesFromHeadscale` берёт tag из `headscale.ListAllNodes()` и пишет в `node_owner_map` |
| Конвертировать существующие preauth-keys | ❌ Нет — `Backfill` работает только с preauth-keys, **выданными через `/my/preauth`** (Strategy A) |
| Сделать dry-run preview перед первым Re-apply | ⚠️ Частично: `/admin/acls` показывает текущий, но не делает diff с тем, что будет |

---

## 3. Что skygate делает с существующими узлами

### 3.1 Авто-backfill при загрузке `/my/devices`

`internal/nodeownership/nodeownership.go::Backfill`:

| Strategy | Условие матча | Что ловит |
|---|---|---|
| A | `n.PreAuthKeyID == preauth_keys.headscale_preauth_id` | Ноды, зарегистрированные через `/my/preauth` |
| C | `n.CreatedAt` в пределах 1h от любого preauth-key пользователя | Temporal fallback для A |
| D | У ноды уже есть `tag:dev-<username>-*` | Когда оператор сам навесил тег через CLI |
| E (B175) | `n.PreAuthKeyID == ""` + `n.User.Name == portal_username` | OIDC-ноды |

### 3.2 Что НЕ ловится автоматически

- **Ноды, зарегистрированные через `headscale preauthkeys create` + `tailscale up --authkey=...`** (как у оператора в логе): PreAuthKeyID есть, но в `preauth_keys` его нет (skygate ничего не знает про этот ключ). Ни одна стратегия не срабатывает.
- **Ноды без тега**, у которых `User.Name != portal_username` (например, унаследованные от старого headscale user-а).

### 3.3 Админская кнопка `Sync from headscale`

`POST /admin/devices/sync-from-headscale` (`internal/feature/admin/devices.go:286`) вызывает:

1. `HSGlobalFn().ListAllNodes()` → все ноды headscale.
2. Для каждой: `db.SyncNodeInfo{ID, Hostname, Tag, Username=n.UserName, HSUserID=...}`.
3. `db.SyncNodesFromHeadscale(...)` → upsert в `node_owner_map` (INSERT OR IGNORE / UPDATE, `tag` пропускается если пустой или `tag:untagged`).
4. Audit: `node_sync_from_headscale` `inserted=N updated=M`.
5. Redirect с `?ok=...`.

**Это и есть «одноразовая конвертация» для существующих нод.** Она не анализирует правила, не трогает `device_rules`, не запускает автоматически. Только кнопка.

### 3.4 Что НЕ делает `Sync from headscale`

- Не заполняет `devices` (таблица для `/my/devices` отображения) — только `node_owner_map`.
- Не помечает узлы как exit (`exit_servers`) автоматически — только если тег `tag:exit-node` уже висит в headscale.
- Не создаёт `device_rules`.
- Не вешает `tag:dev-<username>-*` на ноды, у которых его нет.

### 3.5 Что есть ещё на `/admin/devices`

| Кнопка | Что делает | Файл |
|---|---|---|
| Sync from headscale | Одноразовый pull всех нод в `node_owner_map` | `internal/feature/admin/devices.go:286` |
| Force resync all tags | Прогоняет `nodeownership.Backfill` для каждого portal user (применяет dev-tags) | `internal/feature/admin/devices.go` (B77) |
| Transfer | Перевесить ноду на другого portal user | `internal/feature/admin/devices.go` (B99) |
| Tag / Untag | Навесить `tag:private` или `tag:public` | `internal/feature/admin/devices.go` |
| Delete | Удалить ноду из headscale + skygate (B162/B169/B171) | `internal/feature/admin/devices.go` |
| Meta (OS, device_type) | Ручное переопределение | `internal/feature/admin/devices.go` |

### 3.6 Чего нет, но нужно при «поставили сверху»

- **First-run wizard / banner**: «у вас 4 ноды в headscale, у 0 portal_users; нажмите Sync, чтобы привязать».
- **Bulk «claim all nodes for this headscale user»**: оператор выбрал headscale user → все его ноды привязались к выбранному portal user.
- **Auto-sync on first run** (флагом `SKYGATE_IMPORT_EXISTING_NODES=true` для разовых миграций).
- **Implied exit-node detection**: ноды с `tag:exit-node` или advertised routes → автоматически в `exit_servers`.

---

## 4. Веб-ассеты — где сидит баг

### 4.1 Что встроено в бинарь

`internal/handlers/templates.go:19`:

```go
//go:embed templates/*.html templates/*/*.html
var tplFS embed.FS
```

HTML-шаблоны (`admin/*.html`, `user/*.html`, `layout.html`) компилируются в бинарь. ✅

### 4.2 Что НЕ встроено

`internal/handlers/static.go:46`:

```go
http.ServeFile(w, r, "./static/"+clean)
```

Читает с диска. ❌ Встраивания нет (grep `//go:embed` показывает только `templates/*.html`, `templates/*/*.html`, `bundles/setup.sh`, `bundles/README.md`, `templates_test/templates`).

### 4.3 Что ломается на prebuilt пути

`Dockerfile.prebuilt:118`:

```
COPY --from=builder /out/skygate /app/skygate
COPY entrypoint.sh /entrypoint.sh
```

Только бинарь + entrypoint. `static/` НЕ копируется. После старта контейнера `WORKDIR=/app`, `ls /app/static` → пусто.

`Dockerfile:122-123`:

```
COPY entrypoint.sh /entrypoint.sh
```

То же самое — только entrypoint.

### 4.4 Что это ломает на UI

`internal/handlers/templates/layout.html:21-22`:

```html
<link rel="stylesheet" href="/static/css/font-awesome.min.css">
<link rel="stylesheet" href="/static/css/themes.css">
```

`favicon.svg` тоже отдельной ручкой:

```go
http.ServeFile(w, r, "./static/favicon.svg")
```

`themes.css` содержит `@font-face` правила для всех 7 шрифтов из `static/webfonts/*.woff2` (B158). Всё это на prebuilt пути → 404.

**Симптом:** страница рендерится, но без CSS — голый HTML, шрифты системные, иконок нет, тёмная тема не работает (всё в `:root` через CSS-переменные).

**На dev пути** (bind-mount `./:/app`) работает, потому что `./static/` есть на хосте. Но оператор из лога использует `ghcr.io/barssky/skygate:latest` (prebuilt), и получает именно эту поломку.

### 4.5 Дополнительная проблема

`internal/handlers/static.go:46` использует относительный путь `./static/`. Это зависит от `cwd` бинаря. Если оператор запустит бинарь не из `/app`, а из `/opt/skygate/skygate` — `static/` не найдётся. То есть баг не только в Docker — он в самом Go-коде.

---

## 5. Дыры в документации

### 5.1 Что не сходится

| Док | Что говорит | Реальность |
|---|---|---|
| `docs/db-schema.md` (предположительно) | `node_owner_map.user_id` | Колонки `headscale_user_id`, `username`, `tagged_by_user_id` — `user_id` НЕТ |
| `docs/skygate-as-shell.md` | «ACL import is the missing piece» (v0.13.0) | Реализована только ручная JSON paste/replace, не conversion |
| `internal/handlers/templates/exit_rules_help.html` | «knownSubdomains map» (rutracker.org → static.rutracker.cc) | Есть `knownSubdomains` в `internal/feature/exit_rules/sync.go:41`, но в help'е не упомянуто, что авто-updater ловит только HTML парсингом |
| README | «SQLite by default» (упоминается в issue из лога) | v1.3.0+ — Postgres-only, SQLite удалён |
| `docs/deploy.md` | Возможно, упоминается `SKYGATE_PORT` как host-publish | На prebuilt пути бинарь слушает `SKYGATE_PORT` внутри контейнера, что ломает healthcheck |
| Документация по `preauth_keys.headscale_preauth_id` | Strategy A ловит всё | Не ловит headscale CLI-выданные ключи |
| `docker-compose.lite.yml` | «only SkyGate, you already have Headscale» — нет Postgres | v1.3+ refuses to start без `SKYGATE_DB_DSN` |

### 5.2 Чего нет в принципе

- **Документация по first-run adoption**: оператор пришёл со своим headscale — что нажимать.
- **Документация по «convert existing rules»**: как перевести hand-written ACL в `device_rules` (с примерами SQL или скриптом).
- **Раздел про архитектуру «sidecar mode»** (B90): skygate + чужой headscale, без поднятия своего. Сейчас это «implicit mode», но явных инструкций нет.
- **Troubleshooting для «мне выдало no-device в /my/exit-rules»**: пошаговая диагностика (headscale users list → portal_users → Sync → Re-apply).
- **Troubleshooting для «CSS не загружается»**: проверка `./static/`, prebuilt vs dev image.
- **Документация по `node_owner_map` колонкам** — что они значат, какая из них «primary key» по смыслу.

### 5.3 Что можно улучшить в AGENTS.md

- Зафиксировать что `static/` НЕ embedded — чтобы следующий разработчик не наступил.
- Добавить секцию «Sidecar mode (existing headscale)» с перечнем env vars и обязательных шагов.
- Зафиксировать «админская кнопка Sync from headscale» как официальный путь для first-run adoption.

---

## 6. План доработки (по приоритетам)

### Фаза A — блокеры деплоя (должны быть в v1.5.3 / v1.5.4)

#### A1. Встроить `static/` в бинарь (B-mod-static-embed)

**Проблема:** prebuilt-образ отдаёт CSS/шрифты 404.

**Фикс:**
1. `internal/handlers/static.go` — добавить `//go:embed static/*` + `var staticFS embed.FS`, переписать `StaticHandler` на `fs.Sub(staticFS, "static")` + `http.FileServer(http.FS(sub))`.
2. `internal/handlers/static.go::FaviconHandler` — то же самое с `//go:embed`.
3. Удалить зависимость от `./static/` на диске. Заменить на in-memory `embed.FS`.
4. **Не нужно** править `Dockerfile` или `Dockerfile.prebuilt` — ассеты в бинаре.
5. Проверить: `http_get /static/css/themes.css` → 200 с правильным Content-Type; `http_get /static/webfonts/fa-solid-900.woff2` → 200; `http_get /favicon.svg` → 200.
6. B-check script `check_b_mod_static_embed.sh`:
   - `static.go` содержит `//go:embed` для `static/`
   - Нет `http.ServeFile(w, r, "./static/`
   - `go build ./...` + `go test ./internal/handlers/...` зелёные
   - AGENTS.md упоминает B-mod-static-embed

#### A2. Документация / логирование prebuilt-пути в Dockerfile (мелкое, но важно)

Если по какой-то причине A1 не подходит (например, embed.FS не работает с большими файлами шрифтов), fallback:
- В `Dockerfile.prebuilt`: `COPY static/ /app/static/` после `COPY --from=builder /out/skygate`.
- В entrypoint.sh: pre-flight `test -d /app/static || { echo "FATAL: /app/static missing"; exit 1; }`.

#### A3. B-check `check_b_mod_static_served.sh` (live)

После A1: `wget -qO- http://localhost:8080/static/css/themes.css | head -1` → должно вернуть CSS, не 404.

---

### Фаза B — first-run adoption (для «пришли со своим headscale»)

#### B1. First-run banner + кнопка (B-mod-first-run)

**Проблема:** оператор не понимает, что нужно нажать Sync, и сидит с no-device.

**Фикс:**
1. На странице логина (или на `/dashboard`) — баннер «У вас N нод в headscale, не привязанных к skygate. [Sync from headscale]», если `SELECT COUNT(*) FROM node_owner_map WHERE tagged_by_user_id = 0` мало (или любой другой маркер «несинхронизировано»).
2. На `/admin/devices` — вынести «Sync from headscale» в более заметное место + добавить tooltip со ссылкой на docs.
3. Help-url: `/docs/sidecar-mode` (новый документ, см. B5).

#### B2. Bulk claim для headscale user → portal user (B-mod-claim-all)

**Проблема:** `Sync from headscale` заполняет `node_owner_map` с `username = headscale_user_name`, но не привязывает `portal_user_id` явно (его там и нет, но фильтры `WHERE username = ?` могут не работать если есть case-mismatch).

**Фикс:**
1. Новый endpoint: `POST /admin/devices/claim-all-for-user` с параметрами `portal_user_id`.
2. Handler:
   - Берёт `portal_user.username` и `portal_user.headscale_user_id`.
   - `SELECT node_id FROM node_owner_map WHERE (headscale_user_id = ? OR username = ?) AND tagged_by_user_id IN (0, current_admin)`.
   - Для каждого: UPDATE `tagged_by_user_id = portal_user.id, username = portal_user.username`.
3. Audit: `node_claim_all user=<id> nodes=<count>`.
4. UI: на `/admin/users/{id}` — кнопка «Claim all nodes from headscale for this user».

#### B3. Auto-detect exit-nodes (B-mod-auto-exit)

**Проблема:** нода с `tag:exit-node` или с advertised routes 0.0.0.0/0 не попадает в `exit_servers` автоматически.

**Фикс:**
1. Расширить `SyncNodesFromHeadscale` (или сделать отдельный `SyncExitNodesFromHeadscale`):
   - Для каждой ноды с `tag:exit-node` ИЛИ advertised routes, содержащими `0.0.0.0/0` или `::/0`:
     - INSERT INTO `exit_servers` (если нет) с `enabled=1`.
2. Запускать из той же `Sync from headscale` кнопки + из `BackfillInfra` (cron).
3. Audit: `exit_node_auto_detected node=<id>`.

#### B4. Auto-sync on first run (флаг, B-mod-autosync)

**Проблема:** даже с B1-B3, оператор может проигнорировать кнопку.

**Фикс:**
1. Env: `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true` (default `false`).
2. На старте: если `node_owner_map` пустая И `portal_users` ≥ 1 И флаг true → вызвать `SyncNodesFromHeadscale` + `SyncExitNodesFromHeadscale` однократно.
3. Audit: `first_run_auto_sync nodes=<count>`.
4. Через 24h или после первого `node_owner_map` > 0 строк → авто-выключается (не повторяется).

#### B5. Документ `docs/sidecar-mode.md` (B-mod-docs-sidecar)

**Что должно быть:**
- Prerequisites (headscale уже стоит, API key есть, base_domain знаем).
- `HEADSCALE_URL`, `HEADSCALE_API_KEY`, `HEADSCALE_SERVER_URL`, `HEADSCALE_BASE_DOMAIN` — что куда.
- Что **НЕ** нужно делать: skygate не поднимает свой headscale/Postgres/DERP.
- Что skygate **делает** при первом старте (только reconcile пользователей по username).
- Что skygate **не делает** (не импортирует ноды, не импортирует ACL).
- **Обязательные ручные шаги:**
  1. `/admin/devices` → «Sync from headscale».
  2. `/admin/exit-nodes` → добавить exit-ноду (или подождать B3).
  3. `/admin/devices` → проверить, что у каждой ноды правильный portal user.
  4. `/admin/exit-rules` → «Re-apply ACL» (с предупреждением «это перезапишет существующий ACL, сначала снимите бэкап»).
- Troubleshooting:
  - `/my/exit-rules` пустой → см. выше.
  - CSS не загружается → проверка prebuilt vs dev, см. B-mod-static-embed.
  - «headscale_user_id is null» → `SELECT * FROM portal_users WHERE headscale_user_id IS NULL`.

---

### Фаза C — анализ и конвертация существующих правил

#### C1. `headscale policy get` → dry-run preview (B-mod-policy-preview)

**Проблема:** оператор не видит, что именно перезапишется при первом Re-apply.

**Фикс:**
1. Новый endpoint: `GET /admin/acls/preview-import-from-headscale` (admin-only).
2. Handler:
   - `hs.GetACL()` → raw policy JSON.
   - Парсит grants, для каждого определяет: это skygate-style grant (per-user, с `via=`) или hand-edited.
   - Возвращает diff: «эти grants будут удалены, эти переписаны, эти останутся».
3. UI: `/admin/acls` — новая кнопка «Preview existing headscale ACL».
4. Audit: `acl_preview_existing`.

#### C2. Парсер headscale ACL → `device_rules` (B-mod-acl-convert)

**Проблема:** оператор должен вручную переводить grants в `device_rules`.

**Фикс:**
1. Пакет `internal/acl/import.go`:
   - `func ParseACL(json string) ([]ImportableGrant, error)` — парсит HuJSON policy, классифицирует grants:
     - **Per-user src** (`"src": ["user@"]`) → 1 portal user.
     - **CIDR dst** (`"dst": ["1.2.3.0/24:*"]`) → `target_type=cidr`.
     - **Domain dst** (через autogroup: или прямой домен с резолвом позже) → `target_type=domain`.
     - **`via:`** → exit_node pref.
   - Для каждой grant'ы: `func (g ImportableGrant) ToDeviceRule(portal_user_id int, device_ip string, exit_node_id string) DeviceRule`.
2. Endpoint: `POST /admin/acls/import-from-headscale` (admin-only):
   - `hs.GetACL()` → parse → preview page (как в C1) → operator click Apply → INSERT в `device_rules`.
3. Best-effort: ручные grants (group nesting, tag-based, нестандартный dst) помечаются «can't convert automatically» и НЕ импортируются. Оператор решает: пропустить или переписать руками.
4. Audit: `acl_imported_from_headscale grants=<count> skipped=<count>`.

#### C3. Бэкап текущего ACL перед первым Re-apply (B-mod-acl-backup)

**Проблема:** оператор нажал Re-apply → ручная политика потеряна.

**Фикс:**
1. На `POST /admin/exit-rules/reapply` (или любом триггере `SetPolicy`):
   - Перед `SetPolicy` — `hs.GetACL()` → сохранить в `acl_snapshots` (уже есть, проверить что именно).
   - Если `acl_snapshots` за последние N дней пусто — warning на UI: «вы уверены, что хотите Re-apply? Текущая политика: SHA256=..., размер=N байт. Бэкап: <ссылка>».
2. Проверить: `internal/acl/acl.go::ApplyACLPipelineForPlane` уже вызывает `SaveACLSnapshot`? Если да — добавить pre-write `GetACL` чтобы в snapshot попало то, что СЕЙЧАС в headscale, а не то, что мы только что сгенерировали.

#### C4. `Merge mode` (B-mod-acl-merge) — долгосрочно

**Что:** не перезаписывать существующие grants, а **добавлять** свои.

Сложно: требует парсинга + union, а не replace. После C1-C3 можно смотреть.

---

### Фаза D — документация

#### D1. Обновить `docs/db-schema.md` под реальную схему (B-mod-docs-schema)

- `node_owner_map`: колонки `node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname, os, device_type` (нет `user_id`).
- `device_rules`: добавить `user_name` и `device_hostname` (NOT NULL).
- `portal_users.headscale_user_id`: тип INTEGER (не TEXT), может быть NULL.

#### D2. Обновить `README` под v1.3+ (B-mod-docs-readme)

- Убрать упоминание SQLite как «по умолчанию».
- Добавить: «v1.3.0+ — PostgreSQL only. Без `SKYGATE_DB_DSN` не стартует».
- Выделить секцию «Sidecar mode (existing headscale)» со ссылкой на `docs/sidecar-mode.md`.

#### D3. `docker-compose.lite.yml` — добавить postgres (B-mod-compose-lite)

- Либо добавить `postgres` сервис в `docker-compose.lite.yml`.
- Либо пометить файл как «DEPRECATED, use docker-compose.yml» + комментарий «v1.3+ требует Postgres».

#### D4. Обновить `AGENTS.md` (B-mod-docs-agents)

Добавить секцию:
- Sidecar mode: env vars, что работает, что не работает.
- Static assets: `embed.FS` для templates, НЕ для static (если B-mod-static-embed не сделан, иначе описать новую архитектуру).
- Известные дыры на 2026-09-11: ACL auto-convert, CLI-preauth node attribution, prebuilt static.
- `SKYGATE_PORT` — внутри контейнера = 8080, на хосте через publish. (см. issue 1 из лога)

---

### Фаза E — узкие UX-дыры

#### E1. Админ не может создать exit-rules за другого user (issue 4 из лога)

**Фикс:** `POST /admin/exit-rules` с `user_id` в теле (или impersonate). Handler копирует логику `POST /my/exit-rules`, но `src` берётся из device owner, не из admin session.

#### E2. `node_owner_map` schema расходится с примерами API (issue 6 из лога)

**Фикс:** API handler `/my/exit-rules/add` должен сам проставлять `user_name` и `device_hostname`, чтобы клиент мог слать только `device_id` + `target_*`.

---

## 7. Что НЕ в скоупе этого плана (отдельные задачи)

- Multi-control-plane adoption (B90 + B130): как импортировать несколько headscale одновременно.
- OIDC adoption: есть B174/B175, но это про login flow, не про «уже стоял OIDC, теперь поставили skygate».
- B-mod-ha (B150 deployment + HA chain): не относится к adoption.
- Backup/restore adoption: backup уже работает с существующим Postgres, отдельная задача.

---

## 8. Оценка трудоёмкости

| Фаза | Задач | Строк кода | B-check'ов | Примерная трудоёмкость |
|---|---|---|---|---|
| A (блокеры) | A1-A3 | ~150 | 6 contracts | 1-2 дня |
| B (first-run) | B1-B5 | ~400 | 20 contracts | 3-4 дня |
| C (ACL import) | C1-C3 | ~600 | 30 contracts | 4-5 дней |
| D (docs) | D1-D4 | ~100 (markdown) | 0 (read by humans) | 1 день |
| E (UX) | E1-E2 | ~200 | 10 contracts | 1-2 дня |

**Итого:** ~10-14 дней на полный план, **минимум для разблокировки sidecar-сценария** — A + B1 + B5 (~4-5 дней).
