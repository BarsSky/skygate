# Skygate — Headscale Management Portal

Tailscale/headscale management portal with exit node rules, split-tunnel routing, device management, and ACL automation.

## Архитектура

```
┌─────────────┐     ┌──────────────┐     ┌──────────────────┐
│  NPM Proxy  │────▶│   Skygate    │────▶│    Headscale     │
│  :443→:8080 │     │  Go 1.21+   │     │  v0.29 (control  │
│  (cache!)   │     │  SQLite      │     │   plane)         │
└─────────────┘     └──────┬───────┘     └────────┬─────────┘
                           │                      │
                    ┌──────▼───────┐     ┌────────▼─────────┐
                    │  Tailnet     │     │  Exit Nodes       │
                    │  100.64.0/10 │     │  karolina         │
                    │              │     │  emilia           │
                    │  Clients:    │     │  sharlotta        │
                    │  skyworker   │     │  --advertise-     │
                    │  skybars     │     │    exit-node      │
                    │  ...         │     │  --advertise-     │
                    └──────────────┘     │    routes         │
                                         └──────────────────┘
```

**Стек:** Go 1.21+, SQLite, Docker, headscale 0.29 API, embedded HTML templates

**Компоненты:**
- `cmd/skygate/main.go` — точка входа, HTTP-маршруты
- `internal/auth/` — JWT-аутентификация
- `internal/config/` — переменные окружения
- `internal/db/` — SQLite-схема + миграции
- `internal/handlers/` — HTTP-обработчики + Go-шаблоны
- `internal/headscale/` — клиент headscale HTTP API
- `internal/middleware/` — auth middleware

## Конфигурация (переменные окружения)

| Переменная | Назначение | Пример |
|---|---|---|
| `SKYGATE_PORT` | Порт Skygate | `8080` |
| `SKYGATE_DB` | Путь к SQLite | `/data/skygate.db` |
| `HEADSCALE_URL` | URL headscale API | `http://headscale:50444` |
| `HEADSCALE_API_KEY` | Ключ headscale API | `hskey-...` |
| `SKYGATE_JWT_SECRET` | Секрет JWT | `1659ec...` |
| `SKYGATE_ADMIN_USER` | Имя админа | `skyadmin` |
| `SKYGATE_ADMIN_PASS` | Пароль админа | `...` |
| `SKYGATE_CONTROL_URL` | Публичный URL headscale | `https://head.skynas.ru` |
| `HEADSCALE_CONTAINER` | Имя Docker-контейнера headscale | `headscale` |
| `SKYGATE_EXIT_SSH_KEY` | Путь к SSH-ключу для автосинка | `/home/skyadmin/.ssh/skygate_sync` |
| `SKYGATE_EXIT_SSH` | SSH-цель для автосинка (все ноды) | `root@100.64.0.2` |
| `SKYGATE_EXIT_SSH_KAROLINA` | SSH на конкретную ноду | `root@karolina.local` |

## Headscale Config

Файл: `/etc/headscale/config.yaml` (в контейнере headscale), хост-путь: `/home/skyadmin/headscale/config/config.yaml`

```yaml
policy:
  mode: database          # обязательно для API-управления ACL
  auto_approve:
    routes: "0.0.0.0/0,::/0"  # автоодобрение advertised-routes
```

## Режимы маршрутизации (Split Tunnel)

### v0.4: Advertised Routes (рекомендуемый)

Exit-узлы анонсируют whitelist-подсети через `--advertise-routes`. Headscale по ACL раздаёт маршруты только тем клиентам, которым разрешён доступ.

| Клиент | Команда | Трафик |
|---|---|---|
| **Windows** | `tailscale up --accept-routes` | whitelist → exit-node, остальное напрямую |
| **Linux** | `tailscale up --accept-routes` | whitelist → exit-node, остальное напрямую |
| **Android/iOS** | `tailscale up --exit-node=X` | весь трафик → exit-node |
| **Резерв (Linux)** | скрипт `.sh` из Skygate UI | статические маршруты |

### Почему НЕ работают статические маршруты на Windows

WireGuardNT использует Windows Filtering Platform (WFP). При `--exit-node` `AllowedIPs = 0.0.0.0/0` — WFP перехватывает весь трафик ДО таблицы маршрутизации. `route add/delete` не может это обойти.

### Настройка exit-node

Выполнить на КАЖДОЙ exit-node (karolina, emilia, sharlotta):

```bash
# Скачать скрипт:
# https://skygate.skynas.ru/admin/exit-rules → "Sync Advertised Routes" → команда

tailscale set \
  --ssh \
  --advertise-exit-node \
  --advertise-routes="91.108.4.0/22,91.108.8.0/22,...,216.58.192.0/19"
```

`--ssh` включает Tailscale SSH (нужен ACL-правило `tag:private → tag:exit-node`).
`--advertise-exit-node` сохраняет возможность полного туннеля для Android.
`--advertise-routes` задаёт whitelist для split-tunnel клиентов.

**Откат:**
```bash
tailscale set --advertise-exit-node --advertise-routes="0.0.0.0/0,::/0"
```

## ACL

Генерируется автоматически из `device_rules` (Skygate DB). Включает:
- Базовое правило `*:*` (весь tailnet + интернет)
- Per-device правила: `{"src": ["100.64.0.x"], "dst": ["target:*"]}`
- SSH-правила: `tag:private + skyadmin → tag:exit-node` (root)
- TagOwners, Groups

**Применение:** автоматическое при добавлении/удалении правил через UI или API.

**API для AI-ассистента:**
- `GET /my/exit-rules/api` — все правила текущего пользователя (JSON)
- `POST /my/exit-rules/api` — массовое создание правил
- `GET /my/exit-rules/help` — документация API

## Администрирование

### Управление правилами
- `/my/exit-rules` — пользовательские правила
- `/admin/exit-rules` — все правила, история ACL, откат, sync-кнопка

### Синхронизация маршрутов
- `GET /admin/exit-rules/sync` — JSON: результат синхронизации
- Кнопка «Синхронизировать» в админке — JS fetch + отображение результата
- Автосинк при каждом изменении правил через UI или API

**Для автосинка через SSH:**
1. На exit-node выполнить скрипт `skygate_exit_node_setup.sh`
2. На agent VM: положить ключ `/home/skyadmin/.ssh/skygate_sync`
3. В `/tmp/skygate.env`: `SKYGATE_EXIT_SSH_KEY=/home/skyadmin/.ssh/skygate_sync`
4. Пересоздать контейнер skygate

## Схема БД (ключевые таблицы)

```sql
-- Правила туннелирования
CREATE TABLE device_rules (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL,
    device_id INTEGER NOT NULL,
    exit_node_id TEXT NOT NULL,    -- имя exit-node (karolina/emilia/sharlotta)
    target_type TEXT NOT NULL,     -- 'ip', 'subnet', 'domain'
    target_value TEXT NOT NULL,    -- 8.8.8.8, 91.108.4.0/22, telegram.org
    action TEXT DEFAULT 'accept',  -- 'accept' или 'deny'
    device_ip TEXT DEFAULT '',     -- Tailscale IP устройства (100.64.0.x)
    enabled INTEGER DEFAULT 1
);

-- Снапшоты ACL (для отката)
CREATE TABLE acl_snapshots (
    version INTEGER PRIMARY KEY,
    config TEXT NOT NULL,
    created_by TEXT,
    applied_success INTEGER DEFAULT 0,
    error_msg TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Лог изменений
CREATE TABLE exit_rule_logs (
    id INTEGER PRIMARY KEY,
    version INTEGER,
    action TEXT,
    detail TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Привязка headscale-нод к пользователям портала
CREATE TABLE node_owner_map (
    node_id INTEGER PRIMARY KEY,
    headscale_user_id INTEGER,
    username TEXT,
    tag TEXT,
    tagged_by_user_id INTEGER
);
```

## Deploy

```bash
# Пересборка после изменения исходников
docker exec skygate rm -f /tmp/skygate_build.sum
docker restart skygate
# Ждать 10-30с (Go build)

# Просмотр логов
docker logs --tail 20 skygate

# Копирование файла в volume (без пересоздания контейнера)
docker run --rm -v /home/skyadmin/skygate:/app -v /tmp:/host alpine cp /host/file.go /app/internal/handlers/target.go
```

## Pitfalls

- **NPM кэширует HTML** — добавляй `?t=` к URL после деплоя шаблонов
- **Build cache** — entrypoint.sh кэширует checksums в `/tmp/skygate_build.sum`; удаляй перед пересборкой
- **Docker cp + права** — после `docker cp` файлы теряют права; используй `alpine` контейнер
- **headscale policy.mode** — должен быть `database`, иначе API ACL не работает
- **Go `$` не спецсимвол** — в Go-строках `$` это литерал, не экранировать
- **Windows WFP** — `route add/delete` бесполезны при `--exit-node`; используй `--accept-routes`
- **`--advertise-routes` заменяет список** — не мерджит; сохраняй нужные маршруты явно
