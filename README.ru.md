# Skygate

[![CI](https://github.com/BarsSky/skygate/actions/workflows/ci.yml/badge.svg)](https://github.com/BarsSky/skygate/actions/workflows/ci.yml)
![Version](https://img.shields.io/badge/version-v0.9.0--dev-blue)
![Headscale](https://img.shields.io/badge/headscale-0.29-green)
![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8)

Веб-портал самообслуживания для [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale) — даёт пользователям
дружелюбный UI для получения preauth-ключей, просмотра своих устройств,
управления per-device правилами exit-node и (опционально) управления всем
этим через Telegram-бот без необходимости трогать CLI headscale.

> **English version:** [README.md](README.md).
> **Статус:** на теге `v0.9.0`. Работа в процессе на `main` — `v0.9.1-dev`.
> **Последний `make test` на VM:** зелёный — `59+59` smoke-ассертов,
> 3/3 exit-node анонсируют `0.0.0.0/0` + `::/0`, `go test ./...` race-free.

## Что умеет

**Для пользователей:**

- Войти на `/login` (без аккаунта Tailscale — портал сам всё разруливает)
- Получить одноразовый preauth-ключ на `/my/preauth` и выполнить
  `tailscale up --authkey <ключ>` на новом устройстве
- Увидеть свои устройства на `/my/devices`, свои preauth-ключи на
  `/my/keys` (с возможностью отозвать), свои exit-правила на
  `/my/exit-rules` (добавление / мульти-удаление / фильтр / поиск /
  каскад / очистка)
- Список доступных exit-node на `/my/exit-nodes` (Tailscale IP + страна)
- Персональные API-токены (Bearer) на `/my/tokens` для AI / скриптов
- Сменить свой пароль на `/my/account`
- Переключить язык интерфейса (EN / RU) в боковой панели

**Для админов** (`/admin/*`):

- `users` — создать / посмотреть / удалить пользователей портала
  (каждый = пользователь headscale)
- `devices` — все ноды во всех namespace'ах, с tag / un-tag
- `exit-rules` — иерархический вид по пользователям; очистка дублей
  `device_id`
- `exit-rules/rollback` — откат к предыдущему snapshot'у ACL
- `exit-rules/sync` — ручной триггер синхронизации advertised-routes
- `exit-nodes` — управление Tailscale-состоянием каждой exit-node
  (host, IP, AcceptRoutes, SSH-таргет)
- `acls` — read-only вид текущего headscale ACL
- `audit` — журнал кто-что-сделал (фильтры: `?action=…`, `?user=…`)
- `derp` — статус DERP-ретранслятора (peers, conn summary)
- `backup` — backup / restore ACL-политики headscale
- `telegram` — настройка бота (токен в `global_settings`, hot-swap)
- `settings` — per-user лимиты правил, макс. всего, DNS auto-update

**Для ops** (Telegram-бот, опционально но рекомендуется):

- Phase 1–4 read-only: `/status /help /nodes /rules /audit /exit_nodes
  /quota /ack /version /restart /help <command>`
- Phase 11–14 реальные операции: `/add_device /add_rule /delrule
  /clearrules /myexitnodes` — выпустить preauth-ключи, добавить/удалить
  exit-правила, управлять своими устройствами — прямо из чата
- Триггеры: применён ACL, сброс пароля, добавление/удаление правила,
  откат ACL, ошибка применения ACL — всё с префиксом `[#<id>]`,
  чтобы `/ack <id>` закрыл алерт
- Подробности: [docs/TELEGRAM.md](docs/TELEGRAM.md)

## Архитектура

- **Backend:** Go 1.23, один бинарник, stdlib `net/http` роутер
- **Хранилище:** SQLite (один файл, embedded в volume контейнера, WAL)
- **Шаблоны:** `html/template`, `embed.FS` — без Node, без JS-бандлера
- **Auth:** bcrypt (cost 12) + JWT (HS256) cookie, HttpOnly +
  SameSite=Lax; персональные API-токены (Bearer) для публичного REST API
- **Интеграция с headscale:** REST API с API-ключом; CLI-fallback через
  `docker exec headscale headscale …` для tag-операций (admin API не
  имеет прав); SSH для синхронизации advertised-routes на exit-node
- **i18n:** 270+ ключей каталога EN+RU, per-request locale через
  `atomic.Value` + funcmap `Tr/Trf`
- **Rate limits:** in-memory token bucket (per-username / per-IP),
  429 + `Retry-After` при блоке
- **Deploy:** Docker (Linux/WSL2) или нативный Go-бинарник (любая ОС
  с Go 1.23+)

Полная карта компонентов: [docs/architecture.md](docs/architecture.md),
модель данных: [docs/db-schema.md](docs/db-schema.md), HTTP API:
[docs/api.md](docs/api.md), установка/бэкап/восстановление:
[docs/deploy.md](docs/deploy.md).

## Быстрый старт (Linux + headscale на том же хосте)

```bash
# 1. Получить API-ключ headscale (на хосте с headscale)
docker exec headscale headscale apikeys create --expiration 365d
# или: headscale apikeys create --expiration 365d

# 2. Сгенерировать JWT-секрет
openssl rand -hex 32

# 3. Клонировать и сконфигурировать
git clone <repo> skygate
cd skygate
cp .env.example .env
nano .env          # заполнить HEADSCALE_API_KEY, SKYGATE_JWT_SECRET, SKYGATE_ADMIN_PASS
# Оставить HEADSCALE_URL=http://headscale:50444 для same-network.

# 4. Собрать и запустить
docker compose up -d --build
docker compose logs -f skygate

# 5. Открыть в браузере
curl -I http://localhost:8080/login         # должен вернуть 200
# затем http://localhost:8080/login
```

Дефолтный админ: `skyadmin` + пароль из `SKYGATE_ADMIN_PASS`.

Полная кросс-платформенная установка (Windows, восстановление из бэкапа,
DERP, headplane sidecar): [docs/deploy.md](docs/deploy.md).

## Удалённый headscale

Skygate ходит в headscale по HTTP. `HEADSCALE_URL` может указывать на
**любой** достижимый headscale — та же LAN, только Tailscale, за
reverse-proxy и т.д. Дефолт `http://headscale:50444` работает только
когда оба контейнера в одной docker-сети.

```bash
# Skygate нативно (не в Docker), headscale там же:
HEADSCALE_URL=http://localhost:50444

# Headscale на другом хосте в LAN (например 192.168.13.69):
HEADSCALE_URL=http://192.168.13.69:50444

# Headscale доступен только через Tailscale (без публичного IP):
HEADSCALE_URL=http://100.64.0.1:50444

# Headscale за HTTPS reverse-proxy:
HEADSCALE_URL=https://headscale.example.com
```

**Важно:** host:port должен быть достижим оттуда, где работает Skygate.
Если Skygate в Docker на хосте A, а headscale на хосте B — используйте
LAN-IP или Tailscale-IP хоста B; `localhost` не сработает.

API-ключ (`HEADSCALE_API_KEY`) глобальный для headscale и даёт полный
admin-доступ. Создайте его на хосте headscale, вставьте в `.env` Skygate,
не делитесь им.

## Reverse proxy + HTTPS

Skygate — только HTTP. Всегда ставьте его за TLS-терминатор.

- **Nginx Proxy Manager**: proxy host `skygate.example.com` →
  `http://192.168.13.69:8080`, LE-сертификат, force SSL.
- **Caddy** (одной строкой):
  ```
  skygate.example.com {
      reverse_proxy 192.168.13.69:8080
  }
  ```
- **nginx** (вручную): https://docs.nginx.com/nginx/admin-guide/web-server/reverse-proxy/

Куки HttpOnly + SameSite=Lax — работают за любым стандартным reverse-proxy.
Убедитесь, что прокси не срезает `Set-Cookie`.

## Безопасность

**Где живут секреты**

| Секрет | Файл | Права |
|---|---|---|
| `HEADSCALE_API_KEY` | `.env` на хосте Skygate | `chmod 600` (root или skyadmin) |
| `SKYGATE_JWT_SECRET` | `.env` на хосте Skygate | `chmod 600` |
| `SKYGATE_ADMIN_PASS` | `.env` на хосте Skygate | `chmod 600`; используется только при первом старте |
| `skygate.db` (bcrypt-хеши + audit log) | volume `/var/lib/skygate` | `chmod 700` |

`.env` в `.gitignore` — никогда не коммитится.

**Ротация**

- `HEADSCALE_API_KEY`:
  ```bash
  # на хосте headscale
  docker exec headscale headscale apikeys create --expiration 365d
  # вставить новый токен в .env Skygate, перезапустить контейнер
  docker compose restart skygate
  # удалить старый ключ когда готовы
  docker exec headscale headscale apikeys expire <old-key-id>
  ```
- `SKYGATE_JWT_SECRET`: перегенерировать, вставить в `.env`, перезапустить.
  **Внимание:** это разлогинит всех пользователей и отзовёт все
  персональные API-токены.
- `SKYGATE_ADMIN_PASS`: удалить пользователя из SQLite, задать новый
  `SKYGATE_ADMIN_PASS`, перезапустить.

**Что НЕ отображается в UI**

`HEADSCALE_API_KEY` **никогда не рендерится в HTML**. Чтобы
использовать ключ для Headplane — скопируйте его вручную из `.env` на
хосте Skygate. Это сделано специально: любой отрисованный секрет может
утечь через скриншоты, расширения браузера или XSS.

**Другое hardening**

- Пароль админа: bcrypt cost 12 (специально медленно)
- Сессии: JWT HS256, TTL 24h, HttpOnly + SameSite=Lax
- Куки за HTTPS: reverse-proxy не должен срезать `Secure`
  (в nginx `proxy_cookie_flags Secure httponly`)
- Skygate на `127.0.0.1`, наружу только через reverse-proxy:
  в `docker-compose.yml` поставьте `ports: ["127.0.0.1:8080:8080"]`
- Per-IP и per-username rate limits на `/login` и `/api`

## Разработка

```bash
# Быстрая итерация
make build              # GOTOOLCHAIN=local go build -o ./skygate ./cmd/skygate
make run                # build + ./skygate
make go-test            # go test ./...
make smoke              # HTTP smoke (59+59 = 118 ассертов, двуязычный)
make check-nodes        # проверяет что exit-nodes анонсируют 0.0.0.0/0 + ::/0
make audit-routes       # статический аудит main.go vs handlers
make test               # go-test + audit-routes + smoke + check-nodes (всё вместе)
```

Шаблоны лежат в `internal/handlers/templates/`, в бинарник встраиваются
через `//go:embed`. Поменяли — пересобрали — перезапустили.

Для AI-ассистентов: сначала прочитайте [AGENTS.md](AGENTS.md) — там
полная карта файлов, schema-gotchas и правила работы на VM vs Windows.

## Куда смотреть

| Хочется… | Идти в |
|---|---|
| Карту компонентов, поток данных | [docs/architecture.md](docs/architecture.md) |
| Все таблицы и колонки БД | [docs/db-schema.md](docs/db-schema.md) |
| Каждый HTTP-эндпоинт + curl | [docs/api.md](docs/api.md) |
| Deploy / backup / restore / DERP | [docs/deploy.md](docs/deploy.md) |
| Настройка Telegram-бота + команды | [docs/TELEGRAM.md](docs/TELEGRAM.md) |
| История изменений по версиям | [CHANGELOG.md](CHANGELOG.md) |
| Карта файлов, gotchas, AI-хинты | [AGENTS.md](AGENTS.md) |
| Скрипты первоначальной настройки клиента | [docs/scripts/skygate_exit_node_setup.sh](docs/scripts/skygate_exit_node_setup.sh) |
| Workflow AI-агента (knaga) | [docs/SYNC.md](docs/SYNC.md) |

## Статус (live)

- **CI:** зелёный (см. бейдж выше — `go vet + go test -race + go build +
  audit_routes.py` на `ubuntu-24.04`)
- **VM `make test`:** зелёный с последнего push'а — точная метка в
  футере коммита через `git describe --tags --always`
- **Карта исходников:** см. [AGENTS.md](AGENTS.md) — поддерживается
  в актуальном состоянии по декомпозиции `handlers*.go` /
  `exit_rules*.go` / `internal/headscale/*.go`

## Roadmap

### Сделано (v0.6.0+)

- ✅ Per-user headscale ACL с гранулярной видимостью (фикс Android-баг)
- ✅ Фильтры audit-лога по user и action (`/admin/audit?action=…&user=…`)
- ✅ Per-user лимиты правил (`SKYGATE_USER_MAX_RULES=skyadmin:2000,alice:500`)
- ✅ Очистка осиротевших /32 правил (`/admin/exit-rules/cleanup`)
- ✅ Per-exit-node Tailscale-политика `AcceptRoutes`
- ✅ Двуязычный EN/RU веб-UI (270+ ключей)
- ✅ Telegram-бот (реальные операции: preauth, правила, устройства,
  restart, version)
- ✅ Персональные API-токены (Bearer)
- ✅ Self-service смена пароля
- ✅ Rate limits (login + api)
- ✅ Статический аудит роутов (`scripts/audit_routes.py` в CI)
- ✅ Юнит-тесты для `acl`, `headscale`, `telegram`, `i18n`, `db`

### Не сделано

- ⏳ Фильтр audit-лога по **дате** (сейчас работает только `?action=`
  и `?user=`)
- ⏳ Email-уведомления при создании пользователя
- ⏳ QR-код для мобильной регистрации (альтернатива
  `tailscale up --authkey …`)
- ⏳ Переименование устройств через UI (сейчас только со стороны headscale)
- ⏳ Интеграция с Gitea (per-user provisioning API-ключей)
- ⏳ UI ротации admin API-ключа (ротация headscale API-ключа описана
  в [Безопасность → Ротация](#безопасность), но пока не одной кнопкой)
- ⏳ Headplane replacement — `GenerateACL()` всё ещё рукописный;
  долгосрочно план — оставить его как fallback, а Headplane отдать
  редактор политик для нетривиальных конфигов

`ACL editor (currently Headplane-only)` был в v0.6.0 roadmap и
остаётся там by design — у нас нет собственного визуального
ACL-редактора, а Headplane — канонический инструмент. Если сегодня
нужен визуальный редактор — подключите Headplane с тем же
`HEADSCALE_API_KEY` (см. раздел [Безопасность](#безопасность) насчёт
секретов).

---

## Лицензия

Proprietary — см. апстрим `LICENSE`, когда появится. Пока считайте как
"all rights reserved" и спрашивайте перед распространением.
