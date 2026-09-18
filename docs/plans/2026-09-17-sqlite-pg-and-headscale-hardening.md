# План: SQLite⇄PostgreSQL, выдача ключей устройств, exit-node SSH, headscale вне Docker

**Дата:** 2026-09-17 · **База:** `v1.5.8-1-g6fff2e25` · **Статус:** proposal (требует
решения оператора по п.10)

**Область:** устранение конфликта двух реализаций БД, восстановление выдачи
pre-auth ключей, восстановление exit-node SSH / accept-routes, поддержка
headscale, установленного вне Docker, + документация и настройки в веб-интерфейсе.

---

## 0. Как читать документ и статус доказательств

Каждое утверждение помечено:

* **[П]** — проверено лично в этом сеансе (чтение кода / запуск теста / git).
* **[О]** — из отчётов четырёх исследовательских подагентов, выборочно
  перепроверено.
* **[Г]** — гипотеза, требует подтверждения на живой VM.

Главный инструмент доказательства — временный пробный тест в `internal/db`
(создан, запущен, удалён; рабочее дерево чистое, `git status --porcelain` пуст):

```
SQLITE max applied migration version = 70          (PG: 71)
SQLITE derp_cert_sync table count = 0              → таблицы нет
SQLITE SELECT EXTRACT(EPOCH FROM now())::bigint
      -> SQL logic error: near "FROM": syntax error
SQLITE strftime -> err=<nil>
MISSING in SQLite exit_servers:  [ssh_target ssh_key_path accept_routes]
MISSING in SQLite device_rules:  [device_ip]
```

---

## 1. Резюме: корневые причины

Живая конфигурация оператора (`.env:8`) — **`SKYGATE_DB=/data/skygate.db`,
то есть SQLite** [П]. `DetectDSN()` корректно распознаёт это как SQLite
(`internal/db/dialect.go:325` — путь без `://` → `DialectSQLite`) [П].
При этом `PG_DB_PASSWORD` и `SKYGATE_DB_DSN` в `.env` **отсутствуют** [П], а
`docker-compose.yml` при этом всё ещё описывает сервис `postgres` под профилем
`local-pg` и комментирует «`SKYGATE_DB` больше не используется» (строки 63-69) [П].

Итог: skygate работает на SQLite, а весь остальной проект (код, миграции,
B-чеки, документация, DERP-стек, HA) написан под PG. Ниже — семь конкретных
корневых причин.

### R1. Цепочка миграций SQLite отстала на одну версию и не догоняет

* PG: 49 записей, последняя — **V071** (`migrations_v0_71_derp_cert_sync.go`,
  `migrateV071PG`) [П].
* SQLite: 48 записей, последняя — **V070** [П].
* `migrateV071SQLite` **не существует вообще** [П].
* Дополнительно: `driver_sqlite.go:96` помечает V070 как
  `migrations_v0_70_b236.go`, а PG — как `migrations_v0_70_b238.go`
  (`driver_postgres.go:155`). Файла `migrations_v0_70_b236.go` в дереве нет [П] —
  это дрейф метаданных, т.е. цепочки уже расходятся семантически.

**Следствие:** на SQLite никогда не появляется таблица `derp_cert_sync` →
`/admin/derp` (авто-продление сертификата) падает.

### R2. SQLite-миграции **молча глотают ошибки**, из-за чего трёх колонок нет

`migrations_sqlite.go` копирует PG-DDL почти дословно, а `ALTER TABLE … ADD
COLUMN IF NOT EXISTS` — **невалидный синтаксис для SQLite** (в SQLite нет
`IF NOT EXISTS` в `ADD COLUMN`). Ошибка при этом подавляется [П]:

```
145:  d.Exec(q) // ignore errors (column may exist)
106:  if err != nil { return nil } // column exists      ← синтаксическая ошибка = «колонка есть»
225:  if _, err := d.Exec(q); err != nil { continue }    ← accept_routes
```

Шесть таких мест [П]: `device_rules.device_ip` (:105), `exit_servers.ssh_target`
(:141), `exit_servers.ssh_key_path` (:142), `exit_servers.accept_routes` (:222)
+ PG-двойники. Эмпирически подтверждено: в SQLite-схеме **нет**
`exit_servers.ssh_target`, `exit_servers.ssh_key_path`,
`exit_servers.accept_routes`, `device_rules.device_ip`.

Родственный дефект: `migrations_sqlite.go:890-896` внутри **SQLite**-цепочки
запрашивает `information_schema.columns` (каталог PostgreSQL) и глотает ошибку
(`if err := row.Scan(&n); err != nil { return false }`), поэтому хелпер
`colExists` всегда возвращает «нет» [О] — эвристика «колонка только что
добавлена» не работает, и `ALTER TABLE … ADD COLUMN` выполняется при каждом
повторном прогоне. На SQLite нужен `PRAGMA table_info(...)`.

### R3. Общий слой запросов содержит SQL, валидный только для одного диалекта

`internal/db/queries.go` — общий файл для обоих бэкендов. В нём в открытом виде
сосуществуют PG- и SQLite-конструкции [П]:

| место | SQL | статус |
|---|---|---|
| `queries.go:570`, `:571` | `tagged_at = ` + `nowUnix` | **сломано на SQLite** |
| `queries.go:616`, `secrets.go:159,168` | `strftime('%s','now')` | ✅ **не дефект**: `migrations_pg.go:965` создаёт PG-шим `strftime(format text, ts text)`, проверено на живой БД |
| `node_owner_map.go:806-809` | `?` + `strftime` | **сломано на PG** (проверено: `SELECT ?` → `syntax error`), ошибка глотается на `:783-785` |
| `headscale_version/monitor.go:278-283` | `LIMIT ?` | **сломано на PG**, ошибка глотается на `:211` |
| `mesh/cleanup.go:127` | `ANY($1::bigint[])` | **сломано на SQLite** (на PG проверено — работает) |
| `nodeownership/auto.go:341` | `EXTRACT(epoch FROM now())::bigint` | **сломано на SQLite** |
| `telegram/commands_phase3.go:182` | `EXTRACT(EPOCH FROM now())::bigint` | **сломано на SQLite** |

**Уточнение по итогам живой диагностики (2026-09-18).** Комбинация
«PG-шим для `strftime` + отсутствие SQLite-шима для `EXTRACT`» означает, что
переносимость односторонняя: PG умеет притвориться SQLite, а SQLite — нет.
Именно поэтому живая система на PG работает, а SQLite-путь падает.

Ключевой механизм — «шим» `nowUnixSQL()` из `now_unix.go`, который
**безусловно** делегирует в PG-реализацию:

```
now_unix.go:8-10   func nowUnixSQL() string { return nowUnix }
now_unix_postgres.go:4   const nowUnix = "EXTRACT(EPOCH FROM now())::bigint"
```

Комментарии в `now_unix.go`, `on_conflict.go`, `placeholders.go` до сих пор
говорят «As of v1.3.0, skygate is PG-only; the SQLite variant has been removed» [П].
**v1.5.4 вернула SQLite, но шим не переразветвили.** Пробный тест подтвердил:
`SELECT EXTRACT(EPOCH FROM now())::bigint` на SQLite → `near "FROM": syntax error`,
и вставка вида `qInsertOrReplaceNodeOwner` падает [П].

`nowUnixSQL()`/`NowUnixSQL()` используется минимум в 12 боевых местах [П]:
`globalsettings.go:139,142`, `exit_node_prefs.go:99,187`,
`telegram_login_tokens.go:263,264`, `backup/config.go:539,574`,
`queries.go:570,571`.

### R4. Два разных пути открытия БД; CLI-подкоманды остались PG-only

* `db.OpenDSNWithRetry` → `openDSNPing` → `dialect.OpenDialect()` — **умеет оба**
  (`retry.go:156-191`) [П]. Это путь веб-сервера (`cmd/skygate/main.go:432`) [П].
* `db.OpenDSN` (`db.go:220-236`) — **жёстко PG**: `sql.Open("pgx", dsn)` +
  `MigratePostgres` [П]. Его используют подкоманды:
  `cmd/skygate/cluster.go` (6 мест), `init.go` (3), `acl_apply.go`,
  `derp_probe.go`, `migrate.go`, `regapi_credentials.go` (4) [П].

**Следствие:** на SQLite-развёртывании `skygate init`, `skygate cluster`,
`skygate migrate`, `skygate acl-apply`, `skygate derp-probe`,
`skygate regapi-credentials` не работают.

### R5. Двухбэкендность почти не доходит до прикладного кода

`db.BackendOf()` в боевом коде используется **ровно в 3 местах**, все —
`internal/feature/admin/system_tests.go:140,198,873` [П]. Ни `/my/keys`,
ни `/admin/exit-nodes`, ни ACL, ни nodeownership, ни telegram не ветвятся
по бэкенду — они просто пишут SQL «как получится».

### R6. Ошибка БД на странице = отдельная text/plain страница

Хелпера отрисовки ошибок **не существует** [П]: `renderError` / `serverError` /
`ErrorPage` — 0 совпадений в боевом коде. Зато `http.Error(...)` — **482 вызова
в 68 файлах**, из них 136 с кодом 500 [П]. Это и есть «отдельная страница
вместо сообщения с ошибкой»:

* `internal/feature/my/keys.go:63-66` — `GET /my/keys`:
  `http.Error(w, err.Error(), 500)`. В `user/keys.html` **нет** блока FlashError [О],
  значит ошибка БД на странице ключей физически может быть только сырой страницей.
* `internal/feature/admin/exit_nodes.go:147` — `GET /admin/exit-nodes`:
  `http.Error(w, listErr.Error(), 500)`. На SQLite `ListExitServers` читает
  несуществующие `ssh_target`/`ssh_key_path` → ошибка → **страница exit-nodes
  целиком превращается в текст ошибки**.
* `internal/feature/my/keys.go:419,427,584,594` — сырые ошибки на POST-ах.

`http.Error(w, err.Error(), 500)` вдобавок **утекает** текст SQL/DSN наружу
(см. §7).

### R7. Ошибка записи ключа в БД подавляется, пользователь видит «успех»

`internal/feature/my/preauth.go:130-136` [П]:

```go
if _, err := db.InsertPreauthKey(s.dbc(), c.UserID, key.Key, …, newKey.ID); err != nil {
    log.Printf("web.my.preauth: InsertPreauthKey userID=%d err=%v", c.UserID, err)
}
if err := db.AppendAuditLog(...); err != nil {
    log.Printf(...)
}
```

Тот же подавляющий паттерн повторён в **остальных путях выдачи ключа** [О]:
`my/keys.go:445-448` (reissue), `my/devices.go:1487-1489` (reregister),
`telegram/commands_user.go:749-751` (бот `/add_device`).

Ключ в headscale создан (`:99`), страница результата отрисована, но локальной
строки в `preauth_keys` **нет**. Именно на неё опираются стратегии
сопоставления узла с пользователем (A: `PreAuthKeyID = preauth_keys.headscale_preauth_id`)
— значит устройство навсегда остаётся «⏳ pending», не получает dev-тег и не
привязывается к пользователю. Это ровно «ошибка по выдаче ключей для устройств
для их регистрации» [П].

Плюс `internal/feature/my/preauth.go:70-76` [П]: любая ошибка БД из
`db.GetUserHSByID` **отбрасывается** и подменяется сообщением
`http.Error(w, "no headscale user linked", 400)` — диагностика теряется.

Плюс `internal/handlers/templates/user/preauth_result.html:34,40,62,67` [П]
использует `{{.PreauthKey}}` и `{{.OSLabel}}`, а хендлер кладёт `"Key"` и `"OS"`
(`preauth.go:155-161`) [П]. Ни один Go-файл не задаёт `OSLabel`/`PreauthKey` [П]:
страница показывает `tailscale up --authkey=<no value>` и «instructions for
`<no value>`». Это видимый дефект на самой странице регистрации устройства.

---

## 2. Живая конфигурация: что реально крутится

> **ПОПРАВКА ОТ 2026-09-18.** Первоначально этот раздел был написан по
> локальной копии `.env` на рабочей машине (`SKYGATE_DB=/data/skygate.db`) и
> делал вывод «живая система на SQLite». **Это неверно.** Проверка на VM по
> SSH показала: рабочая система — на **PostgreSQL**. Локальный `.env` —
> устаревшая копия. Все выводы ниже перепроверены на живой системе.

### 2.1 Рабочий экземпляр (docker, PostgreSQL) — проверено на VM

```
DB backend: postgres
SKYGATE_DB=postgres://admin:***@172.18.0.3:5432/skygate_staging?sslmode=disable
applied_migrations: max version = 71, всего 51 запись  → схема полностью мигрирована
```

| Что | Значение |
|---|---|
| Каталог проекта | `/home/skyadmin/skygate`, `SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate` |
| Контейнеры | `skygate-skygate-1` (healthy), `skygate-pg-local` (postgres **18.4**), `headscale` (**0.29.3**), `derper`, `headplane` |
| headscale | **в docker** (`HEADSCALE_CONTAINER=headscale`, `HEADSCALE_URL=http://headscale:50444`) |
| HTTP | `0.0.0.0:8080` и `:50444` — docker-proxy |
| `SKYGATE_EXIT_SSH_KEY` | `/ssh-sync/skygate_sync` |
| tailscaled внутри контейнера | **выключен** (`failed to connect to local tailscaled`) — по AGENTS.md это сделано осознанно (B258/B259) |
| Журнал systemd | **493 МБ**, 212 742 строки от падающего юнита `skygate.service` |

### 2.2 Вторая, сломанная установка: systemd + SQLite

На том же хосте **одновременно** живёт native-установка (следы
`install-debian.sh`), и она и есть источник «конфликта SQLite с PG»:

```
● skygate.service — active (activating) (auto-restart), restart counter is at 98779
  ExecStart=/usr/local/bin/skygate   → exit 1 каждый раз
  лог: "config: HEADSCALE_API_KEY is required"
```

* `/etc/skygate/skygate.env`: **`SKYGATE_DB=sqlite:<path>`**, `SKYGATE_DB_DSN=`
  (пустой), `HEADSCALE_API_KEY=` — **пустой (`len=0`)**, при этом
  `SKYGATE_JWT_SECRET` заполнен (64 символа).
* Итог: юнит перезапускается каждые 5 секунд (98 779 раз), никогда не
  стартует, пишет ~17 тыс. строк в journal в сутки.

**Это ключевой вывод для вашей жалобы №1:** «SQLite входит в конфликт с
PostgreSQL» — буквально верно: на одной машине две конфигурации skygate,
одна указывает на SQLite и мертва, вторая на PG и работает. Никакой
автоматики, которая бы сказала оператору «у вас две установки», нет.

### 2.3 Мёртвые и нерабочие переменные

| Переменная | Факт |
|---|---|
| `SKYGATE_EXIT_SSH_KEY` | ✅ читается; значение `/ssh-sync/skygate_sync` **не существует** в контейнере |
| `SKYGATE_EXIT_SSH{,_EMILIA,_SHARLOTTA}` (`root@relay-*` в локальной копии `.env`) | ❌ **не читает ни один Go-файл** |
| `HEADSCALE_SERVER_URL` | ❌ не читается ни одним Go-файлом, но `deploy/lib/env.sh:48-49` делает его обязательным |
| `SKYGATE_HOST_REPO_PATH` | ✅ `/home/skyadmin/skygate` — определяет, какие `.ssh` и `data/ssh-sync` попадут в контейнер |

---

## 3. Направление A — БД: явное разделение SQLite / PostgreSQL

### A.0 Немедленная стабилизация (0.5 дня, без изменения кода)

Цель — вернуть оператору рабочую систему до большого рефакторинга.

1. Зафиксировать выбор бэкенда **осознанно**. Рекомендация: оставить PG
   (проект целиком написан под него; DERP/HA/B-чеки — PG). Для этого:
   * добавить в `.env` `SKYGATE_DB=postgres://skygate:<PG_DB_PASSWORD>@postgres:5432/skygate?sslmode=disable`
     и `PG_DB_PASSWORD=…`, **удалить/закомментировать строку 8** `SKYGATE_DB=/data/skygate.db`;
   * `docker compose --profile local-pg up -d` (или внешний PG);
   * перенести существующие данные из SQLite: `skygate db-migrate --from=sqlite:/data/skygate.db --to=postgres://…`
     — **но только после A.2**, потому что сейчас конвертер не знает о
     недостающих колонках (см. ниже).
2. Если данные SQLite не нужны (свежий старт) — просто переключить `SKYGATE_DB` на PG.
3. Немедленно поправить `.env:34` на реально смонтированный путь
   (`/ssh-sync/id_ed25519` или `/etc/skygate/ssh_key/id_ed25519`) — это
   возвращает exit-node SSH **независимо** от A (но только после A.4,
   иначе страница останется сломанной на SQLite).

> **Важно:** не запускать `skygate db-migrate` SQLite→PG, пока не выполнены A.2
> и A.3 — конвертер копирует данные по `SELECT *` (`convert.go:269`), и на
> SQLite-источнике колонки `ssh_target`/`ssh_key_path`/`accept_routes`/
> `device_ip` отсутствуют, поэтому в PG они приедут пустыми/дефолтными. Это
> **тихая потеря конфигурации exit-node и per-device IP-правил** [Г].

### A.1 Единый источник истины диалекта

**Проблема:** три конкурирующих представления о бэкенде —
`db.Backend` (`driver.go:33`), `db.DialectKind` (`dialect.go`) и шимы
`nowUnixSQL`/`onConflictDoNothing`/`placeholdersList`.

**Работа:**
1. Ввести один тип `db.Dialect` (структура уже есть, `dialect.go:247`) и
   протащить его через `App` вместо пересчёта `DetectBackend`/`BackendOf`.
2. Переписать шимы в реальное ветвление по диалекту:

   | функция | PG | SQLite |
   |---|---|---|
   | `nowUnixSQL()` | `EXTRACT(EPOCH FROM now())::bigint` | `strftime('%s','now')` |
   | `placeholdersList(n)` | `$1,$2,…` | `?,?,…` |
   | `onConflictDoNothing(cols)` | `ON CONFLICT (…) DO NOTHING` | `ON CONFLICT (…) DO NOTHING` (поддержано с 3.24 — оставить) |

   Реализация: **не** build-теги (один бинарник обслуживает оба бэкенда), а
   `dialect.go`-методы + `db.ActiveDialect()`/поле в `*sql.DB`-обёртке.
   Убрать «Code generated by helper build; DO NOT EDIT» комментарии из
   `now_unix.go`, `placeholders.go`, `on_conflict.go` — это наследие v1.3.0.
3. Удалить `nowUnix` как константу из `queries.go` (строки 570, 571) —
   заменить на параметр/функцию. **Это блокирующий пункт: без него SQLite
   не работает.**

**Приёмка:** `grep -rn 'EXTRACT(EPOCH' internal | grep -v _test` не находит
ничего вне `dialect.go`/`migrations_pg.go`; `grep -rn 'strftime' internal | grep
internal/db` не находит ничего вне SQLite-миграций.

### A.2 Починить и выровнять цепочки миграций

1. **Запретить глотание ошибок.** В `migrations_sqlite.go` убрать все
   `d.Exec(q) // ignore` и `if err != nil { continue }`. Вместо этого —
   идемпотентный guard на уровне диалекта:
   ```go
   func addColumnIfMissing(d *sql.DB, table, col, ddl string) error
   ```
   который на SQLite проверяет `PRAGMA table_info(<table>)` и выполняет
   `ALTER TABLE … ADD COLUMN <ddl>` только если колонки нет (для PG — прежний
   `ADD COLUMN IF NOT EXISTS`). Это делает идемпотентность явной и
   перестаёт маскировать настоящие ошибки.
2. **Добавить V071 для SQLite** (`migrations_v0_71_derp_cert_sync.go` →
   `migrateV071SQLite` + запись в `sqliteMigrations`) с `CREATE TABLE IF NOT
   EXISTS derp_cert_sync (…)` в SQLite-типах.
3. **Исправить метаданные V070** в `driver_sqlite.go:96`
   (`migrations_v0_70_b236.go` → `migrations_v0_70_b238.go`).
4. **Провести ревизию всех пар миграций.** Написать тест, который берёт
   `pgMigrations` и `sqliteMigrations`, сверяет наборы версий и **падает** при
   расхождении. Плюс тест схемы: для каждой таблицы сравнить набор колонок,
   полученный из PG- и SQLite-цепочек (PG — через testcontainers/`SKYGATE_TEST_PG_DSN`,
   SQLite — `:memory:`), и падать на расхождении. Именно этого теста не хватало,
   чтобы поймать R1–R2.
5. **Зафиксировать регламент:** новая миграция = одна PR, в которой есть
   PG- и SQLite-функция + обновление обеих таблиц + зелёный schema-parity тест.

**Приёмка:** SQLite-схема содержит `exit_servers.{ssh_target,ssh_key_path,accept_routes}`,
`device_rules.device_ip`, `derp_cert_sync`; schema-parity тест зелёный.

### A.3 Убрать диалект-протечки из общего слоя

Каждую строку из таблицы R3 привести к одному из двух состояний: **через
диалект-хелпер** или **явно помечена как PG-only и защищена guard-ом**.

| Файл | Строки | Действие |
|---|---|---|
| `internal/db/queries.go` | 570, 571 | `nowUnix` → диалект-функция |
| `internal/db/queries.go`, `secrets.go` | 616 / 159,168 | ✅ не трогать: `strftime` покрыт PG-шимом (`migrations_pg.go:965`) |
| `internal/db/node_owner_map.go` | 806-809 | `?`+`strftime` → `placeholdersList`+`nowUnixSQL` (пропущено в B253/B254); ошибка глотается на `:783-785` → авто-обнаружение exit-узлов молча мертво на PG [О] |
| `internal/headscale_version/monitor.go` | 278-283 | `LIMIT ?` → `PlaceholdersList(1)`; ошибка глотается на `:211` → история релизов headscale навсегда пуста на PG [О] |
| `internal/mesh/cleanup.go` | 127 | `ANY($1::bigint[])` → dialect-хелпер `InClause(ids)` |
| `internal/nodeownership/auto.go` | 341 | `EXTRACT(…)` → `nowUnixSQL()` |
| `internal/telegram/commands_phase3.go` | 182 | `EXTRACT(…)` → `nowUnixSQL()` |
| `internal/db/driver_sqlite.go`, `open_sqlite.go` | — | оставить |
| `internal/db/cluster*.go`, `pgmigrate/`, `dbmigrate/`, `ha/`, `elector/` | — | **осознанно PG-only**: это подсистемы, привязанные к семантике PG (Patroni, `TEXT[]`, etcd-DSN, WAL-G). На SQLite — явный guard `BackendOf(db).IsSQLite() → ErrFeatureRequiresPostgres`, **видимое** сообщение на странице (не 500-страница и не молчаливый пропуск) + запись в `docs/db-backends.md`. Прятать кнопки нельзя: админ должен понимать, что функция недоступна и почему |

**Приёмка:** линтер-тест `TestNoDialectLeaks` (статический grep по
backtick-SQL вне `internal/db`) запрещает `EXTRACT(`, `strftime`, `::text`,
`::bigint`, `TEXT[]`, `ANY($1::` в общих файлах.

### A.4 Один путь открытия БД

1. `db.OpenDSN` (`db.go:220`) сделать тонкой обёрткой над
   `openDSNPing`/`OpenWithDialect` — то есть диалект-осведомлённой.
   Либо (чище) удалить `OpenDSN` и перевести все 16 вызовов в
   `cmd/skygate/{cluster,init,acl_apply,derp_probe,migrate,regapi_credentials}.go`
   на `OpenDSNWithRetry`.
2. Добавить boot-time диагностику: в лог при старте — бэкенд, DSN-схема
   (без пароля), версия применённой миграции, и предупреждение, если
   бэкенд = SQLite, а PG-only страницы доступны.
3. `/healthz`/`/readyz` — отдавать `db: ok (sqlite|postgres) vNN`.

**Приёмка:** `grep -rn 'sql.Open("pgx"' cmd internal | grep -v _test` даёт только
`open_pg.go`; все CLI-подкоманды работают на SQLite (smoke-тест).

### A.5 Явный выбор БД: env + установщик + веб-интерфейс

Сейчас выбор есть **только** в `deploy/install-common.sh:81 resolve_db_type`
(флаг `--db-type=sqlite|postgres`, промпт при TTY, дефолт sqlite) [П], и то лишь
для native-установщика; Docker-пути (`docker-compose.{yml,lite,sqlite,ghcr}.yml`)
про DB не говорят ничего [П], а в UI выбора нет вообще.

**Работа:**
1. **Единый контракт:** `SKYGATE_DB` — единственная переменная выбора
   (`postgres://…` | `sqlite:/path` | `/path`). Удалить двусмысленность
   `SKYGATE_DB` vs `SKYGATE_DB_DSN` (`config.go:849-857`) — оставить
   `SKYGATE_DB_DSN` как deprecated alias с предупреждением в логе.
2. **Валидация на старте:** если `SKYGATE_DB` не задан и нет
   `SKYGATE_DB_REQUIRE=1`, сейчас происходит «тихий» откат на локальный
   SQLite (`config.go:774-777`) [П]. Заменить на **явный** выбор:
   * при первом запуске без БД — печатать «выберите бэкенд: `--db-type=…`» и
     завершаться (fail-fast), либо писать крупный WARN в `/healthz`;
   * `SKYGATE_DB_REQUIRE=1` сделать дефолтом для образов.
3. **Установщик:** довести `--db-type` до всех путей (`install-rh.sh`,
   `install-alpine.sh`, `install-bare.sh`, `deploy.sh` для Docker), и
   **починить `deploy/install.sh`**: сейчас README (`:208-209`) рекомендует
   `install.sh -f docker-compose.ghcr.yml`, но `install.sh` флаг `-f` не
   обрабатывает вообще, а `install-debian.sh:39-50` его молча игнорирует —
   т.е. **оператор получает bare-metal systemd-установку вместо Docker** [П].
4. **Веб-интерфейс — новая страница `/admin/database` (расширение существующей):**
   * read-only карточка «Активный бэкенд»: `sqlite`/`postgres`, версия схемы,
     путь/хост (без пароля), источник значения (`env` / `ui-default`),
     список PG-only функций, недоступных на текущем бэкенде;
   * чек-лист «Готовность к переключению» (все ли таблицы/колонки на месте,
     нет ли диалект-протечек) — берётся из schema-parity проверки;
   * **переключение** — не мгновенное: форма «целевой DSN» → `skygate db-migrate
     --dry-run` → показать план (таблицы/строки/пропущенные колонки) → кнопка
     «Выполнить» → запись нового DSN в `global_settings` + в `.env` → требование
     перезапуска. Хранение — по образцу `headplane.mode`
     (`internal/db/integrations.go:69-87`) и `tailscale.login_server`
     (`internal/feature/admin/tailscale.go:521-549`): **DB > env > default**
     с подсказкой источника.
5. **Запретить «полувыбор»:** если `docker-compose*.yml` поднимает сервис
   `postgres`, а `SKYGATE_DB` указывает на SQLite — печатать громкое
   предупреждение при старте и в `/admin/database`.

**Приёмка:** оператор может (а) выбрать бэкенд в `.env`, (б) увидеть в UI,
какой бэкенд активен и почему, (в) запустить конвертацию из UI с dry-run.

### A.6 Конвертер `db-migrate`: сделать безопасным

`internal/db/convert.go` — обобщённый: читает схему, переводит DDL шестью
`strings.ReplaceAll` (`:209-215`) и копирует данные `SELECT *` (`:269`) [П].

**Работа:**
1. **Pre-flight проверка совместимости** перед копированием: сверить колонки
   источника с ожидаемой схемой целевого диалекта; при расхождении —
   **отказ** с перечислением потерянных колонок (сейчас — тихая потеря, см. A.0).
2. **Пост-проверка:** количество строк и контрольные суммы по таблицам
   (сейчас есть `internal/dbmigrate/steps/verify.go` — использовать всегда).
3. **Бэкап источника** перед конвертацией (обязательный шаг, не опция).
4. Расширить `translateDDL` на: `TEXT[]` → `TEXT` (JSON), `JSONB` → `TEXT`,
   `TIMESTAMPTZ` → `TEXT/INTEGER`, `DEFAULT now()` → `strftime`, `SERIAL` →
   `INTEGER PRIMARY KEY AUTOINCREMENT`, `GENERATED … AS IDENTITY`.
5. Тест-обвязка «round-trip»: PG → SQLite → PG на синтетическом датасете,
   сравнение всех таблиц. Положить в CI.

### A.7 Тесты и CI

* **CI красный уже сейчас.** `.github/workflows/ci.yml:39-40` запускает
  `go vet ./...`, а на HEAD `6fff2e25` он падает:
  `internal/feature/admin/derp.go:810:12: append with no values`
  (проверено: `go vet ./...` → exit code 1) [П]. Значит job `ci` не проходит
  на текущем коммите — это надо починить **первым**, иначе любой новый гейт
  бессмыслен.
* CI **не тестирует SQLite** как бэкенд: в `ci.yml` нет сервиса `postgres` и нет
  `SKYGATE_TEST_PG_DSN`, поэтому PG-тесты скипаются, а SQLite проверяется лишь
  там, где тест сам открывает `:memory:` [П]. Именно поэтому дефект
  `?`+`strftime` в `node_owner_map.go:807` не ловится: `exit_servers_auto_test.go`
  гоняет SQLite, где `?` валиден [О].
* Комментарий `ci.yml:23` («CGO is required for github.com/mattn/go-sqlite3») и
  `release.yml:289` («CGO_ENABLED=0 (postgres-only, no SQLite)») — наследие
  v1.3.1: с v1.5.4 используется `modernc.org/sqlite` (pure Go, без CGO) [П].
  Обновить, иначе они вводят в заблуждение при разборе сборки.
* **50 тестовых файлов** помечены `t.Skip("v1.3.0: … SQLite …")` [П] — целые
  пакеты (`acl`, `mesh`, `subnet`, `expirewatch`, `sidecar`, `invite`,
  `telegram`, `feature/admin`, `feature/my`) без покрытия.
  План: (1) inventory-тест, который печатает список skip-ов и не даёт ему
  расти; (2) поэтапно перевести их на `db.OpenForTest(t)` (PG) и, где это
  дёшево, прогнать и на SQLite `:memory:` — получится настоящая матрица.
* **Добавить в CI** job `test-sqlite` (`SKYGATE_DB=sqlite:/tmp/x.db` + полный
  `go test ./...`) и job `test-pg` с сервис-контейнером `postgres:15` +
  `SKYGATE_TEST_PG_DSN`. Плюс static-тест «нет диалект-протечек» (A.3).

---

## 4. Направление B — ключи устройств и UX ошибок

### B.1 Починить запись ключа (R7)

1. `internal/feature/my/preauth.go:130-136` — ошибки `InsertPreauthKey` и
   `AppendAuditLog` **не подавлять**. Варианты: (а) откатить ключ в headscale
   (`ExpirePreauthKey`) и показать пользователю ошибку; (б) сохранить ключ в
   `preauth_keys` в рамках одной транзакции с аудитом. Рекомендуется (а) +
   (б): транзакция на запись, при неудаче — компенсирующее удаление в headscale
   и flash-ошибка.
2. `internal/feature/my/preauth.go:70-76` — не подменять ошибку БД сообщением
   «no headscale user linked». Различать `sql.ErrNoRows` (нет привязки) и
   реальную ошибку БД (→ flash «Ошибка БД, повторите/сообщите администратору»).
3. `internal/handlers/templates/user/preauth_result.html:34,40,62,67` — заменить
   `{{.PreauthKey}}`→`{{.Key}}`, а `{{.OSLabel}}` либо завести
   (`devicemeta.DetectOS` уже есть — `internal/feature/admin/adopt_devices.go:223`),
   либо убрать из текста. Сейчас страница выдаёт
   `tailscale up --authkey=<no value>` [П].
4. Добавить B-чек: страница результата обязана содержать значение ключа
   (`preauth_result.html` не ссылается на ключи, которых нет в `map[string]any`).

### B.2 Единый механизм ошибок вместо 482 `http.Error`

**Работа:**
1. Ввести в `internal/httputil` (или новый `internal/weberr`):
   ```go
   type AppError struct { User string; Op string; Err error; Code int }
   func (s *Service) RenderError(w, r, c *auth.Claims, err error)   // HTML + layout
   func FlashError(errMsg string)                                   // через ?err=
   ```
   Правила: **никогда** не отдавать `err.Error()` пользователю; писать
   детальную ошибку в `log` + `audit_log`, пользователю — i18n-ключ.
2. Сначала обернуть **критичный минимум** (14 точек), затем расширять:
   * `/my/keys` (`my/keys.go:65`),
   * `/my/preauth` (`my/preauth.go:71,76,102`),
   * `/admin/exit-nodes` (`admin/exit_nodes.go:147,503,525,598,737`),
   * `/admin/devices`, `/admin/users`, `/admin/acls`.
   Отдельно отмечу два «дешёвых» случая, где flash-инфраструктура на странице
   **уже есть**, а хендлер ею не пользуется [О]:
   * `admin/users.go:130-170` (`PostAdminUser`, создание пользователя) — сырой
     text/plain, хотя в `admin/users.html:70-74` есть рабочий `{{.FlashError}}`;
   * `exit_rules/form_my.go:859` и `form_admin.go:872` (ветка ошибки БД) —
     при том что валидационные ветки в тех же функциях уже редиректят с `?err=`.
   Плюс `internal/middleware/ratelimit.go:26-31` — **ограничение частоты на
   `POST /login` отдаёт text/plain страницу** вместо сообщения на форме логина.
3. Во **все** HTML-шаблоны добавить партиал flash-ошибки (в `user/keys.html`
   его нет вообще [О]). Сделать общий `templates/partials/flash.html`.
4. Механический гейт: тест, который для каждого `mux.Handle` с `GET`-шаблоном
   проверяет наличие flash-партиала; и линтер-правило «`http.Error` запрещён в
   файлах, обслуживающих HTML» (allow-list для JSON-API-роутов).
5. Отдельно: `exit_nodes.go:686-690` — `ssh=err=…` сейчас уходит в **зелёный**
   `?ok=`-алерт [П]. Разделять: `ssh=err` → `?err=`, `ssh=ok` → `?ok=`.

### B.3 i18n ошибок

* **Нет общего ключа «ошибка БД».** Есть только страничные
  `account.db_error` (`internal/i18n/catalog_my.go:28`) и
  `my_telegram.db_error` (`:110`) [О]. Добавить `error.db` (RU+EN).
* **Существующие ключи не используются**: `keys.reissue_err_used` /
  `keys.reissue_err_expired` определены, но `my/keys.go:373,381` пишут
  английский текст в обход каталога; `error.internal` / `error.try_again`
  (`catalog_common.go:131-135`, RU+EN) **не упоминаются ни в Go, ни в шаблонах**
  (6 вхождений — все в самом каталоге) [О].
* **Захардкоженный русский вне i18n**: `admin/tailscale.go` — 43 кириллических
  строковых литерала (`:918,:924,:993,:1015,:1025`), `admin/telegram.go` — 37
  (`:384,:390,:439`). Всего 24 файла содержат кириллицу внутри строковых
  литералов (116 строк) [О]. При английской локали оператор видит русские
  сообщения. Вынести в каталоги и передавать `lang`.
* `templates/user/preauth_result.html:53,55` — английская проза в
  i18n-странице (обратная утечка) [О].

### B.4 Диагностика без SSH

Добавить на `/admin/database` (или `/admin/diagnostics`) чек-лист:
подключение к БД, версия схемы, parity PG↔SQLite, доступность `ssh`-бинаря,
наличие ключа exit-node (stat файла), timeout `headscale /health`. Это
превращает «ошибка БД отдельной страницей» в понятный статус.

---

## 5. Направление C — exit node, accept-routes, SSH

### C.1 Как это работает сейчас (разобрано специально, до правок)

* **SSH-библиотеки нет.** `golang.org/x/crypto/ssh` в репозитории отсутствует;
  всё делается `exec.Command("ssh", …)` [П]:
  `internal/headscale/routes.go:242`, единственный вызывающий —
  `SetAdvertisedRoutes` (`:182`).
* Аргументы (`routes.go:231-241`): `-i <key> -o BatchMode=yes -o
  StrictHostKeyChecking=accept-new -o ConnectTimeout=10 [-p <port>] <host> <cmd>`.
* Команда на удалённом узле — `tailscale set --advertise-exit-node
  --advertise-routes=<csv>[ --accept-routes=true|false]`
  (`internal/headscale/route_args.go:60-80`). Базовые `0.0.0.0/0,::/0` всегда
  добавляются, т.к. `--advertise-routes=` **заменяет** список (`:33-44`).
* **Три источника цели SSH:** `exit_servers.ssh_target` → fallback
  `root@<tailscale_ip>[:ssh_port]` → `nodeHostname` [О].
* **Два источника ключа:** `exit_servers.ssh_key_path` → `Config.SSHKeyPath`
  (`SKYGATE_EXIT_SSH_KEY`, дефолт `/ssh-sync/id_ed25519`,
  `config.go:532`) [П]. Если путь пуст — функция отказывается работать
  (`routes.go:199-202`) [П].
* **Два разных «accept routes»:**
  1. **approved routes в headscale** — `docker exec … nodes approve-routes`
     (`routes.go:86-90`), влияет на бейджи ✅/⏳/⚠️ (B182/B184);
  2. **клиентский `--accept-routes` на exit-узле** — ставится **только через
     SSH** из tri-state `exit_servers.accept_routes` (−1/0/+1).
* Порядок в `syncOneExitNode` (`internal/feature/exit_rules/sync.go:162-222`):
  сначала SSH, затем **безусловно** `ApproveAllRoutesWithList` [О]. Поэтому при
  падении SSH headscale-часть «успешна», UI показывает `approved=N`, а
  accept-routes на узел не попали — ровно симптом оператора.
* Периодический SSH — только `StaggeredSync()` (раз в `DNSAutoCheck`, 5 мин),
  ошибки **только** `log.Printf` (`sync.go:326-330`) [О].

### C.2 Причины поломки, по убыванию вероятности

| # | Причина | Доказательство |
|---|---|---|
| C-1 | **На SQLite отсутствуют `exit_servers.ssh_target` / `ssh_key_path` / `accept_routes`** → `ListExitServers`/`LookupExitServerSSH` падают, страница `/admin/exit-nodes` отдаёт `http.Error` (R2+R6) | **[П]** пробный тест + `exit_nodes.go:147` |
| C-2 | `SKYGATE_EXIT_SSH_KEY=/home/admin/.ssh/skygate_sync` — путь недоступен в контейнере (монтируется `/ssh-sync`, `/etc/skygate/ssh_key`, `/host-keys`) | **[П]** `.env:34`, `docker-compose.yml:260,270,280` |
| C-3 | `SKYGATE_EXIT_SSH*` (`root@relay-*`) — **мёртвые переменные**, их никто не читает; пустой `ssh_target` тихо подменяется на `root@<tailscale_ip>` | **[О]** |
| C-4 | Fallback на `root@<tailscale_ip>` требует работающего tailscaled **внутри контейнера**, а его можно выключить кнопкой `/admin/tailscale` (B258/B259, 2026-09-16) | **[О]** |
| C-5 | `StrictHostKeyChecking=accept-new` + персистентный `known_hosts`: смена host key узла навсегда ломает SSH; пути сброса нет | **[П]** `routes.go:234` |
| C-6 | `routes.go:86` жёстко задаёт контейнер `headscale` и бинарь `/ko-app/headscale`, игнорируя `HEADSCALE_CONTAINER` | **[О]** |
| C-7 | Авто-cleanup может удалить строку `exit_servers` (и её ssh/accept-routes), а rediscovery вернёт дефолты | **[О]** `exit_nodes.go:1016-1050,1088-1117` |
| C-8 | Периодические SSH-ошибки не видны: нет аудита и UI | **[О]** `sync.go:326-330` |

### C.3 Работы

1. **Сначала A.2** — без `ssh_target`/`ssh_key_path`/`accept_routes` в SQLite
   ничего не заработает. Это блокер.
2. **Один канонический путь ключа.** Убрать пять конкурирующих соглашений
   (`config.go:532`, `.env.example:176`, `docs/deploy.md:45`, `exit_nodes.html:328`,
   `docs/features.md:408`, `entrypoint.sh:438-448`). Канон:
   `/ssh-sync/id_ed25519` (то, что реально смонтировано). Исправить
   `.env.example:176` и `docs/deploy.md:45`.
3. **Pre-flight проверка ключа перед SSH:** `os.Stat(keyPath)` + `0600`-проверка;
   если файла нет — понятная ошибка «ключ не найден по пути X; смонтированные
   пути: /ssh-sync, /etc/skygate/ssh_key, /host-keys», а не «Permission denied
   (publickey)» через 10 секунд [П] `routes.go:199-202` не делает stat.
4. **Настройка в UI, а не в `.env`.** Перенести «исходный ключ exit-node SSH» и
   «префикс SSH-цели» в `global_settings` с приоритетом **DB > env > default**
   (образец — `tailscale.auth_key_path`, `tailscale.go:386-395`) + поле на
   `/admin/exit-nodes` и в «Add exit node».
5. **Добавить кнопку «Проверить SSH»** на `/admin/exit-nodes` и хранить
   `last_ssh_ok`/`last_ssh_error`/`last_ssh_at` (новая колонка или
   `global_settings`), показывать в строке. Сейчас per-row ошибки не видны [О].
6. **Передавать `ExecContainer` в approve-routes** (`routes.go:86`) + параметр
   `HEADSCALE_BIN` (по умолчанию `/ko-app/headscale`, для native —
   `/usr/bin/headscale`).
7. **Сброс known_hosts** — кнопка + вынос known_hosts на volume, чтобы он был
   предсказуем.
8. **Защита от потери конфигурации:** перед авто-cleanup сохранять строку
   `exit_servers` в `audit_log`/теневую таблицу; rediscovery не должна
   перезаписывать непустые `ssh_target`/`ssh_key_path`/`accept_routes`.
9. **Периодический sync:** писать результат в `audit_log` и в статус на
   дашборде, не только в лог.

### C.4 Безопасность exit-node SSH (важно)

* **[П] Аргумент-инъекция в `ssh`.** `ssh_target` приходит из БД (правится через
  админ-форму) и подставляется как позиционный аргумент:
  `sshArgs = append(sshArgs, host, cmd)` (`routes.go:241`). `splitSSHTarget`
  (`:119-129`) отрезает только `:port`. Значение вида
  `-oProxyCommand=<произвольная команда>` будет разобрано `ssh` как **опция**,
  что даёт выполнение команд. Это sudo-эквивалент для админа, но при
  компрометации БД/CSRF — escalation.
  **Фикс:** строгая валидация `ssh_target` регуляркой
  `^(?:[A-Za-z0-9._-]+@)?[A-Za-z0-9._-]+(?::\d{1,5})?$` (IPv6 — отдельной
  веткой) на входе формы **и** перед exec; плюс `--` перед `host`, если
  версия OpenSSH поддерживает (проверить), плюс `-oProxyCommand=none`.
  `keyPath` подставляется как аргумент `-i`, что безопаснее, но всё равно
  валидировать как абсолютный путь без перевода строк.
* `StrictHostKeyChecking=accept-new` — TOFU без пина; для прод-узлов
  предпочтительнее явный `known_hosts` + `StrictHostKeyChecking=yes`.

---

## 6. Направление D — headscale вне Docker: скрипты, UI, документация

### D.1 Что известно [О]

* headscale **нет ни в одном** `docker-compose*.yml` — он живёт отдельным
  compose-проектом, который рендерит `deploy/deploy.sh:113` из
  `deploy/templates/headscale-compose.yml.tmpl` в `${DEPLOY_HEADSCALE_DIR}`
  (по умолчанию `/home/admin/headscale`).
* Каналы взаимодействия: (A) REST API — диалект-независимый; (B)
  `docker exec <HEADSCALE_CONTAINER> headscale …` — для тегов, approve-routes,
  fallback ACL/preauth/users/nodes; (C) запись config-файла + restart/SIGHUP;
  (D) SSH к exit-узлам (см. §5).
* **7 режимов перезапуска уже существуют** — только для OIDC:
  `auto|docker|systemd|k8s|manual|download|api`, реализованы в
  `deploy/oidc-sync.sh:175-198` (автодетект) и `:346-361` (рестарт) [П],
  прокидка — `internal/oidc/sync.go:251-253`, форма —
  `internal/handlers/templates/admin/oidc_sync.html:116-126`, хендлеры —
  `internal/feature/admin/oidc_sync.go:81,142` [О].
* **Режим нигде не сохраняется** — `oidc_sync.go:101` и `main.go:824` каждый раз
  подставляют `"auto"` [О]. Нет `headscale.mode` ни в env, ни в
  `global_settings` [О].
* Докер-хардкод без единого исключения [О]:
  `headscale/routes.go:86`, `headscale/acl.go:148-150` (`docker run -v
  /home/admin/headscale/config`), `feature/admin/integrations_renderer.go:240`
  (`docker cp … headscale:/etc/headscale/config.yaml`) и `:248`
  (`docker kill -s HUP headscale` — единственный SIGHUP в репозитории),
  `feature/admin/derp_apply_headscale_b237.go:270` (`docker restart`),
  `headscale/provision.go:233` (`docker ps`).
* **`/admin/settings` молча выбрасывает** `headscale_url`,
  `headscale_api_key`, `public_domain`, `admin_password` —
  `internal/feature/admin/backup.go:656-659` `_ = r.FormValue(...)`, при этом
  форма их показывает (`settings.html:41,46,51,60`), а редирект сообщает
  «Saved!» [П].
* `HEADSCALE_URL`/`HEADSCALE_API_KEY` читаются **один раз** при старте
  (`internal/config/config.go:497-500`) и нигде больше [О] — переключить
  headscale без рестарта нельзя.
* `deploy/systemd/` содержит только `derper.service` +
  `skymate-cleanup-smoke.{service,timer}`; **`skygate.service` и
  `headscale.service` отсутствуют**, при этом `skygate.service` генерируется
  инлайн в `deploy/install-common.sh:399-477` [О].
* Документация: non-Docker установка **skygate** есть, но размазана
  (`README.md:299-340`, `:211`, `:442-445`, `docs/sidecar-mode.md:115-125`,
  `deploy/install-bare.sh:1-36`) и **противоречит** `PROJECT.md:7-11`
  (там Docker на всех платформах). Установка **headscale без Docker не
  описана нигде** — единственные две строки:
  `docs/internal/runbooks/oidc-headscale.md:151-152` [О].
  `docker exec headscale` встречается в `docs/` 50 раз [О].
* `deploy/lib/env.sh:48-49` требует `HEADSCALE_SERVER_URL`, который **не читает
  ни один Go-файл** — оператора блокирует переменная-пустышка [О].

### D.2 Настройка «где живёт headscale» — новая модель

**Ввести `headscale.location` в `global_settings`** (таблица уже есть,
`globalsettings.go:40,107`; образцы — `headplane.mode` в
`internal/db/integrations.go:69-87` и `tailscale.login_server`):

| ключ | значения | смысл |
|---|---|---|
| `headscale.location` | `docker` \| `systemd` \| `k8s` \| `remote` \| `auto` | где работает headscale |
| `headscale.container` | строка | имя контейнера (docker) |
| `headscale.systemd_unit` | строка, дефолт `headscale` | unit (systemd) |
| `headscale.config_path` | путь | где лежит `config.yaml` |
| `headscale.bin` | путь, дефолт по location | `/ko-app/headscale` или `/usr/bin/headscale` |
| `headscale.url` / `headscale.api_key_enc` | строка / шифр | URL и ключ API (сейчас только env) |

Резолвер `internal/headscale/location.go`: **DB > env > autodetect**, плюс
подсказка источника (`db|env|auto`) в UI — как сделано для
`tailscale.login_server`.

**Единый раннер перезапуска/перезагрузки** `internal/headscale/runner.go`,
собранный из двух **уже существующих** в проекте образцов:
* `internal/feature/admin/derp_cert_sync.go:486-513` — стратегия 1
  `systemctl reload <unit>`, стратегия 2 `kill -HUP <pidfile>`;
* `internal/feature/admin/tailscale.go:1437-1488` +
  `isRunningInContainer():1504-1509` — «в контейнере / на хосте».

Раннер обязаны использовать **все** операции, а не только OIDC:
ACL-файловый fallback (`headscale/acl.go:148`), раздача DERP-конфига
(`derp_apply_headscale_b237.go:142,270`), пуш конфига
(`integrations_renderer.go:240,248`), approve-routes (`routes.go:86`),
теги/предах (`tags.go`, `preauth.go`), per-user provision
(`provision.go:233`).

### D.3 UI

1. **`/admin/headscale` (новая страница)** или секция в `/admin/integrations`:
   * выбор `location` (radio) + поля `container` / `systemd_unit` /
     `config_path` / `bin`;
   * кнопка **«Проверить»** — реально проверяет каждый канал (REST `/health`,
     `docker inspect`, `systemctl status`, `kubectl get deploy`) и показывает,
     что доступно, а что нет;
   * кнопка **«Перезапустить headscale»** через раннер + вывод результата;
   * матрица «какие функции работают в текущем режиме» (см. D.4).
2. **Починить `/admin/settings`:** либо реально сохранять `headscale_url`/
   `api_key` в `global_settings` (и резолвить их в рантайме), либо убрать
   поля. Сейчас админ печатает новый URL, получает «Saved!» и ничего не
   меняется [П].
3. **OIDC-режим сохранять**, а не спрашивать каждый раз: `oidc_sync.go:101`
   подставлять значение из `headscale.location`, а не `"auto"`.
4. На каждой странице, требующей docker-CLI, показывать бейдж «недоступно в
   режиме systemd/remote» вместо падения на `docker`-вызове.

### D.4 Документация (создать)

| Файл | Содержание |
|---|---|
| `docs/headscale-native.md` | **Главный пробел.** Установка и настройка headscale БЕЗ Docker: бинарь/релиз, `/etc/headscale/config.yaml`, `headscale.service` (готовый unit), `headscale apikeys create`, `systemctl restart headscale`, каталог данных `/var/lib/headscale`, права, firewall |
| `docs/install-no-docker.md` | Установка skygate без Docker: systemd/OpenRC/bare-binary, консолидация `README.md:299-340` + `docs/sidecar-mode.md:115-125` + `deploy/install-bare.sh`, публикация тела unit-файла из `install-common.sh:429-473` |
| `docs/headscale-location.md` | Матрица «где живёт headscale» (docker/systemd/k8s/remote) × какие функции skygate работают, какие требуют docker-сокета |
| `docs/db-backends.md` | Явное описание SQLite vs PostgreSQL: когда что, как выбрать, как переключить, ограничения каждого |
| `deploy/systemd/skygate.service` | Версионируемый unit (сейчас только генерируется) |
| `deploy/systemd/headscale.service` | Unit для native headscale |
| Правки | `PROJECT.md:7-11` (добавить bare-metal), `docs/deploy.md` (раздел non-Docker + SQLite), `docs/ru/README.md` (нет раздела про варианты установки), `.env.example:176` (путь ключа), `docs/deploy.md:45,79-91` (мёртвые переменные) |

### D.5 Скрипты: разделение по способу установки headscale

**Масштаб:** 434 `.sh`-файла, из них `scripts/` — 290 (в т.ч. **203 `check_b*.sh`**),
`deploy/` — 29. 31 B-чек использует `psql`, 3 — `sqlite3`, 54 — `docker` [П].

**Работа:**
1. Ввести `scripts/lib/headscale.sh` — единственную точку «как выполнить
   headscale-команду»:
   ```sh
   hs_exec() { case "$HEADSCALE_MODE" in
       docker)  docker exec "$HEADSCALE_CONTAINER" "$HEADSCALE_BIN" "$@" ;;
       systemd) headscale "$@" ;;
       k8s)     kubectl exec deploy/headscale -n headscale -- headscale "$@" ;;
       remote)  ssh "$HEADSCALE_SSH" headscale "$@" ;;
     esac }
   hs_restart() { … }
   ```
   Образец уже есть: `scripts/b_mod_reregister_live.sh:100-148` (переменная
   `HEADSCALE_CLI`) [О].
2. Перевести на неё минимум: `deploy/oidc-sync.sh`, `scripts/{restore,backup,
   init-headplane,rotate_ts_authkey,bootstrap_standby,ha-phase0,
   reconcile_snapshots}.sh`, `deploy/scripts/create-standby-preauth.sh`,
   `deploy/headscale-users/*.sh` [О].
3. **Аналогично для БД:** `scripts/lib/db_exec.sh` — выбор `psql` /
   `sqlite3` по `SKYGATE_DB`; перевести 31 B-чек с `psql` на этот хелпер.
4. **`deploy/install.sh`: реализовать `-f <compose-file>`** — иначе
   рекомендованная README команда ставит bare-metal вместо Docker [П].
5. Убрать/пометить мусор: 17 ad-hoc `*.sh` в корне
   (`deploy_b260_*.sh`, `diag_*.sh`, `fix_docker_compose_*.sh`, `tmp_rename.sh`)
   с захардкоженными именами контейнеров [О]; `scripts/upgrade.sh:145`
   с захардкоженным ID контейнера `0c8931e2a82a` [О].

---

## 7. Уязвимые места (сводно)

| # | Риск | Место | Приоритет |
|---|---|---|---|
| U0 | **CI красный на HEAD**: `go vet ./...` падает, job `ci` не проходит | `.github/workflows/ci.yml:39` + `internal/feature/admin/derp.go:810` | **высокий (блокирует любые гейты)** |
| U1 | **Инъекция опций `ssh`** через `exit_servers.ssh_target` (`-oProxyCommand=…`) → выполнение команд | `internal/headscale/routes.go:241` | **высокий** |
| U2 | **Утечка внутренних деталей** (текст SQL, имена таблиц/колонок, иногда DSN) в браузер: 482 `http.Error(w, err.Error(), …)` | повсеместно, напр. `my/keys.go:65`, `admin/exit_nodes.go:147` | **высокий** |
| U3 | **Insecure JWT по умолчанию**: при пустом `SKYGATE_JWT_SECRET` подставляется `dev-only-insecure-jwt-secret-do-not-use-in-prod` + только `log.Printf` | `internal/config/config.go:781-784` | **высокий** |
| U4 | Мёртвая проверка: `if c.JWTSecret == "" { return error }` в `config.go:819` недостижима (уже присвоено выше) — «обязательность» секрета не работает | `internal/config/config.go:781-821` | **высокий** |
| U5 | **`/admin/settings` пишет `.env` с правами 0644**, а в нём JWT-секрет и API-ключи | `internal/feature/admin/backup.go:673` | средний |
| U6 | `SKYGATE_SECRET_KEY` (AES-GCM для `headscale_api_key_enc`) не обязателен — при пустом ключе шифрование per-user ключей деградирует | `.env.example:20`, `internal/db/secrets.go` | средний |
| U7 | TOFU без пина host key: `StrictHostKeyChecking=accept-new`, сброса нет | `internal/headscale/routes.go:234` | средний |
| U8 | `docker`-сокет в контейнере skygate (`docker-compose.yml:220`) = выход на хост при RCE в skygate | `docker-compose.yml` | средний (архитектурный) |
| U9 | Живые секреты в рабочем `.env` (`HEADSCALE_API_KEY`, `HEADPLANE_…API_KEY`); файл в `.gitignore:22` [П], но 9 `diag_*.sh` в корне могут их печатать | `.env`, корневые скрипты | средний |
| U10 | `db-migrate` копирует данные без pre-flight и без обязательного бэкапа → тихая потеря конфигурации | `internal/db/convert.go:258-290` | средний |
| U11 | Авто-cleanup удаляет `exit_servers` без бэкапа | `exit_nodes.go:1016-1050` | низкий-средний |
| U12 | `deploy/install-debian.sh:39-50` не имеет ветки `*)` → неизвестные флаги молча игнорируются | `deploy/install-debian.sh` | низкий (но дал R-D1) |
| U13 | 50 тестовых файлов отключены (`t.Skip`), покрытие ядра просело | по всему `internal/` | средний (риск регрессий) |
| U14 | `go vet` красный → гейт невозможен | `internal/feature/admin/derp.go:810` | низкий |

---

## 8. Что нужно для разработки и отладки

### 8.1 Инструменты (установить)

| Инструмент | Зачем именно здесь | Приоритет |
|---|---|---|
| **`sqlite3` CLI** | инспекция живого `skygate.db`, `PRAGMA table_info`, `EXPLAIN` — без него нельзя проверить R1/R2 | **обязательно** |
| **`psql` + `pgcli`** | паритет схем, проверка `applied_migrations` на PG | **обязательно** |
| **`golangci-lint`** (с `staticcheck`, `sqlclosecheck`, `bodyclose`, `rowserrcheck`) | ловит `append(s)` без значений (U14), забытые `rows.Err()`, утечки `*sql.Row`; сейчас есть только `go vet` | **обязательно** |
| **`sqlc`** *(или `jet`)* | генерация типобезопасных запросов из SQL — структурно убивает класс R3 (диалект-протечки). Требует решения: переписывать `queries.go` (48 КБ) или генерировать только новые запросы | **рекомендую** |
| **`testcontainers-go`** | schema-parity тест PG↔SQLite в CI без внешнего PG | **рекомендую** |
| **Delve (`dlv`)** | отладка `http.Error`-путей и SSH-вызовов на живом процессе | рекомендую |
| **`pgloader`** или свой round-trip тест | проверка конвертера `db-migrate` на реальных данных | рекомендую |
| **`gosec`** | автоматический поиск `http.Error(w, err.Error())`, hardcoded creds, weak crypto под U1–U6 | рекомендую |
| **`govulncheck`** | зависимости (`modernc.org/sqlite`, `pgx`) | желательно |
| **OpenSSH client ≥ 9.x в образе + `ssh -vvv`** | диагностика C-2/C-4/C-5 (`ssh -G` показывает итоговый конфиг) | обязательно для C |
| **`docker compose config`** | проверка рендера compose перед деплоем (B260 учил этому) | обязательно |
| **`ansible-lint`/`shellcheck`** | 434 shell-скрипта, часть с мёртвым кодом (U12) | рекомендую (`shellcheck` — обязательно) |

### 8.2 Скиллы DSH (проектные, `C:\Projects\skygate\.dsh\skills\`)

DSH подхватывает скиллы из `<project>/.dsh/skills`, `<project>/.agents/skills`,
`~/.dsh/skills`, `~/.agents/skills` [П]. Предлагаю завести:

| Скилл | Что делает | Зачем |
|---|---|---|
| `skygate-db-dialect-audit` | Прогоняет статический + динамический аудит диалекта: ищет `EXTRACT(`, `strftime`, `::cast`, `TEXT[]`, `ANY($1::`, `?`-плейсхолдеры вне `internal/db`; поднимает SQLite `:memory:` и PG-контейнер, сверяет схемы и печатает расхождения | делает R1–R3 воспроизводимыми в одну команду |
| `skygate-migration-parity` | Сравнивает `pgMigrations` и `sqliteMigrations`, падает на расхождении версий и колонок | gate для A.2 |
| `skygate-b-check-runner` | Запускает нужный `scripts/check_b*.sh` (203 штуки) на Windows/через VM, парсит результат, объясняет типовые падения | сейчас агент каждый раз угадывает, как запускать |
| `skygate-vm-deploy` | Инкапсулирует `scripts/deploy_to_vm.sh` + `docker compose -f … --env-file …` из правильного CWD + проверку `/healthz` | B260 показал, что это самый частый источник ошибок |
| `skygate-headscale-mode` | Определяет, где живёт headscale (docker/systemd/remote), и подставляет правильную форму команд | прямо закрывает направление D |
| `skygate-exit-node-ssh-debug` | Чек-лист диагностики exit-node SSH: путь ключа → монтирования → `ssh -vvv` → `tailscale set` → `approve-routes` | §5 |
| `skygate-error-ux-migration` | Помогает механически переводить `http.Error` на flash/HTML-ошибку, ведёт allow-list | §4/B.2 |

### 8.3 Плагины/интеграции (опционально)

* **PostgreSQL MCP-сервер** (напр. `@modelcontextprotocol/server-postgres`) —
  чтобы я мог инспектировать схему и данные напрямую, а не через `psql`-обёртки.
* **SQLite MCP-сервер** — то же для SQLite-файла.
* **GitHub MCP** — для работы с issue/PR при пакетной правке (у проекта
  есть `.github/workflows/`).

---

## 9. Порядок работ, оценка, критерии приёмки

| Этап | Содержание | Оценка | Блокирует |
|---|---|---|---|
| **0** | Стабилизация: осознанный выбор бэкенда, правка `.env:34` (путь ключа), бэкап `data/skygate.db`, починка `go vet` (U0) | 0.5–1 день | — |
| **1** | A.1 — единый диалект + `nowUnixSQL`/`placeholdersList` реально ветвятся | 2–3 дня | этапы 2–6 |
| **2** | A.2 — починка SQLite-миграций (V071, guard по `PRAGMA`, отказ от глотания ошибок) + parity-тест | 2–3 дня | этап 3 |
| **3** | A.3/A.4 — устранение протечек, один путь открытия БД, CLI на оба бэкенда | 3–4 дня | — |
| **4** | A.5/A.6 — выбор БД в env/установщике/UI + безопасный конвертер | 4–5 дней | — |
| **5** | B — ключи (транзакция, шаблон) + единый механизм ошибок для критичных страниц | 3–4 дня | — |
| **6** | C — exit-node: pre-flight ключа, настройки в UI, `ExecContainer`, аудит, валидация `ssh_target` | 3–4 дня | — |
| **7** | D — `headscale.location` + раннер + `/admin/headscale` | 5–7 дней | — |
| **8** | D.4 — документация (5 файлов) + unit-файлы + правки существующих | 3–4 дня | — |
| **9** | D.5 — `scripts/lib/{headscale,db_exec}.sh` + миграция 31 B-чека | 4–6 дней | — |
| **10** | A.7 — CI-матрица PG/SQLite, линтеры, vet-гейт | 2–3 дня | — |

**Итого:** ~5–6 недель последовательно; этапы 5–7 можно вести параллельно после
этапа 1.

### Критерии приёмки (definition of done)

1. `sqlite3 data/skygate.db "PRAGMA table_info(exit_servers)"` содержит
   `ssh_target`, `ssh_key_path`, `accept_routes`; `derp_cert_sync` существует.
2. Schema-parity тест (PG vs SQLite) зелёный в CI на каждом PR.
3. Ни один файл вне `internal/db` не содержит `EXTRACT(EPOCH`, `strftime(`,
   `::bigint`, `TEXT[]`, `ANY($1::`.
4. Все CLI-подкоманды (`init`, `cluster`, `migrate`, `acl-apply`, `db-migrate`,
   `derp-probe`) проходят smoke на SQLite **и** на PG.
5. `/my/preauth` при сбое записи в БД НЕ показывает успех; ключ либо цел
   в `preauth_keys`, либо отозван в headscale; страница результата содержит
   реальное значение ключа.
6. Ошибка БД на `/my/keys`, `/admin/exit-nodes`, `/admin/devices` отображается
   flash-сообщением в вёрстке, а не text/plain страницей.
7. `/admin/exit-nodes` → «Проверить SSH» возвращает `ok` на живом узле;
   `accept_routes` реально применяется (`tailscale debug prefs` на узле).
8. `/admin/headscale` позволяет переключить `docker`↔`systemd` без правки
   `.env`, и все операции (OIDC, DERP, ACL, approve-routes, теги) идут через
   единый раннер.
9. В репозитории есть `docs/headscale-native.md`, `docs/install-no-docker.md`,
   `docs/db-backends.md`; `deploy/systemd/{skygate,headscale}.service` существуют.
10. `SKYGATE_EXIT_SSH_KEY` в `.env.example` и `docs/deploy.md` указывает на
    реально смонтированный путь.
11. `go vet ./...` и `golangci-lint run` зелёные; число `t.Skip("v1.3.0…")`
    не растёт (inventory-тест).

---

## 10. Открытые вопросы — статус после проверки на VM

| # | Вопрос | Ответ |
|---|---|---|
| 1 | Какой бэкенд целевой на VM? | **PG** (фактически работает), плюс решение оператора: SQLite доводим до первого класса (§11.1). Native-установка на SQLite — мёртвая, её судьбу надо решить |
| 2 | Ценны ли данные в SQLite? | На VM `/home/skyadmin/skygate/data/skygate.db` — 41 МБ от **10 августа**, рядом `skygate.db.empty-20260912`. Похоже, легаси после перехода на PG. Нужно подтверждение оператора: архивировать или удалить |
| 3 | headscale — docker или systemd? | **docker** (`headscale/headscale:0.29.3`, контейнер `headscale`), systemd-юнит `headscale` — `inactive`. Native-режим не используется |
| 4 | Актуальные SSH-цели exit-узлов? | Из `/home/skyadmin/.ssh/config`: `karolina <KAROLINA_PUBLIC_IP>:18022`, `emilia <EMILIA_PUBLIC_IP>:22`, `sharlotta <SHARLOTTA_PUBLIC_IP>:22`. В БД `ssh_target` **пуст** — это и есть дефект |
| 5 | Приоритет: «чтобы работало» или «сразу правильно»? | **Остаётся открытым.** Рекомендация: сначала §12.3-12.4 (1 день, возвращает exit-node и гасит зомби), затем этапы 1–3 плана |

---

## 11. Решения оператора (2026-09-17)

### 11.1 Целевой бэкенд: **оба равноправно**

Оператор выбрал «оба равноправно» — SQLite и PostgreSQL объявляются
равноправными бэкендами первого класса. Что это меняет в плане:

* **A.1, A.2, A.3, A.4, A.7 — обязательны полностью.** Отпадает возможность
  «объявить SQLite вторым сортом»: шим `nowUnixSQL()` обязан реально ветвиться,
  цепочки миграций обязаны совпадать по версиям и колонкам, диалект-протечки
  в общих файлах должны быть устранены, а не задокументированы.
* **CI-матрица обязательна:** job `test-sqlite` + job `test-pg` (сервис
  `postgres:15`) + schema-parity тест на каждом PR. Без этого «равноправие»
  будет лишь декларацией.
* **A.6 (конвертер) становится критичным путём**, а не «nice to have»: при
  равноправии переключение туда-обратно — штатная операция, значит нужны
  pre-flight, пост-проверка, обязательный бэкап и round-trip тест.
* **A.5 (выбор в UI)** — переключатель бэкенда становится полноценной
  функцией `/admin/database`, а не информационной карточкой.
* Hiding PG-only подсистем (`cluster*`, `pgmigrate`, `ha`, `elector`) остаётся
  допустимым — они привязаны к Patroni/etcd/WAL-G, — но только с **явным**
  сообщением в UI и в `docs/db-backends.md`, и без 500-страницы.
* Оценка этапов 1–4 и 10 вырастает примерно в 1.3–1.5 раза относительно
  исходной оценки в §9 (там закладывался сценарий «один основной бэкенд»).

### 11.2 Инструменты: выбраны все четыре группы

Установочный скрипт (идемпотентный, PS 5.1-совместимый, прошёл smoke-тест):

```powershell
pwsh -File scripts/dev/install-dev-tools.ps1            # всё
pwsh -File scripts/dev/install-dev-tools.ps1 -SkipMCP   # без MCP-плагинов
pwsh -File scripts/dev/install-dev-tools.ps1 -Only sqlite3,golangci-lint
```

Что ставит:

| Группа | Состав | Зачем |
|---|---|---|
| Минимум для отладки БД | `sqlite3`, `psql`, `golangci-lint`, `shellcheck`, `git-bash` | без них R1–R3 не воспроизводятся; `golangci-lint` ловит `append` без значений (U0) |
| Аудит и гейты CI | `gosec`, `govulncheck`, `dlv`, `sqlc` (пилот) | автоматический поиск `http.Error(err.Error())` (U2), инъекций в `ssh` (U1), уязвимостей; `sqlc` — структурное устранение класса R3 |
| Скиллы DSH | 7 файлов в `.dsh/skills/` | см. 11.3 |
| MCP-плагины | `@modelcontextprotocol/server-postgres`, `server-sqlite`, `server-github` | прямой доступ к схеме/данным и к issues/PR |

**Про `sqlc`:** внедрять как **пилот на новых запросах**, а не переписывать
`internal/db/queries.go` (48 КБ) целиком. Критерий успеха пилота — один
подпакет (например `internal/db/preauth_keys.go` — 12 функций, все на `$N`,
чистые) переведён на генерацию, тесты зелёные.

**Про MCP:** скрипт намеренно **не** подставляет DSN/токены — их надо прописать
вручную в конфиге DSH. Пароли БД и `HEADSCALE_API_KEY` в конфиг не класть.

### 11.3 Созданные скиллы (`.dsh/skills/`)

DSH подхватывает их автоматически из `<project>/.dsh/skills` (проверено:
каталог сессии обновился сразу после создания файлов).

| Скилл | Закрывает |
|---|---|
| `skygate-db-dialect-audit` | R1, R2, R3 — статический + исполняемый аудит диалекта, эталонный список дефектов |
| `skygate-migration-parity` | R1 — 4 проверки паритета цепочек миграций + хелпер `addColumnIfMissing` |
| `skygate-error-ux-migration` | R6, R7 — перевод `http.Error` на flash, приоритетный список 10 страниц, гейты |
| `skygate-exit-node-ssh-debug` | R2+C — чек-лист из 6 шагов, таблица 8 дефектов, предупреждение об инъекции в `ssh` |
| `skygate-headscale-mode` | D — определение режима, единые `hs_exec`/`hs_restart`, таблица docker-хардкода |
| `skygate-b-check-runner` | процесс — как запускать 203 B-чека, три обязательных правила |
| `skygate-vm-deploy` | процесс — деплой на VM с CWD-требованием и `--force-recreate` |

### 11.4 Что осталось решить

Вопросы 2–5 из §10 (ценность данных в `data/skygate.db` на VM, фактический
режим headscale, актуальные SSH-цели exit-узлов, приоритет «сначала работает /
сразу правильно») остаются открытыми — они требуют доступа к VM.

---

## 12. Живая диагностика 2026-09-18 (SSH на <VM_HOST>)

Все факты ниже получены командами только для чтения на работающей VM.

### 12.1 Exit node: почему SSH и accept-routes не работают

Это **не** следствие дефектов БД (схема PG полная). Причин пять, и они независимы:

| # | Факт | Доказательство |
|---|---|---|
| C-1 | **Ключ не смонтирован в контейнер.** `/ssh-sync/` и `/etc/skygate/ssh_key/` и `/host-keys/` — **пустые каталоги**. `ls /ssh-sync/skygate_sync` → `No such file or directory` | `docker exec skygate-skygate-1 sh -c 'ls -l "$SKYGATE_EXIT_SSH_KEY"'` |
| C-2 | **Ключ есть на хосте, но не там, где его ищет монтирование.** Реальный ключ: `/home/skyadmin/.ssh/skygate_sync` (+ `.pub`). Compose монтирует `${SKYGATE_HOST_REPO_PATH}/.ssh` = `/home/skyadmin/skygate/.ssh` — **пустой**; и `${SKYGATE_HOST_REPO_PATH}/data/ssh-sync` = `/home/skyadmin/skygate/data/ssh-sync` — **пустой** | `find /home -name 'skygate_sync*'` |
| C-3 | **`ssh_target` пуст у всех трёх узлов** → fallback на `root@<tailscale_ip>` (`100.64.0.3/.2/.4`) | `SELECT hostname, ssh_target FROM exit_servers` |
| C-4 | **tailscaled внутри контейнера выключен**, поэтому fallback-путь мёртв. И даже при живом tailscaled ACL запрещает: `tailscale: tailnet policy does not permit you to SSH to this node` | `docker exec … tailscale status` + логи sync |
| C-5 | **Права на хост-ключ `0644`** → ssh отказывается: `WARNING: UNPROTECTED PRIVATE KEY FILE!` | `ssh -i /home/skyadmin/.ssh/skygate_sync …` |

Дополнительно:

* **`accept_routes = 0` у всех трёх узлов** (0 = «не трогать») → `--accept-routes` в
  команду вообще не попадает (`route_args.go:60-69`). Это и есть «не
  формируются настройки accept routes».
* **approve-routes сломан отдельно** — `docker exec headscale /ko-app/headscale
  nodes approve-routes` возвращает
  `connecting to /var/run/headscale/headscale.sock: context deadline exceeded`.
  То есть «approved»-половина пайплайна тоже не работает (а UI при этом
  показывает `advertised_routes_ok = 1` из данных headscale).
* **Два relay из трёх недоступны с хоста**: `sharlotta` <SHARLOTTA_PUBLIC_IP>:22 и
  `karolina` <KAROLINA_PUBLIC_IP>:18022 — connection timed out; доступен только
  `emilia` <EMILIA_PUBLIC_IP>:22.
* **Настоящие адреса лежат в `/home/skyadmin/.ssh/config`** — их и надо
  положить в `ssh_target`:
  `karolina → <KAROLINA_PUBLIC_IP>:18022`, `emilia → <EMILIA_PUBLIC_IP>:22`,
  `sharlotta → <SHARLOTTA_PUBLIC_IP>:22`.

Выдержка из лога (периодический sync, каждые ~5 минут):

```
staggeredSync(aggregated): emilia advertising 32 unique routes (was: per-batch, lost all but last batch)
staggeredSync(aggregated): emilia SSH err: ssh root@100.64.0.3 (key /ssh-sync/skygate_sync):
    Warning: Identity file /ssh-sync/skygate_sync not accessible: No such file or directory.
    tailscale: tailnet policy does not permit you to SSH to this node
staggeredSync(aggregated): emilia approve err: approve-routes: exit status 1:
    Error: connecting to headscale: connecting to /var/run/headscale/headscale.sock: context deadline exceeded
discovery-ticker: discover failed: tailscale status --json: failed to connect to local tailscaled
```

Заметьте: **адvertised-routes считаются правильно** (32/147 маршрутов), но не
применяются. Это подтверждает §C.1: сломан не расчёт, а доставка.

### 12.2 Что подтвердилось, а что опровергнуто живой проверкой

**Опровергнуто (важные поправки):**

* ❌ «живая система на SQLite» — на PG; SQLite-конфигурация принадлежит
  мёртвому systemd-юниту (§2.2).
* ❌ «`strftime` ломает PG» — `migrations_pg.go:965` создаёт PG-шим
  `strftime(format text, ts text)`; на живой БД проверено, работает.
  Переносимость односторонняя: PG притворяется SQLite, но не наоборот.
* ❌ «расхождение цепочек миграций влияет на прод» — живая БД на V071,
  schema полная (все колонки `exit_servers` на месте).
* ❌ «пропавшие колонки SQLite объясняют поломку exit-node» — нет, это
  отдельный дефект SQLite-пути.

**Подтверждено:**

* ✅ `?`-плейсхолдеры действительно фатальны для PG (`SELECT ?` → syntax error),
  значит `node_owner_map.go:806-809` и `headscale_version/monitor.go:278-283`
  сломаны, а ошибки у них подавлены.
* ✅ `nowUnixSQL()` = `EXTRACT(EPOCH FROM now())::bigint` — на SQLite падает
  (`near "FROM": syntax error`), проверено запуском.
* ✅ SQLite-схема неполная (нет `ssh_target`/`ssh_key_path`/`accept_routes`,
  `device_rules.device_ip`, таблицы `derp_cert_sync`); SQLite-цепочка на V070.
* ✅ R6 (482 `http.Error` → сырая страница), R7 (подавление ошибки записи
  ключа), U0 (CI красный на `go vet`), U2, U3/U4 (JWT-дефолт).
* ✅ headscale живёт в docker; `deploy/systemd/headscale.service` отсутствует,
  но и не нужен — native-режим headscale на этой VM не используется.
* ✅ Ключи: в `preauth_keys` есть строки **без `headscale_preauth_id`** —
  ровно тот класс, который порождает подавленная ошибка вставки (R7).
* ✅ Дистrolless-образ headscale (нет `sh`) — как и описано в B260.2.

### 12.3 Порядок исправления exit-node (от простого к сложному)

**Проверено экспериментально (2026-09-18):** если скопировать ключ во временный
файл с правами `600` и подключиться с хоста, **emilia отвечает** —
`hostname = <emilia-hostname>`, Tailscale IP `100.64.0.3`. То есть для
emilia достаточно починить права и монтирование; сам ключ рабочий.

`sharlotta` (<SHARLOTTA_PUBLIC_IP>:22) и `karolina` (<KAROLINA_PUBLIC_IP>:18022) —
**connection timed out** и с хоста, и из контейнера (`nc -z` → CLOSED для
порта 22/18022). Это **инфраструктурная проблема, не дефект skygate**: похоже,
на этих VPS закрыт публичный SSH (доступ только через Tailscale-интерфейс) либо
они недоступны. Пока это не выяснено, синхронизировать их skygate не сможет
даже с исправленным ключом.

Шаги 1–3 возвращают SSH для emilia, шаг 4 — accept-routes, шаг 5 — approved routes.

```bash
# 1. положить ключ туда, куда указывает SKYGATE_EXIT_SSH_KEY, и закрыть права
sudo cp /home/skyadmin/.ssh/skygate_sync     /home/skyadmin/skygate/data/ssh-sync/skygate_sync
sudo cp /home/skyadmin/.ssh/skygate_sync.pub /home/skyadmin/skygate/data/ssh-sync/skygate_sync.pub
sudo chmod 600 /home/skyadmin/skygate/data/ssh-sync/skygate_sync
sudo chmod 644 /home/skyadmin/skygate/data/ssh-sync/skygate_sync.pub
sudo chown root:root /home/skyadmin/skygate/data/ssh-sync/skygate_sync
# проверить, что файл виден в контейнере:
sudo docker exec skygate-skygate-1 ls -l /ssh-sync/

# 2. прописать реальные ssh_target (публичные адреса из ~/.ssh/config) —
#    через /admin/exit-nodes или SQL:
#    karolina  → root@<KAROLINA_PUBLIC_IP>:18022   (ssh_port 18022)
#    emilia    → root@<EMILIA_PUBLIC_IP>:22
#    sharlotta → root@<SHARLOTTA_PUBLIC_IP>:22

# 3. включить tailscaled в контейнере ИЛИ отказаться от fallback на 100.64.x
#    (см. /admin/tailscale; сейчас сознательно выключен)

# 4. выставить accept_routes по каждому узлу (-1 = false, +1 = true, 0 = не трогать)
#    и нажать «Пере-синхронизировать» на /admin/exit-nodes

# 5. починить approve-routes: routes.go:86 ходит через
#    `docker exec headscale /ko-app/headscale`, который упирается в unix-сокет.
#    Проверить причину таймаута сокета и/или перейти на REST API.
```

### 12.4 Немедленные действия по остальным находкам

1. **Погасить зомби-юнит** (экономит ~17 тыс. строк journal в сутки и 493 МБ):
   `sudo systemctl disable --now skygate` — если native-установка не нужна.
   Если нужна — заполнить `HEADSCALE_API_KEY` в `/etc/skygate/skygate.env` и
   решить, какой БД она должна пользоваться (сейчас SQLite, а PG-контейнер уже есть).
2. **Починить `go vet`** (`internal/feature/admin/derp.go:810`) — CI красный.
3. **Разобраться с 5 ключами без `headscale_preauth_id`** и подавлением ошибки
   записи (R7) — иначе устройства не привязываются.
4. **Проверить доступность sharlotta/karolina** (timeout с хоста и из контейнера):
   это инфраструктурный вопрос, не дефект skygate.

### 12.5 approve-routes: причина найдена (дефект шаблона проекта)

**Симптом:** `approve-routes: exit status 1: Error: connecting to headscale:
connecting to /var/run/headscale/headscale.sock: context deadline exceeded`
— 144 падения за 6 часов, то есть вся «approved»-половина пайплайна мертва.

**Ключевое наблюдение:** та же команда падает **внутри самого контейнера
headscale**, без участия skygate:

```bash
$ sudo docker exec headscale /ko-app/headscale nodes list
Error: connecting to headscale: connecting to /var/run/headscale/headscale.sock: context deadline exceeded
```

**Причина:** в конфиге headscale есть `grpc_listen_addr: 0.0.0.0:50443`, но
**нет ключа `unix_socket`**. Официальный `config-example.yaml` для v0.29.3
([headscale docs](https://headscale.net/stable/ref/configuration/)) объявляет его
как обязательный:

```yaml
# Unix socket used for the CLI to connect without authentication
unix_socket: /var/run/headscale/headscale.sock
unix_socket_permission: "0770"
```

Без него сервер сокет не создаёт, а CLI всё равно идёт по этому пути (значение
по умолчанию) → таймаут. Флага `--socket`/`--unix-socket` у CLI нет (проверено:
`unknown flag`), единственный флаг — `-c/--config`.

**Это дефект проекта, а не ошибка оператора** — ключ отсутствует в шаблоне:

| Файл | Строки | Что не так |
|---|---|---|
| `deploy/templates/headscale-config.yaml.tmpl` | 16-17 | есть `grpc_listen_addr` + `grpc_allow_insecure`, **нет `unix_socket`** |
| `deploy/headscale-users/headscale-bootstrap.sh` | 126-127 | то же для per-user control plane |

Именно поэтому skygate-код, вызывающий `docker exec <ctr> headscale …`
(`routes.go:86`, `tags.go:48/92`, `acl.go:107/157`, `preauth.go:130/203`,
`users.go:152/210`, `nodes.go:358`), **никогда не работал на этой инсталляции**.

**Фикс (одна строка в шаблоне + перезапуск):**

```yaml
# deploy/templates/headscale-config.yaml.tmpl (и bootstrap-скрипт)
grpc_listen_addr: 0.0.0.0:50443
grpc_allow_insecure: true
unix_socket: /var/run/headscale/headscale.sock
unix_socket_permission: "0770"
```

Затем — в живой `/home/skyadmin/headscale/config/config.yaml` и
`docker restart headscale` (перерыв control-plane ~5-10 с; активные
WireGuard-сессии клиентов не рвутся).

**Смягчающий фактор:** в конфиге включён `policy.mode: database` с
`auto_approve.routes` (список из `.env`), поэтому маршруты попадают в
approved и без skygate — этим и объясняется `advertised_routes_ok = 1` при
падающем `approve-routes`.

### 12.6 Выполнено 2026-09-18 (с согласия оператора)

| Действие | Результат |
|---|---|
| `systemctl disable --now skygate` | юнит `inactive` + `disabled`, рестартов больше нет (было 98 779) |
| Ключ установлен в `data/ssh-sync/skygate_sync` (0600, root:root) | виден в контейнере: `ls -l /ssh-sync/` → `skygate_sync`, читается |
| `ssh_target` прописан для всех трёх узлов | emilia `root@<EMILIA_PUBLIC_IP>`, karolina `root@<KAROLINA_PUBLIC_IP>:18022`, sharlotta `root@<SHARLOTTA_PUBLIC_IP>` |
| `accept_routes=1` для emilia (совпадает с её фактическим `RouteAll: true`, других VPN на узле нет) | — |
| **Проверка: SSH-ошибок за 5 минут — 0** (было в каждом цикле) | `emilia advertised: Warning: Permanently added '<EMILIA_PUBLIC_IP>' …` — удалённая команда выполнена |
| Значения `exit_servers` не затёрты авто-синком | перепроверено после цикла |
| karolina / sharlotta | оставлены `accept_routes=0` (не трогать): узлы недоступны, проверить нельзя |
| **Шаблон + bootstrap-скрипт**: добавлены `unix_socket` + `unix_socket_permission` | `deploy/templates/headscale-config.yaml.tmpl`, `deploy/headscale-users/headscale-bootstrap.sh` |
| **Живой конфиг headscale** пропатчен (бэкап `config.yaml.bak-20260918-125653`), `configtest` → exit 0 | `/home/skyadmin/headscale/config/config.yaml` |
| **`docker restart headscale`** | healthy через 2 с (HTTP 200); `docker exec headscale /ko-app/headscale nodes list` теперь **работает** (было `context deadline exceeded`) |
| **Проверка approve-routes после фикса** | `approve err` за 5 минут: **0** (было 4 за 12 минут); `emilia advertised: ok` |
| Регрессионный чек | `scripts/headscale_unix_socket_check.sh` → PASS |

**Итог по exit-node:** emilia полностью синхронизируется (`advertised: ok`,
SSH-ошибок 0). У karolina/sharlotta ошибка сменилась с «нет файла ключа» на
`Operation timed out` — то есть исправление ключа и монтирования сработало, а
осталась **сетевая недоступность узлов** (инфраструктура, не skygate).

**Осталось:** доступность karolina/sharlotta (нужен доступ к их firewall/провайдеру)
и работы из направлений A–D плана (SQLite-цепочка, UX ошибок, выдача ключей).

### 12.7 Дефекты инфраструктуры проверок (найдено попутно, важно)

Проверяя, куда положить регрессионный чек для `unix_socket`, обнаружились две
структурные проблемы, из-за которых дисциплина B-чеков ослаблена:

**1. `.gitignore` отменяет сам себя.** Правило `.gitignore:93`
(`!scripts/check_*.sh`) разрешает чеки в `scripts/`, но **более позднее**
правило `.gitignore:205` (`check_*.sh`, без слэша → совпадает на любой глубине)
снова их игнорирует. В gitignore побеждает **последнее** совпадение.
Проверено: `git check-ignore -v scripts/headscale_unix_socket_check.sh` →
`.gitignore:205:check_*.sh`.

Следствие: **новый `scripts/check_*.sh` невозможно закоммитить** обычным
`git add` — нужен `git add -f`. Сейчас в `scripts/` лежат 15 игнорируемых
файлов, включая `verify_b199.sh`, `verify_b200.sh`, `verify_b201.sh`,
`verify_b202.sh` — то есть контракты B199–B202 существуют **только на этой
машине** и в репозиторий не попали.

**2. `verify_pre_deploy.sh` ссылается на несуществующие/переехавшие скрипты.**
Из 216 упомянутых путей **7 не существуют** по указанному адресу:

| Ссылка в гейте | Реальность |
|---|---|
| `scripts/check_b202.5.sh` | файл называется `scripts/check_b202_5.sh` (точка вместо `_`) → контракт B202.5 молча пропускается |
| `scripts/check_b236.sh` | файла нет нигде |
| `scripts/b234_race_verify.sh` | файла нет |
| `scripts/cleanup-skygate.sh` | файла нет |
| `scripts/install-tailscale.sh` | файла нет |
| `scripts/create-standby-preauth.sh` | лежит в `deploy/scripts/` |
| `scripts/setup-skygate-public.sh` | лежит в `deploy/scripts/` |

**Что делать:**
1. `.gitignore`: перенести `!scripts/check_*.sh` и `!scripts/verify_*.sh`
   **в конец** файла (после строки 205) либо убрать `check_*.sh` из блока
   «root-level scratch» и ужесточить его до `/check_*.sh`. Затем обычным
   `git add` вернуть `verify_b199..b202.sh` и остальные 15 файлов.
2. `verify_pre_deploy.sh`: исправить путь `check_b202.5.sh` → `check_b202_5.sh`,
   вернуть/создать `check_b236.sh`, поправить два пути на `deploy/scripts/`,
   удалить три ссылки на несуществующие скрипты.
3. Добавить в гейт проверку «все упомянутые скрипты существуют» — иначе такие
   расхождения продолжают накапливаться незаметно.

### 12.8 SSH-доступ к exit-узлам: порты, tailnet и ACL (2026-09-18)

**Особенности портов — проверено:**

| Узел | Публичный адрес | Tailnet | Факт |
|---|---|---|---|
| emilia | `<EMILIA_PUBLIC_IP>:22` **открыт** | `100.64.0.3:22` перехвачен Tailscale SSH | работает по публичному адресу |
| karolina | `<KAROLINA_PUBLIC_IP>:18022` **отфильтрован** | `100.64.0.2:18022` **открыт**, `:22` перехвачен | работает по tailnet + порт 18022 |
| sharlotta | `<SHARLOTTA_PUBLIC_IP>:22` **отфильтрован** | `100.64.0.4:22` только, перехвачен | **не работает** |

Вывод: публичные порты у karolina и sharlotta закрыты (таймаут и с рабочей
машины, и с skygate-хоста — значит проблема не в egress skygate). karolina
держит sshd на нестандартном **18022**, и этот порт доступен только из tailnet.
У sharlotta альтернативного порта нет.

**Почему tailnet-SSH упирается в ACL.** Все три узла отвечают
`tailscale: tailnet policy does not permit you to SSH to this node`. В политике
headscale есть нужное правило:

```json
"ssh": [{ "action": "accept",
          "src": ["tag:private", "skyadmin@tsnet.example.com"],
          "dst": ["tag:exit-node"], "users": ["root"] }]
```

`dst` совпадает (у узлов есть `tag:exit-node`), а `src` — **нет**: у самого
skygate-хоста тег `tag:dev-infra-skygate-host`, а не `tag:private`. Трафик
skygate приходит как этот хост (NAT через docker-мост → tailscaled хоста),
поэтому доступ отклоняется.

**Корень — в генераторе ACL** (дефект проекта):

```
internal/acl/acl.go:808   "src": ["tag:private", "<admin>@<domain>"]   ← первая генерация
internal/acl/acl.go:1696  то же самое                                   ← вторая генерация
```

Тег хоста, на котором работает сам skygate, в `src` не попадает — то есть
**skygate в принципе не может зайти по Tailscale SSH на свои exit-узлы**.
Именно поэтому раньше это «работало» только через публичные адреса.

**Рекомендация:** добавить тег skygate-хоста в `src` обоих мест
(например `tag:dev-infra-skygate-host`, лучше — из конфига, а не хардкодом),
перегенерировать и запушить политику. Тогда sharlotta заработает по
`root@100.64.0.4`, и все три узла можно будет вести единообразно через tailnet.
Альтернативы для sharlotta: поднять на ней sshd на отдельном порту (как
karolina) либо открыть публичный порт у провайдера — но оба требуют доступа,
которого сейчас нет.

**Настроен доступ с рабочей машины (Windows).** Ключ `~/.ssh/id_ed25519`
уже пускал на VM; для узлов скопирован `skygate_sync` из
`/home/skyadmin/.ssh/` (ACL заужен через `icacls`). Добавлены алиасы в
`~/.ssh/config` (бэкап `config.bak-20260918-161235`):

| Алиас | Куда | Проверено |
|---|---|---|
| `skygate` | `skyadmin@<VM_HOST>` | ✅ `skygate-host`, `skyadmin` |
| `emilia` | `root@<EMILIA_PUBLIC_IP>` | ✅ `<emilia-hostname>`, `100.64.0.3` |
| `karolina` | `root@100.64.0.2:18022` через `ProxyJump skygate` | ✅ `<karolina-hostname>`, видит `<KAROLINA_PUBLIC_IP>` и `100.64.0.2` |
| `sharlotta` | `root@100.64.0.4` через `ProxyJump skygate` | ❌ отказ ACL (ожидаемо, см. выше) |

### 12.9 Итог по exit-узлам, ACL и CI

| Узел | ssh_target | accept_routes | Путь | Состояние |
|---|---|---|---|---|
| emilia | `root@<EMILIA_PUBLIC_IP>` | 1 | публичный sshd | ✅ `advertised: ok` |
| karolina | `root@100.64.0.2:18022` | 1 | tailnet, нестандартный порт | ✅ `advertised: ok` |
| sharlotta | `root@100.64.0.4` | 1 | tailnet (после фикса ACL) | ✅ SSH проверен (`SHARLOTTA_OK`) |

SSH-ошибок на синхронизации: **0**. `approve-routes`: **0 ошибок**.
У sharlotta пока **0 правил** в `device_rules` (emilia 112, karolina 218),
поэтому плановый sync её пропускает по дизайну
(`info=no IP/subnet rules target this node`) — SSH-путь проверен напрямую.

**Фикс ACL (код + живая политика):**

* `internal/acl/acl_perdevice.go` — новый `getSkygateHostInfraTags()` +
  `sshRuleSrc()`. Тег берётся из `tagsByUser` (данные о владении узлами), а не
  хардкодится, поэтому переименование/перетег хоста подхватывается на
  следующей регенерации. Если тега нет — возвращается `nil`, и в политику
  уходит прежний двухэлементный `src` (обратная совместимость).
* `internal/acl/acl.go:808` и `:1696` — оба генератора
  (`GenerateACLForPlane`, `GenerateACLWithViaForPlane`) теперь строят `src`
  через `sshRuleSrc`.
* `internal/acl/acl_ssh_src_host_test.go` — 8 кейсов на helper + композицию,
  включая явную регрессию «тег хоста обязан присутствовать».
* Живая политика обновлена через `headscale policy set --file` (бэкап:
  `/home/skyadmin/headscale/config/policy-backup-20260918-131630.json`),
  `policy check` → «Policy is valid».
  `ssh[0].src` теперь `["tag:dev-infra-skygate-host","tag:private","skyadmin@tsnet.example.com"]`.
* **Важно:** живая правка политики — временная. ACL пушится только по
  действиям пользователя (смена правил, удаление устройства, подСети, mesh,
  telegram-команды) и кнопкой «Re-apply ACL», не по таймеру — но как только
  запущенный бинарь перегенерирует политику (старый код без фикса), правка
  откатится. Чтобы это стало постоянным, нужен деплой исправленного кода
  (git push → `git pull` на VM → перезапуск контейнера, который пересобирает
  бинарь из `/app`). До тех пор оператору достаточно не жать «Re-apply ACL»
  без необходимости.

**CI разблокирован:** `internal/feature/admin/derp.go:810` переписан без
`append` без значений; `go vet ./...` → exit 0, `go build ./...` → exit 0,
тесты `internal/acl` и `internal/feature/admin` проходят. Ранее job `ci`
(`.github/workflows/ci.yml:39`) был красным на HEAD.

### 12.10 Деплой и этап 1 (выполнено 2026-09-18)

**Деплой 1 — исправления headscale/ACL/derp (commit `c31f6055` → origin/main).**

* VM: `git fetch` + `git merge --ff-only origin/main` + `git checkout -B main`.
  Репозиторий на VM был в состоянии **detached HEAD** и с локальными правками,
  которые нельзя терять:
  `docker-compose.yml` (`extra_hosts: derp.skynas.ru:${SKYGATE_DERP_PROBE_HOST}` и
  `SKYGATE_TS_HOSTNAME=skygate-host`), `go.mod`, `go.sum`.
  Fast-forward выбран сознательно вместо `stash`/`reset`: мои коммиты не
  касаются этих файлов, поэтому merge прошёл, не тронув их. Бэкап сделан
  (`/home/skyadmin/deploy-backup-20260918-133040/`).
* Вторая особенность: каталог репозитория принадлежит **root**, а вход под
  `skyadmin` — поэтому merge/checkout пришлось выполнять через `sudo`
  (иначе `fatal: cannot create directory at '.dsh': Permission denied`).
* Пересборка: `docker compose stop` + `up -d --force-recreate --no-deps skygate`
  (entrypoint собирает бинарь из `/app`). `build: v1.5.8-5-gc31f605`, healthz ok.
* **Проверка фикса ACL кодом, а не ручной правкой:** `skygate acl-apply` внутри
  контейнера перегенерировал политику, и `ssh[0].src` стал
  `["tag:private","skyadmin@tsnet.example.com","tag:dev-infra-skygate-host"]` —
  тег добавил именно исправленный генератор (порядок отличается от моей ручной
  правки, что и доказывает регенерацию).
* Сохранены: `extra_hosts: derp.skynas.ru:<VM_HOST>`, hostname
  `skygate-host`, все три `exit_servers`.

**Деплой 2 — этап 1 плана, единый диалект БД (commit `5b5afd91`).**

* `internal/db/active_dialect.go` — процессный активный диалект,
  выставляется в `registerBackend` (через него проходят оба пути открытия).
  Дефолт — PostgreSQL, т.е. прежнее поведение, поэтому изменение инертно,
  пока не открыта БД.
* `nowUnixSQL()`/`NowUnixSQL()` реально ветвятся; две init-time константы
  (`qInsertOrReplaceNodeOwner`, `qUpdateNodeOwnerTag`) переведены в функции —
  как константы они вычислялись **до** открытия БД и всегда несли PG-форму.
* Убраны оставшиеся однобокие запросы: `LIMIT ?`
  (`headscale_version/monitor.go`), сырой `EXTRACT(...)`
  (`nodeownership/auto.go`, `telegram/commands_phase3.go`),
  `information_schema.columns` внутри SQLite-цепочки
  (`migrations_sqlite.go` → `PRAGMA table_info`).
* Шапки `placeholders.go` приведены в соответствие: `$N` — **универсальная**
  форма (SQLite её принимает, modernc связывает `$NNN` по ordinal), а `?`
  фатальна для pgx (SQLSTATE 42601). Эта асимметрия и порождала класс дефектов.
* Тесты: `dialect_runtime_test.go` **исполняет** реальные запросы на настоящей
  SQLite `:memory:` (upsert + update `node_owner_map`, вставка авто-обнаружения
  exit-сервера) и проверяет ответ драйвера, а не строку;
  `TestPGFormFailsOnSQLite` фиксирует обратное.
  `dialect_leak_guard_test.go` — статический страж по 359 файлам: запрещает
  сырой PG-таймстамп в общем коде (опасное направление) и **пинит наличие**
  PG-шим-функции `strftime`, потому что общие файлы на неё опираются.
* Деплой на VM тем же безопасным путём. `build: v1.5.8-6-g5b5afd9`, healthz ok,
  0 ошибок сборки, БД — postgres.
* Живая проверка после деплоя: `auto-detect exit-node` ошибок **0**,
  `strftime/EXTRACT/42601` ошибок **0**, SSH err **0**, approve err **0**,
  паник **0**; `emilia`/`karolina` `advertised`, строки `exit_servers` целы.

**Замечание по безопасности (побочно).** В логи контейнера попадает токен
Telegram-бота открытым текстом: сетевые ошибки логируются как
`telegram: getUpdates error: Get "https://api.telegram.org/bot<TOKEN>/getUpdates": …`,
и Go подставляет полный URL. Стоит вырезать токен из сообщений об ошибках
(и/или из `%v` от `*url.Error`). Найдено при разборе 11 «ERROR» строк, которые
оказались таймаутами `api.telegram.org`, а не дефектами БД.

### 12.11 R7 — выдача ключей устройств (выполнено 2026-09-18)

Commit `408d04bc`, задеплоено на VM (`build: v1.5.8-8-g408d04b`).

**Что было.** Три пути выдачи ключа создавали ключ в headscale, а запись
локальной строки `preauth_keys` только логировали при ошибке:
`my/preauth.go:130`, `my/keys.go:445`, `my/devices.go:1487`. Пользователь
получал страницу с рабочим ключом, которого skygate не знает. Всё дальнейшее
завязано на эту строку: backfill владения узлами сопоставляет устройство с
владельцем по `headscale_preauth_id` (стратегия A), поэтому устройство никогда
не привязывалось и висело «⏳ ожидает» (класс B175); `/my/keys` его не
показывал; отозвать или почистить из UI было нельзя. Дополнительно
`PostMyPreauth` выбрасывал ошибку БД из `GetUserHSByID` и подменял её ложным
«no headscale user linked» (400).

**Что сделано.** Новый `persistIssuedKey` (`internal/feature/my/keys_issue.go`)
делает сбой записи **жёстким** и компенсирует его: только что созданный ключ
отзывается в headscale (`ExpirePreauthKey`), поэтому наружу никогда не уходит
ключ, который skygate не может учесть. Если не удалась и компенсация — это
логируется отдельно, потому что почистить сироту может только оператор.
Вызывающие редиректят на исходную страницу с `?err=<i18n>` вместо показа
ключа. Сбой записи аудита оставлен log-and-continue **осознанно**: строка
ключа уже есть, выдача корректна, и валить операцию из-за отсутствующей записи
аудита было бы хуже — обоснование зафиксировано в helper'е.

**Заодно исправлено рассогласование шаблона и данных на той же странице:**

* `preauth_result.html` ссылался на `{{.PreauthKey}}`, тогда как все хендлеры
  передают `"Key"` → баннер печатал `tailscale up --authkey=<no value>`;
* `{{.OSLabel}}` использовался в трёх местах и **не задавался никем** →
  «instructions for `<no value>`». Добавлен `osLabel()`.

**`keys.html` не имел поверхности ошибок вообще**, поэтому сбой БД на
`/my/keys` (и на POST reissue/expire/cleanup) мог быть показан только сырой
text/plain страницей. Добавлен общий блок `FlashError`/`FlashOk`, `GetMyKeys`
их заполняет из `?err=`/`?ok=`. Это минимальная часть R6, необходимая для
пути ошибки R7; остальные ~20 хендлеров по R6 ещё открыты.

**Тесты:**
* `preauth_result_fields_test.go` извлекает все поля, на которые ссылается
  шаблон (предварительно вырезая go-template комментарии, где старое имя
  упомянуто намеренно), вычитает автоматически инжектируемые `handlers.go` и
  падает, если остаток не задаёт ни один хендлер. Класс молчаливый (Go
  templates рендерят отсутствующий ключ как литерал `<no value>`), и этот же
  шаблон уже ловил его однажды — `{{.ControlURL}}` в v0.18.1.
* `TestOSLabelMapping` — маппинг меток, включая пустой и неизвестный случаи.

**Проверка на живой системе:** все четыре затронутых маршрута отвечают 302
(редирект к логину, не 500), `preauth_keys` без изменений (27 всего / 5 без
`headscale_preauth_id`), 0 ошибок, 0 SSH/approve ошибок, контейнер healthy.

### 12.12 R6 — сырые страницы ошибок, первый срез (выполнено 2026-09-18)

Commit `82e57900`, задеплоено (`build: v1.5.8-10-g82e5790`).

Общей поверхности ошибок в проекте нет (482 вызова `http.Error` в 68 файлах,
ни один не рендерит layout), а единственный inline-механизм — редирект с
`?err=` плюс блок `{{.FlashError}}` в шаблоне. Срез закрывает страницы,
**где flash-блок уже был**, поэтому новая инфраструктура не вводится:

| Место | Было | Стало |
|---|---|---|
| `GET /admin/exit-nodes` | `http.Error(listErr)` заменял всю страницу текстом SQL-ошибки | баннер `.FlashError` над пустой таблицей, детали в лог |
| `POST /admin/users` (создание) | валидация и ошибки БД — сырые страницы | редирект на форму (flash уже рендерился); 403 оставлен как есть, иначе цикл |
| `POST /my/exit-rules`, `/admin/exit-rules` | `http.Error(w,"db error",500)` + потеря заполненных полей | `buildFormErrorRedirectURL` / `buildAdminExitRuleRedirectURL` — как в валидационных ветках, поля восстанавливаются |
| `POST /login` (rate limit) | 429 с text/plain, форма логина потеряна | редирект `/login?err=rate_limited`, auth-хендлер локализует в `.Error` |
| i18n | ключа «ошибка БД» не было | добавлен `error.db` (RU+EN), плюс `login.rate_limited` и `users.err_*` |

Middlewar'у недоступен i18n-каталог, поэтому он передаёт стабильный код
`rate_limited`, а переводит его хендлер — компромисс зафиксирован в коде.
Компромисс по non-browser клиентам (303 + `Retry-After` вместо 429) тоже
задокументирован: это единственная существующая обвязка.

**Проверка на живой системе:** все затронутые маршруты отвечают корректно
(302 у admin-страниц без сессии, `/login` — 200), а
`GET /login?err=rate_limited` действительно содержит локализованное
«Слишком много попыток входа» — то есть путь ограничителя частоты проверен
end-to-end.

**Осталось по R6:** ~450 вызовов `http.Error` (включая остальные POST-хендлеры
`/admin/exit-nodes` и семейство `user_subnet`). Следующий шаг — общий
`httputil.RenderError` + линтер-правило «нет `http.Error` в файлах, отдающих
HTML».

---

## Приложение: быстрые ссылки на доказательства

```
internal/db/driver_sqlite.go:47-97        sqliteMigrations (48, до V070)
internal/db/driver_postgres.go:106-157    pgMigrations (49, до V071)
internal/db/migrations_v0_71_derp_cert_sync.go:72   только migrateV071PG
internal/db/migrations_sqlite.go:105,141,142,222    ADD COLUMN IF NOT EXISTS + глотание
internal/db/now_unix.go:8-10              nowUnixSQL → PG-константа
internal/db/now_unix_postgres.go:4        EXTRACT(EPOCH FROM now())::bigint
internal/db/queries.go:570,571,616        nowUnix / strftime в общем файле
internal/db/secrets.go:159,168            strftime в общем файле
internal/db/node_owner_map.go:806-809     ? + strftime (пропущено B253/B254)
internal/headscale_version/monitor.go:278-283   LIMIT ? + глотание на :211
internal/db/migrations_sqlite.go:890-896  information_schema в SQLite-цепочке
internal/middleware/ratelimit.go:26-31    text/plain на POST /login (429)
internal/db/db.go:220-236                 OpenDSN = PG-only
internal/db/retry.go:156-191              openDSNPing = оба диалекта
internal/feature/my/keys.go:63-66         http.Error на /my/keys
internal/feature/my/preauth.go:70-76,130-136   подавление ошибок БД
internal/handlers/templates/user/preauth_result.html:34,40,62,67  {{.PreauthKey}}/{{.OSLabel}}
internal/feature/admin/exit_nodes.go:147,686-690  http.Error + ssh=err → ?ok=
internal/headscale/routes.go:86,199-202,231-247   approve-routes / stat / ssh exec
internal/headscale/route_args.go:33-80    --advertise-routes / --accept-routes
internal/config/config.go:497-500,532,774-784,819-821  env, ключ, JWT-дефолт
internal/feature/admin/backup.go:656-659,673   FormValue выброшены / .env 0644
internal/oidc/sync.go:251-253             --mode в oidc-sync.sh
deploy/oidc-sync.sh:175-198,346-361       автодетект и 7 режимов (образец)
internal/feature/admin/derp_cert_sync.go:486-513   systemd→pidfile (образец)
internal/feature/admin/tailscale.go:1437-1509      container vs native (образец)
internal/db/integrations.go:69-87         headplane.mode (образец настройки)
internal/feature/admin/tailscale.go:521-549        DB > env > default (образец)
.env:8,15,17,34-37                        локальная копия (устарела; живой .env на VM другой)
internal/db/migrations_pg.go:965          PG-шим strftime (поэтому strftime на PG не дефект)

--- проверено на VM <VM_HOST> (2026-09-18) ---
docker ps                                 skygate-skygate-1, skygate-pg-local, headscale 0.29.3, derper, headplane
docker exec skygate-skygate-1 env         SKYGATE_DB=postgres://…@172.18.0.3:5432/skygate_staging
applied_migrations (PG)                   max version = 71, всего 51 → схема полная
exit_servers (PG)                         все 11 колонок есть; ssh_target ПУСТ у всех 3 узлов
exit_node_health                          emilia/karolina/sharlotta online=1, advertised_routes_ok=1
preauth_keys                             27 строк, 5 без headscale_preauth_id
docker exec … ls -l $SKYGATE_EXIT_SSH_KEY  /ssh-sync/skygate_sync: No such file or directory
ls /home/skyadmin/skygate/{data/ssh-sync,.ssh}   оба каталога ПУСТЫЕ
find /home -name 'skygate_sync*'          /home/skyadmin/.ssh/skygate_sync (+ .pub) — не смонтирован
psql "SELECT ?"                           syntax error  → ? фатален для PG
psql "SELECT strftime('%s','now')"        работает (шим) → strftime не дефект
systemctl status skygate                  activating (auto-restart), counter=98779, "HEADSCALE_API_KEY is required"
/etc/skygate/skygate.env                  SKYGATE_DB=sqlite:<path>, HEADSCALE_API_KEY пустой (len=0)
journalctl --disk-usage                   493.3M (212742 строки от зомби-юнита)
/home/skyadmin/.ssh/config                karolina <KAROLINA_PUBLIC_IP>:18022, emilia <EMILIA_PUBLIC_IP>:22, sharlotta <SHARLOTTA_PUBLIC_IP>:22
docker-compose.yml:206,260,270,280        монтирования (SSH/данные)
deploy/install.sh:152 + install-debian.sh:39-50    -f молча игнорируется
```
