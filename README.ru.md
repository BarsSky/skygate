# Skygate

[![CI](https://github.com/BarsSky/skygate/actions/workflows/ci.yml/badge.svg)](https://github.com/BarsSky/skygate/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/BarsSky/skygate?label=Latest)](https://github.com/BarsSky/skygate/releases/latest)
![Headscale](https://img.shields.io/badge/headscale-0.29.x-green)
![Go](https://img.shields.io/badge/go-1.25%2B-00ADD8)
![License](https://img.shields.io/badge/license-MIT-green)

Веб-портал самообслуживания для [Tailscale](https://tailscale.com) и
[headscale](https://github.com/juanfont/headscale). Даёт каждому
пользователю дружелюбный UI, чтобы получить preauth-ключи, увидеть
свои устройства, управлять per-device правилами exit-node с
DNS-автообновлением, переключать preferred exit-node per-device и
(опционально) работать через Telegram-бот — без необходимости
трогать CLI headscale.

> **English version:** [README.md](README.md).
> **Статус (v0.33.1.17):** кросс-проверка между `device_rules` и
> preferred exit-node устройства (ловит баг «правило сохранено, но
> Tailscale его игнорирует»). Все 27 пакетов зелёные
> (`go test -count=1 -short ./...`), 66/66 verify-pre чеков
> проходят, in-process system_tests покрывает 22+ тестов, включая
> новый `exit_rules.preferred_mismatch`. Смотрите
> [latest release notes](https://github.com/BarsSky/skygate/releases/latest).

## Что умеет

**Для пользователей** (`/my/*`):

- Войти на `/login` (без аккаунта Tailscale — портал сам всё
  разруливает)
- Получить одноразовый preauth-ключ на `/my/preauth` и выполнить
  `tailscale up --authkey <ключ>` на новом устройстве
- Увидеть свои устройства на `/my/devices` (с авто-детектом
  ОС + тип устройства, per-device preferred exit-node, кнопка
  «Set as preferred»)
- Управлять preauth-ключами на `/my/keys` (с отзывом)
- Управлять exit-правилами на `/my/exit-rules` (добавление /
  мульти-удаление / фильтр / поиск / каскад / очистка;
  DNS-автообновление превращает `domain`-правила в нужный набор
  `/32` или CDN CIDR; cross-check баннер, когда exit-node правила
  не совпадает с preferred устройства)
- Список доступных exit-node на `/my/exit-nodes` (Tailscale IP,
  страна, статус онлайн)
- Персональные API-токены (Bearer) на `/my/tokens` для AI / скриптов
- Сменить свой пароль на `/my/account`
- Переключить язык интерфейса (EN / RU) в боковой панели

**Для админов** (`/admin/*`):

- `users` — создать / посмотреть / удалить пользователей портала
  (каждый = пользователь headscale)
- `devices` — все ноды во всём tailnet'е, с кнопками tag / un-tag
  и per-device счётчиком «мёртвых правил» (v0.33.1.17+)
- `exit-rules` — кросс-пользовательский иерархический вид с
  per-row индикатором «Preferred»; cleanup дублей `device_id`
- `exit-rules/rollback` — откат к предыдущему snapshot'у ACL
- `exit-rules/sync` — ручной триггер синхронизации advertised-routes
- `exit-nodes` — управление Tailscale-состоянием каждой exit-node
  (host, IP, AcceptRoutes, SSH-таргет, preferred как admin override)
- `acls` — read-only вид текущего headscale ACL
- `audit` — журнал кто-что-сделал (фильтры: `?action=…`, `?user=…`,
  `?ip=…`)
- `derp` — статус DERP-ретранслятора (peers, conn summary)
- `backup` — backup / restore ACL-политики headscale
- `telegram` — настройка бота (токен в `global_settings`, hot-swap,
  per-chat egress relay selector)
- `headscale` — мониторинг релизов headscale (latest tag из
  juanfont/headscale GitHub)
- `headscale/acl` — визуальный ACL-редактор для политики headscale
- `system_tests` — in-process тесты (network / db / headscale /
  exit_rules / disk / replication / backup / integrations)
- `settings` — per-user лимиты правил, макс. всего, DNS auto-update
- `update` — in-app self-update orchestrator с auto-rollback
- `telegram-bind`, `meshes`, `invites`, `integrations`, `derp`,
  `subnets` — дополнительные операторские страницы

**Для ops** (Telegram-бот, опционально но рекомендуется):

- Read-only: `/status /help /nodes /rules /audit /exit_nodes /quota
  /ack /version /restart /help <command>`
- Реальные операции: `/add_device /add_rule /delrule /clearrules
  /myexitnodes` — выпустить preauth-ключи, добавить/удалить
  exit-правила, управлять своими устройствами — прямо из чата
- Триггеры: применён ACL, сброс пароля, добавление/удаление правила,
  откат ACL, ошибка применения ACL — всё с префиксом `[#<id>]`,
  чтобы `/ack <id>` закрыл алерт
- Подробности: [docs/TELEGRAM.md](docs/TELEGRAM.md)

## Архитектура

- **Backend:** Go 1.25+ (один бинарник, stdlib `net/http` роутер)
- **Хранилище:** SQLite по умолчанию; PostgreSQL 14+ опционально
  через build-флаг `-tags postgres` (`SKYGATE_DB_DSN=postgres://…`).
  Одна схема, одни миграции, один `db.BackendOf` диспетчер — никаких
  изменений в коде для переключения.
- **Шаблоны:** `html/template`, `embed.FS` — без Node, без JS-бандлера.
  Per-feature шаблоны в `internal/handlers/templates/`.
- **Auth:** bcrypt (cost 12) + JWT (HS256) cookie, HttpOnly +
  SameSite=Lax; персональные API-токены (Bearer) для публичного REST
  API
- **Интеграция с headscale:** REST API с API-ключом; CLI-fallback
  через `docker exec headscale headscale …` для tag-операций (admin
  API не имеет прав); SSH для синхронизации advertised-routes
- **Headplane (опциональный sidecar):** визуальный ACL-редактор
  + админ-кабина. Версия пинится через `HEADPLANE_IMAGE` в `.env`,
  по умолчанию `ghcr.io/tale/headplane:0.6.3`. См.
  [docs/headplane.md](docs/headplane.md) для интеграционного
  контракта. `HEADPLANE_ENABLED=false` — отключить sidecar.
- **i18n:** 1 000+ ключей каталога EN + RU, per-request locale
  через `atomic.Value` + funcmap `Tr / Trf`. Per-feature каталоги
  (12 штук) в `internal/i18n/catalog_*.go`.
- **Rate limits:** in-memory token bucket (per-username / per-IP),
  429 + `Retry-After` при блоке
- **Deploy:** Docker (Linux/WSL2) или нативный Go-бинарник (любая
  ОС с Go 1.25+)

Полная карта компонентов: [docs/architecture.md](docs/architecture.md),
модель данных: [docs/db-schema.md](docs/db-schema.md), HTTP API:
[docs/api.md](docs/api.md), установка/бэкап/восстановление:
[docs/deploy.md](docs/deploy.md).

## Ключевые фичи (v0.16 → v0.33)

- **Per-user subnets** — каждый пользователь получает логический
  namespace `10.0.<uid>.0/24` в ACL; subnet router анонсирует LAN
  пользователя (v0.16.6+)
- **Per-user preferred exit-node** — настраивается на `/my/devices`
  per-device или на `/admin/users/{id}/subnet` per-user
  (v0.28.1+ / v0.28.4+)
- **Кросс-проверка exit-rule / preferred** — баннер + кнопка на
  `/my/exit-rules`, per-row колонка «Preferred» на
  `/admin/exit-rules`, per-device бейдж «мёртвых правил» на
  `/admin/devices`, и тест `exit_rules.preferred_mismatch` в
  `/admin/system_tests` (v0.33.1.17)
- **Домен-правила с DNS-автообновлением** — `target_type='domain'`
  резолвится в `/32` каждые 5 мин; для Cloudflare/Fastly/Google/
  Akamai per-IP churn заменяется published CIDR-ами CDN
  (v0.30+, `cdn.go`)
- **Mesh (N-way bridge)** — группа пользователей, чьи personal
  subnets взаимно видны (v0.22+)
- **Per-user headscale control plane** — compliance tier для SOX /
  multi-tenant SaaS / географической изоляции (v0.23+, opt-in)
- **Self-update orchestrator** — кнопка `Apply update` на
  `/admin/update` пересобирает + recreate с auto-rollback (v0.29+)
- **PostgreSQL backend** — opt-in через `go build -tags postgres`,
  та же схема и миграции, `db.BackendOf` диспетчер
  (v0.31+ foundation, v0.32.x+ live cutover)
- **Tailscale in-image** — опциональный `tailscaled` внутри контейнера
  skygate для tailnet-only развертываний (off by default с v0.32.15)
- **Telegram egress relay selector** — `/admin/telegram` позволяет
  админу выбрать, какая активная exit-node запускает канонический
  Telegram CIDR-list (v0.33.1.8)
- **In-process system tests** — `/admin/system_tests` запускает
  22+ тестов, покрывающих network, db, headscale, exit_rules, disk,
  replication, backup, integrations; результаты сохраняются в
  `system_tests_runs`
- **Headplane integration** — опциональный визуальный ACL-редактор,
  версия пинится, opt-in
- **Health + readyz probes** — `/healthz` и `/readyz` для мониторинга
  (R1 / R2 в verify-post каталоге)

## Быстрый старт (Linux + headscale на том же хосте)

Самый быстрый путь: headscale и Skygate в одном docker compose
проекте (или два контейнера в одной сети `headscale_default`).

```bash
# 1. Получить API-ключ headscale (на хосте с headscale)
docker exec headscale headscale apikeys create --expiration 365d
# или: headscale apikeys create --expiration 365d

# 2. Сгенерировать JWT-секрет
openssl rand -hex 32

# 3. Клонировать и сконфигурировать
git clone https://github.com/BarsSky/skygate
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

Дефолтный админ: `admin` (рекомендуется переименовать при первом
логине) + пароль из `SKYGATE_ADMIN_PASS`.

Полная кросс-платформенная установка (Windows, восстановление
из бэкапа, DERP, headplane sidecar, PostgreSQL backend): см.
[docs/deploy.md](docs/deploy.md).

## Tailscale: OFF по умолчанию (v0.32.15+)

Контейнер skygate может опционально запускать `tailscaled` и
входить в tailnet (позволяет обращаться к
`https://skygate.example.com` с Tailscale-клиента без открытия
порта 443 на VM). В v0.29.x это было по умолчанию, но теперь
**выключено по умолчанию** из-за двух инцидентов v0.32.8 / v0.32.11:

- `secrets/ts_authkey` оказался 0-байтным файлом (Tailscale preauth
  не был provisioned). `tailscale up --authkey=` ждёт stdin вечно —
  entrypoint зависает, контейнер никогда не становится healthy.
- Переменная `TS_AUTHKEY_FILE` в `docker-compose.yml` была
  литеральной строкой, которую `.env` overrides не заменяли.
  Фикс v0.33.1.16 убрал хардкод из `environment:` и добавил
  кнопку «Restart skygate» на `/admin/tailscale`, которая
  атомарно пишет новое effective-значение в `.env`.

Если ваша VM уже за Nginx Proxy Manager (NPM) и у вас есть
публичная DNS-запись (например, `skygate.example.com`), **вам
вообще не нужен in-container Tailscale** — рекомендуемая
конфигурация это `NPM → 127.0.0.1:8080`, без Tailscale в
контейнере skygate. Tailscale полезен только для tailnet-only
развертываний, где нужна нулевая публичная attack surface.

**Включить Tailscale обратно (3 ручных шага)**:

1. Provision реальный preauth-ключ на хосте headscale:
   ```bash
   docker exec headscale headscale preauthkeys create \
     --user admin --reusable --expiration 24h
   ```
2. Записать его в `secrets/ts_authkey` на хосте Skygate
   (файл bind-mount'ится в контейнер по пути
   `/run/secrets/ts_authkey`).
3. В `docker-compose.yml` снять гейт с блока `secrets:` и
   установить `SKYGATE_TS_AUTHKEY_FILE=/run/secrets/ts_authkey` в
   `environment:` сервиса skygate. Затем
   `docker compose up -d --force-recreate skygate`.

## Удалённый headscale

Skygate ходит в headscale по HTTP. `HEADSCALE_URL` может указывать
на **любой** достижимый headscale — та же LAN, только Tailscale,
за reverse-proxy и т.д. Дефолт `http://headscale:50444` работает
только когда оба контейнера в одной docker-сети.

```bash
# Skygate нативно (не в Docker), headscale там же:
HEADSCALE_URL=http://localhost:50444

# Headscale на другом хосте в LAN (RFC 5737 example IP):
HEADSCALE_URL=http://192.0.2.1:50444

# Headscale доступен только через Tailscale (без публичного IP):
HEADSCALE_URL=http://100.64.0.1:50444

# Headscale за HTTPS reverse-proxy:
HEADSCALE_URL=https://headscale.example.com
```

**Важно:** host:port должен быть достижим оттуда, где работает
Skygate. Если Skygate в Docker на хосте A, а headscale на хосте
B — используйте LAN-IP или Tailscale-IP хоста B; `localhost`
не сработает.

API-ключ (`HEADSCALE_API_KEY`) глобальный для headscale и даёт
полный admin-доступ. Создайте его на хосте headscale, вставьте
в `.env` Skygate, не делитесь им.

## Reverse proxy + HTTPS

Skygate — только HTTP. Всегда ставьте его за TLS-терминатор.

- **Nginx Proxy Manager**: proxy host `skygate.example.com` →
  `http://<skygate-host>:8080`, LE-сертификат, force SSL.
- **Caddy** (одной строкой):
  ```
  skygate.example.com {
      reverse_proxy <skygate-host>:8080
  }
  ```
- **nginx** (вручную): <https://docs.nginx.com/nginx/admin-guide/web-server/reverse-proxy/>

Куки HttpOnly + SameSite=Lax — работают за любым стандартным
reverse-proxy. Убедитесь, что прокси не срезает `Set-Cookie`.
См. [docs/https-setup.md](docs/https-setup.md) для Caddy +
Let's Encrypt walkthrough.

## Безопасность

**Где живут секреты**

| Секрет | Файл | Права |
|---|---|---|
| `HEADSCALE_API_KEY` | `.env` на хосте Skygate | `chmod 600` (root или admin) |
| `SKYGATE_JWT_SECRET` | `.env` на хосте Skygate | `chmod 600` |
| `SKYGATE_ADMIN_PASS` | `.env` на хосте Skygate | `chmod 600`; используется только при первом старте |
| `skygate.db` / PG (bcrypt-хеши + audit log) | volume или БД | `chmod 700` / DB-level access |

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
- `SKYGATE_JWT_SECRET`: перегенерировать, вставить в `.env`,
  перезапустить. **Внимание:** это разлогинит всех пользователей
  и отзовёт все персональные API-токены.
- `SKYGATE_ADMIN_PASS`: удалить пользователя из БД, задать новый
  `SKYGATE_ADMIN_PASS`, перезапустить.

**Что НЕ отображается в UI**

`HEADSCALE_API_KEY` **никогда не рендерится в HTML**. Чтобы
использовать ключ для Headplane — скопируйте его вручную из
`.env` на хосте Skygate. Это сделано специально: любой
отрисованный секрет может утечь через скриншоты, расширения
браузера или XSS.

**Другое hardening**

- Пароль админа: bcrypt cost 12 (специально медленно)
- Сессии: JWT HS256, TTL 24h, HttpOnly + SameSite=Lax
- Куки за HTTPS: reverse-proxy не должен срезать `Secure`
  (в nginx `proxy_cookie_flags Secure httponly`)
- Skygate на `127.0.0.1`, наружу только через reverse-proxy:
  в `docker-compose.yml` поставьте
  `ports: ["127.0.0.1:8080:8080"]`
- Per-IP и per-username rate limits на `/login` и `/api`

## Разработка

```bash
# Быстрая итерация
make build              # GOTOOLCHAIN=local go build -o ./skygate ./cmd/skygate
make run                # build + ./skygate
make go-test            # go test ./...
make smoke              # HTTP smoke (118+118 = 236 ассертов, двуязычный)
make check-nodes        # проверяет что exit-nodes анонсируют 0.0.0.0/0 + ::/0
make audit-routes       # статический аудит main.go vs handlers
make test               # go-test + audit-routes + smoke + check-nodes (всё вместе)

# PostgreSQL backend (opt-in)
go build -tags postgres ./cmd/skygate
# Запустить 4 PG-специфичных verification теста:
docker run -d --name skygate-pgtest -e POSTGRES_USER=skygate \
  -e POSTGRES_PASSWORD=skygate_dev -e POSTGRES_DB=skygate \
  -p 5432:5432 postgres:16
export SKYGATE_TEST_PG_DSN='postgres://skygate:skygate_dev@127.0.0.1:5432/skygate?sslmode=disable'
go test -tags postgres -count=1 -v -run "TestPG" ./internal/db/
```

Шаблоны лежат в `internal/handlers/templates/`, в бинарник
встраиваются через `//go:embed`. Поменяли — пересобрали —
перезапустили.

Для AI-ассистентов: сначала прочитайте [AGENTS.md](AGENTS.md) —
там полная карта файлов, schema-gotchas, каталог гарантий
(B1–B66 build, R1–R27 runtime) и правила работы на VM vs Windows.

## Куда смотреть

| Хочется… | Идти в |
|---|---|
| Карту компонентов, поток данных | [docs/architecture.md](docs/architecture.md) |
| Все таблицы и колонки БД | [docs/db-schema.md](docs/db-schema.md) |
| Каждый HTTP-эндпоинт + curl | [docs/api.md](docs/api.md) |
| Deploy / backup / restore / DERP / HTTPS | [docs/deploy.md](docs/deploy.md), [docs/disaster-recovery.md](docs/disaster-recovery.md) |
| Настройка Telegram-бота + команды | [docs/TELEGRAM.md](docs/TELEGRAM.md) |
| История изменений по версиям | [CHANGELOG.md](CHANGELOG.md), [RELEASE-NOTES.md](RELEASE-NOTES.md) |
| Карта файлов, gotchas, AI-хинты, каталог гарантий | [AGENTS.md](AGENTS.md) |
| Скрипты первоначальной настройки клиента | [docs/scripts/skygate_exit_node_setup.sh](docs/scripts/skygate_exit_node_setup.sh) |
| Backlog (back-burner items) | [docs/BACKLOG.md](docs/BACKLOG.md) |

## Статус (live)

- **CI:** зелёный на каждом push в `main` и каждом PR (см. бейдж —
  `go vet + go test -race + go build + audit_routes.py` на
  `ubuntu-24.04`)
- **Verify-pre:** 66/66 PASS (`bash scripts/verify_pre_deploy.sh`)
- **Latest release:** см. [Releases](https://github.com/BarsSky/skygate/releases)
- **Карта исходников:** см. [AGENTS.md](AGENTS.md) — поддерживается
  в актуальном состоянии по декомпозиции `internal/feature/*`
- **In-process тесты:** `/admin/system_tests` запускает 22+ тестов;
  `exit_rules.preferred_mismatch` (добавлен в v0.33.1.17) — это
  канонический «не сконфигурил ли оператор что-то не так?» чек

## Roadmap

### Сделано (highlights с v0.6.0)

- ✅ Per-user subnets (`10.0.<uid>.0/24` per user как логический
  ACL-namespace; subnet router анонсирует LAN пользователя)
- ✅ Per-user preferred exit-node (per-device + per-user pref, со
  strict pinning через флаг `via`)
- ✅ Домен-правила с DNS-автообновлением; CDN-range подстановка
  для Cloudflare / Fastly / Google / Akamai (без anycast-churn)
- ✅ Кросс-проверка exit-rule / preferred exit-node на трёх
  страницах + in-process тест `exit_rules.preferred_mismatch` в
  system_tests
- ✅ Mesh (N-way bridge) между personal subnets пользователей
- ✅ Per-user headscale control plane (compliance tier — SOX,
  multi-tenant SaaS, географическая изоляция)
- ✅ Self-update orchestrator с auto-rollback (`/admin/update`)
- ✅ PostgreSQL backend (opt-in через `-tags postgres`); 27 PG
  миграций + 4 verification теста
- ✅ Tailscale in-image интеграция (opt-in, off по умолчанию
  с v0.32.15)
- ✅ In-process system tests (`/admin/system_tests` — 22+ тестов)
- ✅ /healthz + /readyz probes для мониторинга
- ✅ Авто-детект устройств (ОС + device_type на каждой загрузке
  `/my/devices`)
- ✅ Telegram egress relay selector (per-chat выбор, какая
  активная exit-node запускает Telegram CIDR-list)
- ✅ Headplane integration (опциональный sidecar, версия
  пинится)
- ✅ Per-feature рефактор (handlers разделены на
  `internal/feature/*` по плану refactor-v0.30; AGENTS.md
  отслеживает декомпозицию)
- ✅ Двуязычный EN/RU веб-UI (1 000+ ключей каталога)
- ✅ Персональные API-токены (Bearer auth) с TTL + auto-rotate
- ✅ Self-service смена пароля
- ✅ Rate limits (login + api)
- ✅ Per-exit-node `AcceptRoutes` политика
- ✅ Статический аудит роутов (`scripts/audit_routes.py` в CI)
- ✅ Каталог гарантий: 66 build-time (B1–B66) + 27 runtime
  (R1–R27) чеков, пинятся через `verify_pre_deploy.sh` /
  `verify_post_deploy.sh`

### Не сделано

- ⏳ Фильтр audit-лога по **дате** (сейчас работает только
  `?action=` и `?user=`; `?ip=` добавлен недавно)
- ⏳ Email-уведомления при создании пользователя
- ⏳ QR-код для мобильной регистрации (альтернатива
  `tailscale up --authkey …`)
- ⏳ Переименование устройств через web UI (сейчас только
  со стороны headscale)
- ⏳ Интеграция с Gitea (per-user provisioning API-ключей)
- ⏳ UI-форма для one-click ротации headscale API-ключа
  (процедура документирована выше, но пока не одной кнопкой)
- ⏳ `?device=NAME` query-фильтр на `/admin/exit-rules` (линк из
  per-device бейджа «мёртвых правил» ведёт туда, но handler
  пока не фильтрует — 10-строчный follow-up)
- ⏳ Standalone визуальный ACL-редактор (Headplane остаётся
  рекомендуемым инструментом; `GenerateACL()` всё ещё
  рукописный)

---

## Лицензия

[MIT](LICENSE) — Copyright (c) 2026. Использование, изменение и
распространение разрешены на условиях лицензии MIT. Полный текст
— в файле [LICENSE](LICENSE).

---

## Товарные знаки

*Tailscale* — товарный знак Tailscale Inc. *headscale* — открытый
проект Juan Font. Skygate — независимый self-service портал,
не аффилирован и не одобрен ни одним из этих проектов.
