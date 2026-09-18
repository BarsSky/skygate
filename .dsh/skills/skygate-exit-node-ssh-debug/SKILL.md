---
name: skygate-exit-node-ssh-debug
description: Диагностика exit-node SSH в skygate — от пути к ключу и монтирований контейнера до `tailscale set`, accept-routes и approve-routes. Использовать, когда «пропал SSH к exit node» или accept-routes не применяются.
whenToUse: exit node offline/не синхронизируется, accept-routes не применяются, "Permission denied (publickey)", "no ssh_key_path provided", /admin/exit-nodes отдаёт ошибку.
---

# Диагностика exit-node SSH (skygate)

## Как это устроено (прочитать до правок)

SSH-библиотеки в проекте **нет** — всё через `exec.Command("ssh", …)`:

* `internal/headscale/routes.go:242` — единственный вызов `ssh`;
  вызывающий — `SetAdvertisedRoutes` (`:182`)
* аргументы (`:231-241`): `-i <key> -o BatchMode=yes -o
  StrictHostKeyChecking=accept-new -o ConnectTimeout=10 [-p <port>] <host> <cmd>`
* команда на узле: `tailscale set --advertise-exit-node
  --advertise-routes=<csv>[ --accept-routes=true|false]`
  (`internal/headscale/route_args.go:60-80`); `0.0.0.0/0,::/0` всегда
  добавляются, т.к. `--advertise-routes=` **заменяет** список (`:33-44`)

**Два разных «accept routes»** — их легко перепутать:

1. **approved routes в headscale** — `docker exec … nodes approve-routes`
   (`routes.go:86-90`), питает бейджи ✅/⏳/⚠️ (B182/B184)
2. **клиентский `--accept-routes` на exit-узле** — ставится **только через SSH**
   из tri-state `exit_servers.accept_routes` (−1/0/+1)

Порядок в `internal/feature/exit_rules/sync.go:162-222`: сначала SSH, затем
**безусловно** `ApproveAllRoutesWithList`. Поэтому при падении SSH headscale-часть
«успешна» и UI показывает `approved=N`, а accept-routes не применены.

Источники цели и ключа:
* цель: `exit_servers.ssh_target` → fallback `root@<tailscale_ip>[:ssh_port]`
  (`internal/db/exit_servers.go:292`) → `nodeHostname`
* ключ: `exit_servers.ssh_key_path` → `Config.SSHKeyPath` ← `SKYGATE_EXIT_SSH_KEY`
  (дефолт `/ssh-sync/id_ed25519`, `internal/config/config.go:532`)
* пустой ключ → отказ (`routes.go:199-202`), но **без** `os.Stat` — поэтому
  несуществующий путь деградирует в «Permission denied (publickey)»

## Чек-лист сверху вниз

### 1. Страница вообще открывается?

`/admin/exit-nodes` → `internal/feature/admin/exit_nodes.go:147`
`http.Error(w, listErr.Error(), 500)`. Если видно text/plain — это ошибка
`SELECT`, а не SSH. На SQLite в `exit_servers` **нет** колонок
`ssh_target`/`ssh_key_path`/`accept_routes` → `ListExitServers` падает.
См. скилл `skygate-db-dialect-audit`.

```bash
docker compose exec skygate sh -c 'echo $SKYGATE_DB'
```

### 2. Ключ реально существует внутри контейнера?

```bash
docker compose exec skygate sh -c 'echo "key=$SKYGATE_EXIT_SSH_KEY"; ls -l "$SKYGATE_EXIT_SSH_KEY"'
```

Смонтированы (`docker-compose.yml`): `/ssh-sync:ro` (:260),
`/etc/skygate/ssh_key:ro` (:270), `/host-keys:ro` (:280).
**`/home/admin/.ssh` НЕ смонтирован.** При этом `.env.example:176` и
`docs/deploy.md:45` веками отдают `SKYGATE_EXIT_SSH_KEY=/home/admin/.ssh/skygate_sync`
— это нерабочее значение, зафиксированное операционно в
`RELEASE-NOTES.md:5548-5558`.

```bash
docker compose exec skygate sh -c 'ls -l /ssh-sync/ /etc/skygate/ssh_key/ /host-keys/ 2>&1'
```

### 3. Что записано в БД по этому узлу?

```sql
SELECT hostname, ssh_target, ssh_key_path, ssh_port, accept_routes
  FROM exit_servers ORDER BY hostname;
```

Пустой `ssh_target` → молчаливый fallback на `root@<tailscale_ip>`, а он
требует работающего tailscaled **внутри** контейнера.

### 4. SSH-соединение вручную

```bash
docker compose exec skygate sh -c \
  'ssh -vvv -i "$SKYGATE_EXIT_SSH_KEY" -o BatchMode=yes -o ConnectTimeout=10 root@<target> "tailscale status | head -3"'
```

Типовые ответы:
* `Identity file … not accessible` → шаг 2
* `Permission denied (publickey)` → ключ не тот / не в `authorized_keys` узла
* `REMOTE HOST IDENTIFICATION HAS CHANGED!` → сменился host key, а
  `known_hosts` персистентен; убрать запись или пересоздать контейнер
  (`--force-recreate`)
* `Could not resolve hostname` → `ssh_target` содержит `host:port` целиком;
  код это чинит (`splitSSHTarget`, `routes.go:119-129`), но кастомные скрипты — нет

### 5. Применяется ли accept-routes

```bash
# на самом exit-узле
tailscale debug prefs | grep -i acceptroutes
tailscale status --json | jq '.Self.AllowedIPs'
```

### 6. Sync-путь

Кнопка **«Пере-синхронизировать»** (`POST /admin/exit-nodes/{hostname}/sync`)
→ `SyncAdvertisedRoutesForNode` (`exit_rules/sync.go:124`) →
`syncOneExitNode` (`:162`) → `SetAdvertisedRoutes`. Периодический вариант —
`StaggeredSync()` (`:241`), ошибки **только** `log.Printf` (`:326-330`).

```bash
docker compose logs --tail=200 skygate | grep -Ei 'exit|ssh|approve|advertise'
```

## Известные дефекты (эталон)

| # | Дефект | Место |
|---|---|---|
| 1 | На SQLite нет колонок `ssh_target`/`ssh_key_path`/`accept_routes` | `migrations_sqlite.go:141,142,222` |
| 2 | `.env.example:176` и `docs/deploy.md:45` отдают нерабочий путь ключа | — |
| 3 | `SKYGATE_EXIT_SSH*` (`root@relay-*`) **не читает ни один Go-файл** — мёртвые переменные | `.env:35-37` |
| 4 | `ssh=err=...` уходит в **зелёный** `?ok=`-алерт | `exit_nodes.go:686-690`, `exit_nodes.html:37-39` |
| 5 | `approve-routes` жёстко берёт контейнер `headscale` и бинарь `/ko-app/headscale` | `routes.go:86` |
| 6 | Периодические SSH-ошибки не видны (нет аудита/UI) | `sync.go:326-330` |
| 7 | Авто-cleanup может удалить строку `exit_servers` вместе с её ssh-конфигом | `exit_nodes.go:1016-1050,1088-1117` |
| 8 | Нет ни одного B-чека на путь ключа/SSH-доступность | `scripts/` |

## Разобранный живой случай (VM <VM_HOST>, 2026-09-18)

Симптом оператора: «пропал SSH к exit node». Фактически — пять независимых причин:

1. `SKYGATE_EXIT_SSH_KEY=/ssh-sync/skygate_sync`, но `/ssh-sync`,
   `/etc/skygate/ssh_key` и `/host-keys` внутри контейнера **пусты**.
2. Ключ **есть на хосте**: `/home/skyadmin/.ssh/skygate_sync` (+`.pub`), но
   compose монтирует `${SKYGATE_HOST_REPO_PATH}/.ssh` =
   `/home/skyadmin/skygate/.ssh` и `.../data/ssh-sync` — оба пусты.
   `SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate`, а ключ лежит уровнем выше.
3. `exit_servers.ssh_target` **пуст** у всех узлов → fallback `root@100.64.0.x`.
4. tailscaled в контейнере **выключен** → fallback мёртв; и даже при живом
   tailscaled ACL отвечает `tailnet policy does not permit you to SSH to this node`.
5. Права на хост-ключ `0644` → `WARNING: UNPROTECTED PRIVATE KEY FILE!`.

Плюс отдельно: `accept_routes=0` у всех узлов (флаг не формируется вовсе), и
approve-routes падает с `connecting to /var/run/headscale/headscale.sock:
context deadline exceeded`.

Быстрая проверка одной командой:

```bash
sudo docker exec skygate-skygate-1 sh -c 'ls -l "$SKYGATE_EXIT_SSH_KEY"; tailscale status 2>&1 | head -2'
sudo docker exec skygate-pg-local psql -U admin -d skygate_staging \
  -c "SELECT hostname, COALESCE(ssh_target,'') t, COALESCE(ssh_key_path,'') k, accept_routes FROM exit_servers;"
```

**Важно:** реальные адреса узлов живут в `/home/skyadmin/.ssh/config`
(`karolina <KAROLINA_PUBLIC_IP>:18022`, `emilia <EMILIA_PUBLIC_IP>:22`,
`sharlotta <SHARLOTTA_PUBLIC_IP>:22`) — их и надо положить в `ssh_target`. Fallback
на Tailscale-IP в этой топологии нерабочий by design.

## Безопасность (не забыть при правке)

`ssh_target` приходит из БД и подставляется **позиционным аргументом**:
`sshArgs = append(sshArgs, host, cmd)` (`routes.go:241`). Значение вида
`-oProxyCommand=<команда>` будет разобрано `ssh` как **опция** → выполнение
команд. Валидировать `ssh_target` строгой регуляркой
(`^(?:[\w.-]+@)?[\w.-]+(?::\d{1,5})?$`, IPv6 — отдельной веткой) на входе формы
**и** перед exec.

## Definition of done для фикса

- [ ] pre-flight `os.Stat(keyPath)` + `0600` с понятным сообщением
- [ ] «Проверить SSH» на `/admin/exit-nodes`, статус сохраняется и виден в строке
- [ ] `ssh=err` уходит в `?err=`, а не в `?ok=`
- [ ] канонический путь ключа один во всех файлах
- [ ] B-чек на путь ключа и на SSH-доступность
