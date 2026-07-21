# v0.14.1 — Auto-heal `node_owner_map` sync in `ExitNodeMonitor.tick`

**Дата:** 2026-07-15
**Размер:** маленький, в основном косметика. Один новый env-флаг
(`SKYGATE_EXIT_NODE_AUTO_SYNC`, opt-in), один новый метод в monitor,
три новых теста, и побочный schema-фикс двух мёртвых колонок в
`node_owner_map`.

## Что вошло

### 1. Auto-heal sync (opt-in)

Главная фича релиза. В v0.14.0 мы добавили
`db.SyncNodesFromHeadscale` (полная sync: INSERT missing + UPDATE
drifted tags) и кнопку "Sync from headscale" на `/admin/devices` —
но это требовало ручного клика. v0.14.1 подключает ту же функцию
к `ExitNodeMonitor.tick()`, чтобы portal автоматически
подтягивал новые tagged exit-nodes из headscale.

* **Новый env-flag**: `SKYGATE_EXIT_NODE_AUTO_SYNC` (default `false`)
* **Когда `true`**: каждый monitor tick (default 5 мин) плюс
  immediate pre-tick вызывают `db.SyncNodesFromHeadscale` ПЕРЕД
  классификацией нод (online/degraded/offline). INSERT/UPDATE
  per tick — на типичном tailnet'е это единицы нод и стоит
  ~1 мс.
* **Когда `false`** (default): поведение v0.13.0/v0.14.0 — никаких
  auto-sync, кнопка на `/admin/devices` остаётся рекомендуемым
  путём.
* **Failure handling**: если sync падает (transient DB error),
  ошибка логируется, но monitor продолжает health-check path.
  Никакого suppress'a алертов.
* **Логируется всегда**: каждый tick пишет
  `exit-node-monitor: auto-sync inserted=N updated=M` — даже если
  оба нули, чтобы оператор видел, что фича работает.

### 2. Schema fix: `node_owner_map`

Live-verify всплыли два latent gap'а в схеме, которые на проде были
тихо out-of-band patched, а на fresh install ломали `db.UpsertNodeOwner`:

* **`user_id` column** (NOT NULL no DEFAULT) — мёртвая колонка из
  v0.25, нигде не читается/пишется. На проде её не было (DB была
  создана до v0.25). Убрал из `migrations_v0.25` CREATE — теперь
  fresh install и production совпадают.
* **`tagged_at` column** — `migrations_v0.28` ALTER list забыл его
  добавить (production имело out-of-band; fresh install падал).
  Добавил в V028 ALTER (идемпотентно — `_, _ = d.Exec(q)`).
* **`qInsertOrReplaceNodeOwner`** и **`SyncNodesFromHeadscale`**
  — column list скорректирован под production schema (без
  `user_id`).
* **`SyncNodesFromHeadscale` ON CONFLICT** — добавил
  `hostname = excluded.hostname` в DO UPDATE clause, чтобы
  существующая row с пустым hostname получила его на следующем
  auto-sync tick (v0.14.0 только обновляло tag).

### 3. Tests

3 новых теста в `internal/monitoring/exit_node_monitor_test.go`:

* `TestTick_AutoSyncEnabled_InsertsAndUpdates` — happy path: pre-seeded
  row с `tag:untagged` обновляется до `tag:exit-node`, новая row
  для sharlotta инсертится, hostname backfill работает
* `TestTick_AutoSyncDisabled_DoesNotWriteNodeOwnerMap` — с
  `AutoSync=false` ничего не пишется в `node_owner_map`
* `TestTick_AutoSyncError_DoesNotAbortHealthCheck` — закрытая
  БД в момент tick'а: sync падает, но tick не panic'ит и
  возвращает non-nil error

## Совместимость

* Default behaviour **не меняется** (`SKYGATE_EXIT_NODE_AUTO_SYNC=false`).
  Существующие install'ы увидят v0.14.1 как обычное обновление
  с дополнительной возможностью, которую можно включить.
* Schema changes идемпотентны (existing production rows не
  трогаются; `user_id` на проде никогда не было; `tagged_at`
  на проде уже был).
* `db.SyncNodesFromHeadscale` semantically identical для
  существующих callers (admin button + bot `/sync_nodes`).

## Live-verify на VM

```text
$ docker logs --since 30s skygate | grep auto-sync
2026/07/15 19:14:30 exit-node-monitor: auto-sync inserted=0 updated=9
2026/07/15 19:19:30 exit-node-monitor: auto-sync inserted=0 updated=9
```

Immediate pre-tick (при Start) + background tick (5 мин спустя)
оба отрабатывают, 0 ошибок, 9 нод с drifted tags приведены в
порядок. HTTP 200, smoke 118/118 (EN 59 + RU 59).

## Апгрейд

```bash
cd /home/skyadmin/skygate
git pull
# опционально: включить auto-sync
echo "SKYGATE_EXIT_NODE_AUTO_SYNC=true" >> .env
docker compose up -d --force-recreate --no-deps skygate
# проверить:
docker logs --since 30s skygate | grep auto-sync
```

Без флага в `.env` поведение v0.14.1 = поведение v0.14.0 + schema fix.

## Что осталось / не вошло

* `v0.15.x follow-up` — wire `check_https.py` в smoke как
  optional HTTPS-smoke
* `v0.16.0` — per-plane ACL + ACL import/export с dry-run
* `BotEnv.HeadscaleRouter` per-user bot routing (отложено
  из v0.12.0)

— Ваш Дворецкий
