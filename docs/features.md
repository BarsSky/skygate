# Skygate — реализованные возможности

Этот документ описывает, что умеет Skygate на текущий момент (v1.5.0-alpha1)
в стиле краткого справочника. Используются только обезличенные примеры
(IP из RFC 5737, домен `example.com`, имена `node-1`, `user-1` и т.п.).
Реальные операторские данные здесь не приводятся.

> **Audience:** администратор, впервые развернувший Skygate, либо вернувшийся
> после долгого перерыва и желающий вспомнить, какие фичи есть и как они
> связаны друг с другом.
>
> **Где смотреть runtime:** в UI доступны два сайдбара — админский
> (виден операторам с флагом `is_admin`) и пользовательский. Все
> разделы UI документированы ниже с привязкой к HTTP-маршрутам.

---

## 1. Архитектура в одном абзаце

Skygate — это веб-панель + Telegram-бот + CLI, которая оборачивает
[headscale](https://github.com/juanfont/headscale) (open-source Tailscale
coordination server) и предоставляет его возможности не-инженерам:

- пользователи получают устройства, exit-ноды, preauth-ключи через
  веб-интерфейс;
- администратор управляет кластером, DERP-серверами, сертификатами
  и HA-цепочкой через те же формы;
- всё это хранится в PostgreSQL (с версии v1.3.0+), никаких файловых
  state, всё бэкапится через S3/SMB/NFS/SFTP.

Главные сущности в системе: **пользователь** (portal_users +
headscale user), **устройство** (headscale node), **preauth-ключ**
(одноразовый токен для регистрации устройства), **exit-нода**
(выделенная нода, через которую другие устройства ходят в интернет),
**DERP-сервер** (relay для трафика Tailscale, когда прямой NAT
недоступен), **headscale ACL** (policy.json, применяемая ко всему
tailnet).

---

## 2. Пользовательские страницы (видны всем)

| Маршрут | Что делает | Заметки |
|---|---|---|
| `/login` | Логин по локальной паре user/password или Telegram. | Сессия в cookie. CSRF-токен выдаётся автоматически. |
| `/dashboard` | Сводка: текущий tailnet, статус собственных устройств, уведомления. | Колокольчик уведомлений в правом верхнем углу. |
| `/my/devices` | Список устройств пользователя + кнопки "Продлить" (B160) и "Удалить" (B162). | Кэш headscale 5s; кнопка "Обновить" сбрасывает. |
| `/my/keys` | Список preauth-ключей + кнопка "Выпустить" + "Reissue". | B155: custom TTL + reusable. B159: device column. |
| `/my/tokens` | Личные API-токены с авто-ротацией. | B153: UI, B154: scheduler. |
| `/my/exit-nodes` | Список exit-нод + per-device preference. | "Pin к конкретной exit-ноде" + strict/any. |
| `/my/exit-rules` | ACL для "какому устройству какие домены разрешены через exit". | Поддержка CDN, /16, /24, конкретные /32. |
| `/my/telegram` | Привязка Telegram-аккаунта к Skygate-аккаунту. | QR-код для bind, токен для unbind. |
| `/my/account` | Смена пароля, язык, имя. | RU/EN переключатель. |
| `/my/notifications` | Полный список уведомлений. | B157.1: filter pills + пагинация. |

### 2.1. Регистрация нового устройства

Форма "Добавить устройство" на `/my/devices` (B155 + B165):

1. Выбрать ОС из тайлов (Android / iOS / Linux / macOS / Windows).
   Тайл автоматически подсвечивается по User-Agent браузера.
2. Опционально: выставить **Custom TTL** (число + h/d/w/y; 0 = бессрочно).
3. Опционально: поставить галку **Многоразовый** (reusable).
4. Нажать **Выпустить ключ**.
5. Скопировать выданный одноразовый ключ и вставить в Tailscale-клиент.

Подробная инструкция (с командами `tailscale up --login-server=...
--authkey=...`) — в блоке FAQ внизу страницы.

### 2.2. SSH-ключи на серверах-устройствах

Если вы регистрируете Linux-сервер, который будет работать **exit-нодой**
или **subnet-router**, на этом сервере должен быть настроен SSH-ключ,
под которым skygate сможет логиниться для автонастройки маршрутов.
Генерация:

```bash
# На сервере, который будет exit-нодой
ssh-keygen -t ed25519 -N "" -f /root/.ssh/id_ed25519
cat /root/.ssh/id_ed25519.pub   # скопировать в authorized_keys
```

После того как skygate знает публичный ключ сервера, укажите его путь
в настройках exit-ноды на `/admin/exit-nodes` (поле `ssh_key_path`),
а `ssh_target` — `user@host:port`.

---

## 3. Админ-страницы

### 3.1. Устройства и ноды (Devices & Nodes)

| Маршрут | Что делает |
|---|---|
| `/admin/devices` | Список всех устройств во всех headscale-плоскостях. Per-row: OS/type, last seen, accept-routes. |
| `/admin/exit-nodes` | CRUD по exit-нодам. SSH-target для автонастройки маршрутов на удалённом сервере. |
| `/admin/exit-rules` | Глобальные правила "устройство → домен через exit" (для всех пользователей). |
| `/admin/derp` | Статус локального derper (если он запущен в контейнере). |
| `/admin/derp/relays` | CRUD по DERP-серверам: hostname, region_id, region_code, region_name, sort_order, enabled. |
| `/admin/derp/relays/init` | **B164** — инициализация нового DERP-сервера на новом хосте (admin указывает host/SSH, skygate сам настраивает). |

### 3.2. Контроль доступа (Access Control)

| Маршрут | Что делает |
|---|---|
| `/admin/acls` | Просмотр и применение headscale ACL (policy.json). |
| `/admin/headscale` | Конфиг control plane (URL, API key). |
| `/admin/headplane` | Линк на headplane web UI (если установлен). |
| `/admin/control-planes` | Multi-plane (один headscale на пользователя). |
| `/admin/users` | Список пользователей + per-user ACL overrides. |
| `/admin/meshes` | Per-user subnet mesh CRUD. |

### 3.3. Здоровье и логи (System Health & Logs)

| Маршрут | Что делает |
|---|---|
| `/admin/system_tests` | **B163** — список системных проверок (DB, network, headscale, derp, exit-nodes, integrations) + "Run all" + history tab. |
| `/admin/services` | Статус всех внешних сервисов (headscale, headplane, telegram, tailscale). |
| `/admin/audit` | Полный audit log с фильтрами по user/action/date. |
| `/admin/update` | Доступные релизы + auto-update scheduler + Apply-кнопка. |

### 3.4. Интеграции (Integrations)

| Маршрут | Что делает |
|---|---|
| `/admin/integrations` | Сводка: что подключено, что нет. |
| `/admin/telegram` | Конфиг Telegram-бота (token, команды, rate limits). |
| `/admin/tailscale` | Tailscale-специфичные настройки (Funnel, MagicDNS, base domain). |
| `/admin/certificates` | **B148** — загрузка TLS-сертификата + DNS-01 toggle (Let's Encrypt). |
| `/admin/derp` | (см. выше). |
| `/admin/ha` | **B149** — HA-цепочка: active/standby ноды, force promote/demote, reg.ru credentials. |
| `/admin/deploy` | **B150** — кнопки push/pull бинаря между нодами, test-failover dry-run. |

### 3.5. Данные (Data)

| Маршрут | Что делает |
|---|---|
| `/admin/backup` | Создание/восстановление бэкапа, расписание. |
| `/admin/backup/config` | Куда бэкапить (SMB/NFS/SFTP/**S3** — B-TD-4 pending). |
| `/admin/subnets` | Per-user subnet CRUD (сеть + статус). |
| `/admin/integrations` | См. выше. |

### 3.6. Настройки и пользователи (Settings & Users)

| Маршрут | Что делает |
|---|---|
| `/admin/settings` | Глобальные настройки (DNS, update policy, default TTL). |
| `/admin/users` | См. выше. |
| `/admin/control-planes` | См. выше. |
| `/admin/invites` | Приглашения новых пользователей (magic-link / single-use codes). |
| `/admin/meshes` | См. выше. |
| `/admin/headscale` | См. выше. |
| `/admin/devices` | См. выше. |

---

## 4. Интеграции

### 4.1. Headscale

- API client — `internal/headscale/` (полный набор: nodes, users, preauth,
  ACL, tags, routes).
- `headscale_url` хранится per-user в `portal_users.headscale_url` (multi-plane).
- API key шифруется AES-256-GCM (master key из `SKYGATE_SECRET_KEY`).
- `HSForUser(uid)` маршрутизирует вызовы в правильную плоскость + кэш 5s.

### 4.2. Headplane (опционально)

- Если установлен, skygate показывает ссылку на `/admin/headplane`.
- API key прописывается в `.env` при первом деплое (см. `init-headplane.sh`).

### 4.3. Telegram-бот

- Команды: `/start`, `/devices`, `/keys`, `/exit`, `/exit_rules`,
  `/notifications`, `/bind`, `/unbind`, и т.п. (полный список ~20).
- Per-user chat (пользователь биндит свой аккаунт, получает личные
  уведомления). Если не забиндил — кнопка "Telegram" в `/my/telegram`.
- i18n: RU + EN, переключение `/language`.

### 4.4. Tailscale

- Tailscale Funnel: нет (отключено администратором — на РФ-сетях
  обычно недоступен).
- MagicDNS: домен берётся из `SKYGATE_BASE_DOMAIN` (по умолчанию
  `example.com`).
- preauth-ключи выдаются через веб-форму (см. `/my/devices`).
- Subnet-router и exit-node: через `/admin/exit-nodes` + SSH.

### 4.5. PostgreSQL

- v1.3.0+: единственный бэкенд. SQLite удалён.
- Driver: `pgx/v5` (pure Go, CGO не нужен).
- Миграции: `internal/db/migrations_pg.go` (с контрольной суммой).
- 60+ миграций, последняя — `V062PG` (notifications).

### 4.6. S3 / SMB / NFS / SFTP (бэкапы)

- `internal/backup/dest_*.go` — одна реализация на протокол.
- Расписание: in-app scheduler (`internal/backup/scheduler.go`).
- Auto-verify: `PRAGMA integrity_check` на ежедневной копии
  (восстановленной во временный файл). Telegram-алерт при
  несоответствии.

### 4.7. OIDC (B161)

- skygate как OIDC-провайдер для headscale. Tailscale-клиент →
  headscale → `/oidc/authorize` → `/login` (если не залогинен) →
  `/oidc/token` → `/oidc/userinfo` → headscale создаёт пользователя.
- 4 env vars: `SKYGATE_OIDC_ISSUER`, `SKYGATE_OIDC_CLIENT_ID`,
  `SKYGATE_OIDC_CLIENT_SECRET`, `SKYGATE_OIDC_KEY_DIR`.
- RSA-2048 keypair, persisted на диск (kid stable через рестарт).
- Discovery: `/.well-known/openid-configuration`.
- JWKS: `/oidc/jwks.json`.
- Authorize: `/oidc/authorize` (S256 PKCE, exact-match redirect_uri).
- Token: `/oidc/token` (RS256-signed JWT, 1h TTL).
- Userinfo: `/oidc/userinfo` (Bearer auth, sub + email + name +
  preferred_username).

### 4.8. DERP (B164 init flow)

- Существующая релейная сеть добавляется через `/admin/derp/relays`
  (просто заполнить форму с hostname + region).
- Новая: `/admin/derp/relays/init` — форма с параметрами + кнопка
  "Initialize". Skygate подключается по SSH к указанному хосту,
  устанавливает derper, настраивает его, регистрирует в
  `derp_relays` и пишет audit log.

### 4.9. Certsync (B147)

- 30s tick: читает `s3://<bucket>/certs/.version`, если SHA
  изменился — тянет cert+key, валидирует (x509 + matching key
  по PKCS#1/PKCS#8/SEC1), пишет на диск, дёргает Caddy reload.
- 7-дневный self-check: предупреждает в Telegram, если
  локальный сертификат вот-вот истечёт.

### 4.10. HA-цепочка (B145 + B149 + B150)

- Active-Passive (по решению администратора).
- `internal/ha/chain.go` — список нод в порядке приоритета.
- `internal/ha/elector.go` — Patroni-derived role, heartbeat 5s,
  missed-threshold 3 (= 15s).
- `/admin/ha` — UI для редактирования цепочки, force promote/demote,
  настройки reg.ru DNS credentials.
- `/admin/deploy` — push/pull бинаря через S3 + test-failover dry-run.
- External DNS: pluggable (`internal/dns/provider.go`), reg.ru
  реализован (B146) — IP whitelist пока не пропагировался, но
  auth работает.

---

## 5. Переменные окружения (env vars)

Сгруппированы по подсистеме. Реальные значения в проде у администратора,
здесь — только примеры / типы.

### 5.1. Основные

```
SKYGATE_BASE_DOMAIN=example.com
SKYGATE_BIND=0.0.0.0:8080
SKYGATE_LISTEN=8080
SKYGATE_SECRET_KEY=<32-byte-base64-random>
SKYGATE_DB_DSN=postgres://skygate:<password>@postgres:5432/skygate?sslmode=disable
```

### 5.2. Headscale

```
HEADSCALE_URL=http://headscale:50443
HEADPLANE_URL=http://headplane:50445
HEADPLANE_API_KEY=<random>
```

### 5.3. Telegram

```
SKYGATE_TELEGRAM_BOT_TOKEN=<from-botfather>
SKYGATE_TELEGRAM_OPS_CHAT_ID=<chat-id-for-admin-alerts>
```

### 5.4. OIDC (B161)

```
SKYGATE_OIDC_ISSUER=https://skygate.example.com
SKYGATE_OIDC_CLIENT_ID=headscale
SKYGATE_OIDC_CLIENT_SECRET=<random-32-bytes>
SKYGATE_OIDC_KEY_DIR=/data/oidc-keys
SKYGATE_OIDC_REDIRECT_URIS=https://head.skynas.ru/oidc/callback
```

### 5.5. Бэкапы (S3 / SMB / NFS / SFTP)

```
SKYGATE_BACKUP_ENABLED=true
SKYGATE_BACKUP_SCHEDULE=0 3 * * *
SKYGATE_BACKUP_S3_BUCKET=skygate-backups
SKYGATE_BACKUP_S3_ENDPOINT=http://minio:9000
SKYGATE_BACKUP_S3_ACCESS_KEY=<minio-user>
SKYGATE_BACKUP_S3_SECRET_KEY=<minio-pass>
```

### 5.6. Update / auto-update (B128 + B129 + B130)

```
SKYGATE_UPDATE_SCHEDULE_ENABLED=false
SKYGATE_UPDATE_SCHEDULE_TIME=03:00
```

### 5.7. HA / chain (B145 + B149 + B150)

```
SKYGATE_HA_ENABLED=true
SKYGATE_HA_HEARTBEAT_INTERVAL=5s
SKYGATE_HA_MISSED_THRESHOLD=3
SKYGATE_HA_PEER_HOSTNAME=skygate-standby
SKYGATE_DNS_PROVIDER=regapi
SKYGATE_DNS_REGAPI_LOGIN=<reg-ru-user>
SKYGATE_DNS_REGAPI_PASSWORD=<alt-password>
SKYGATE_DNS_REGAPI_ZONE=example.com
```

### 5.8. Certsync (B147)

```
SKYGATE_CERTSYNC_ENABLED=true
SKYGATE_CERTSYNC_S3_BUCKET=skygate-backups
SKYGATE_CERTSYNC_LOCAL_DIR=/var/lib/skygate/certs
SKYGATE_CERTSYNC_INTERVAL=30s
```

### 5.9. Preauth auto-rotation (B155) + token rotation (B154)

```
SKYGATE_KEY_NOTIFY_ENABLED=true
SKYGATE_KEY_NOTIFY_SCHEDULE=0 9 * * *
SKYGATE_TOKEN_AUTOROTATE_ENABLED=true
```

---

## 6. Бэкапы и аварийное восстановление

### 6.1. Локальный бэкап

`scripts/backup.sh` (или через UI: `/admin/backup` → "Run now"):

1. `pg_dump` от текущей базы (включая ACL snapshots, audit log, etc.)
2. `sqlite3 .backup` от headscale (если он на SQLite).
3. `git bundle` от исходников (с тега + изменений).
4. Tar-архив с контрольной суммой в `SKYGATE_BACKUP_DESTINATION`
   (SMB / NFS / SFTP / S3).

### 6.2. Восстановление

`scripts/restore.sh <backup-file>` (или через UI: `/admin/backup`
→ "Restore"):

1. Распаковка архива.
2. `psql < skygate-pg.sql` (или `sqlite3 < skygate-pg.sql` если старая версия).
3. Восстановление headscale SQLite.
4. `git checkout` от сохранённого тега.
5. Рестарт контейнеров.

### 6.3. Tier-1 HA failover (если развёрнут)

1. skygate-host-1 падает → Patroni переключает PG на standby.
2. `ha_elector.go` замечает missed heartbeat > 15s.
3. `ha_elector.go` дёргает `dns.UpdateRecord` (reg.ru / cloudflare / etc.).
4. DNS резолвит в svyatoslava-1 (P2) в течение TTL.
5. Telegram-алерт оператору.
6. certsync на новой ноде подтягивает актуальный сертификат из S3.

RTO < 1 минута, RPO = 0 (Patroni async replication).

---

## 7. Сценарии оператора (operator cookbook)

### 7.1. Добавить пользователя

1. `/admin/users` → "Создать".
2. Заполнить username, email, headscale_user_id (или нажать
   "Provision" — авто-создание контейнера).
3. Выдать invite (магическая ссылка или одноразовый код).
4. Пользователь регистрируется, биндит Telegram, начинает работать.

### 7.2. Добавить exit-ноду

1. На сервере, который будет exit-нодой:
   - Установить Tailscale: `curl -fsSL https://tailscale.com/install.sh | sh`.
   - Залогиниться в headscale: `sudo tailscale up --login-server=https://head.example.com --hostname=exit-1 --advertise-exit-node`.
   - Сгенерировать SSH-ключ: `ssh-keygen -t ed25519 -N "" -f /root/.ssh/id_ed25519`.
   - Добавить pubkey в `/root/.ssh/authorized_keys`.
2. В Skygate: `/admin/exit-nodes` → "Add".
3. Заполнить `node_id` (= headscale numeric id), `hostname`, `ssh_target`
   (`root@exit-1.example.com:22` или `root@100.64.0.5` если через Tailscale),
   `ssh_key_path` (`/root/.ssh/id_ed25519`).
4. Нажать "Use Tailscale IP" (если хотите через Tailscale).
5. Approve routes в headscale: `headscale nodes approve-routes -i <node-id>`.
6. На `/admin/exit-rules` создать правила "user → exit-1".

### 7.3. Добавить DERP-сервер на новом хосте (B164)

1. На новом хосте должен быть SSH-доступ с ключом.
2. В Skygate: `/admin/derp/relays/init`.
3. Заполнить:
   - `hostname` (например, `derp-fra-1.example.com`)
   - `region_id` (1-999, уникальный в tailnet)
   - `region_code` (`fra`, `ams`, ...)
   - `region_name` ("Frankfurt relay 1")
   - `ssh_user` (например, `root`)
   - `ssh_key_path` (например, `/root/.ssh/id_ed25519`)
   - `ssh_port` (по умолчанию 22)
   - `sort_order` (1 = primary, 2 = secondary, ...)
4. Нажать "Initialize & Register".
5. Skygate по SSH установит derper, настроит его, добавит в
   `derp_relays` и вернёт "успех" + audit log.
6. Следующий `tailscale up --login-server=...` на клиентах
   автоматически подхватит новый DERP.

### 7.4. Продлить сессию устройства (B160)

1. `/my/devices`.
2. Найти строку с истекающим устройством (красный / жёлтый pill в
   колонке "Истекает").
3. Нажать "Продлить" — сессия устройства продлится на 30 дней.
4. Или `/my/devices?refresh=1` для сброса кэша headscale (если
   статус не обновляется).

### 7.5. Удалить устройство (B162)

1. `/my/devices`.
2. Найти строку с устройством, которое нужно удалить.
3. Нажать "Удалить" → подтвердить в диалоге.
4. Skygate дёрнет `headscale nodes delete -i <id>` и очистит
   `node_owner_map` для этого id.
5. Tailscale-клиент на устройстве сразу теряет связь с tailnet
   (next netmap sync).

### 7.6. Сменить TLS-сертификат (B148)

1. `/admin/certificates`.
2. Вставить PEM cert + key (или загрузить файлом).
3. Нажать "Apply".
4. certsync на всех нодах подхватит новый cert в течение 30s
   (через S3 polling).
5. Caddy reload дёргается автоматически (best-effort).

### 7.7. Переключить active ↔ standby (B149 / B150)

1. `/admin/ha`.
2. Нажать "Force promote" на нужной ноде.
3. Через 5s elector подхватит новое состояние + audit log
   "ha.force_promote".
4. Если включён external DNS — A-record переключится автоматически.

### 7.8. Просмотреть системные тесты (B163)

1. `/admin/system_tests` — список проверок.
2. Нажать "Run all" — все тесты выполнятся параллельно.
3. У каждой строки:
   - ✓/✗/⏸ иконка
   - Название + описание
   - **Output** — раскрывающийся блок с полным выводом (включая
     multi-line FAIL-причины)
4. История за 7d / 30d / all во вкладке "History".

---

## 8. Под капотом: что в каждом B-check

Этот раздел — справочный, для тех, кто хочет понять, какой B-check
за что отвечает. Нумерация `B-N` (B1, B2, ...) — каталог гарантий,
выполняемый перед каждым push.

| B | Тема | Что проверяет |
|---|---|---|
| B11-B17 | Catalog | i18n-каталог (RU+EN), build status, базовые проверки |
| B18 | PG-only | runtime использует pgx, нет sqlite-импортов |
| B20-B40 | UX | Devices/exit-rules страницы, кнопки, формы |
| B91-B95 | System | Catalog гарантий, verify-pre, verify-post |
| B128-B130 | Update | Auto-update scheduler, Apply-кнопка, schedule |
| B145 | HA chain | `internal/ha/` — chain + elector + pluggable DNS |
| B146 | DNS live | reg.ru API client + IP whitelist |
| B147 | Certsync | S3 ↔ local certs sync, x509 validate, Caddy reload |
| B148 | Certificates | `/admin/certificates` upload + DNS-01 toggle |
| B149 | /admin/ha | HA chain editor, force actions, reg.ru creds |
| B150 | /admin/deploy | push/pull + test-failover |
| B151-B152 | DNS + UI | provider-agnostic + font-awesome bundle |
| B153-B154 | Tokens | UX + auto-rotation scheduler |
| B155 | Preauth UX | custom TTL + Reissue |
| B156-B157 | Notify | Telegram scheduler + bell in layout |
| B158 | Fonts | self-hosted Google Fonts (24 woff2) |
| B159 | /my/keys UX | device column + relative-time + cleanup |
| B160 | Device renew | `/my/devices/{id}/renew` + 410 Gone fix + cache bypass |
| B161 | OIDC | discovery + JWKS + authorize + token + userinfo |
| **B162** | Device delete | `/my/devices/{id}/delete` + scope check |
| **B163** | Sys-test UX | collapsible FAIL output |
| **B164** | DERP init | SSH-based derper install on new host |
| **B165** | Reg UI fix | stable form layout + SSH-key example |
| **B166** | Renew tests | e2e + system tests for B160 |

---

## 9. Что **не** описано здесь

- API.md — отдельный документ для HTTP API (для интеграций).
- Telegram-формат сообщений — `docs/bot-message-style-v0.15.2.md`.
- DB schema — `docs/db-schema.md`.
- HA execution plan — `docs/internal/ha-v1.5.0-execution.md`.
- Disкавери recovery — `docs/disaster-recovery.md`.
- Команды CLI (`skygate deploy`, `skygate ha promote`) — `docs/deploy.md`.
- Staticcheck / linting / verify-pre — `AGENTS.md`.
