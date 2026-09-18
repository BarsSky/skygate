---
name: skygate-db-dialect-audit
description: Аудит диалекта БД в skygate — находит SQL, валидный только для PostgreSQL или только для SQLite, и сверяет схемы обоих бэкендов. Использовать перед любой правкой запросов, миграций или при жалобе «БД работает некорректно».
whenToUse: Правка internal/db, миграций, любых SQL-строк; жалобы на ошибки БД; переключение SQLite⇄PostgreSQL.
---

# Аудит диалекта БД (skygate)

## Зачем

`v1.5.4` вернула SQLite как первый класс, но слой `internal/db/*_postgres.go`
остался «шимом» от `v1.3.0` (комментарии «skygate is PG-only» до сих пор в
`now_unix.go`, `placeholders.go`, `on_conflict.go`). В результате один и тот же
общий файл (`queries.go`, `secrets.go`) содержит SQL, который на одном из
бэкендов падает. Задача скилла — находить это **до** того, как оператор увидит
`http.Error` вместо страницы.

## Быстрый аудит (копировать целиком)

```powershell
Set-Location C:\Projects\skygate

# 1. PG-only конструкции в общих файлах (вне migrations_pg.go и dialect.go)
Get-ChildItem -Recurse -Include *.go -Path internal,cmd |
  Where-Object { $_.Name -notlike '*_test.go' -and $_.Name -notlike 'migrations_pg.go' } |
  Select-String -Pattern 'EXTRACT\(EPOCH|EXTRACT\(epoch|strftime\(|::bigint|::text|::jsonb|TEXT\[\]|ANY\(\$1::|BIGSERIAL' |
  ForEach-Object { "$($_.Path.Replace('C:\Projects\skygate\','')):$($_.LineNumber): $($_.Line.Trim())" }

# 2. SQLite-era "?" плейсхолдеры вне internal/db (на PG дают 42601)
Get-ChildItem -Recurse -Include *.go -Path internal,cmd |
  Where-Object { $_.Name -notlike '*_test.go' -and $_.FullName -notlike '*\internal\db\*' } |
  Select-String -Pattern 'SELECT .*\?|INSERT INTO .*\?|UPDATE .*\?|DELETE FROM .*\?|VALUES \(.*\?|LIMIT \?'

# 3. Кто вообще ветвится по бэкенду (норма — единицы вызовов)
Get-ChildItem -Recurse -Include *.go -Path internal,cmd |
  Where-Object { $_.Name -notlike '*_test.go' } |
  Select-String -Pattern 'BackendOf\(|\.IsSQLite\(\)|\.IsPostgres\(\)'
```

## Проверка схемы SQLite «руками» (обязательно после правки миграций)

`PRAGMA table_info` — единственный надёжный способ: `ALTER TABLE … ADD COLUMN
IF NOT EXISTS` в SQLite **невалиден**, а код в `migrations_sqlite.go` глотает
ошибку (`d.Exec(q) // ignore errors`, `if err != nil { continue }`), поэтому
колонка молча не появляется.

Временный тест (создать, запустить, удалить):

```go
// internal/db/zz_tmp_probe_test.go
package db
import "testing"
func TestZZProbe(t *testing.T) {
    d, sqlDB, err := OpenWithDialect(":memory:")
    if err != nil { t.Fatal(err) }
    defer sqlDB.Close()
    if err := ApplyMigrations(sqlDB, d.Kind); err != nil { t.Fatal(err) }
    var maxV int
    _ = sqlDB.QueryRow("SELECT COALESCE(MAX(version),0) FROM applied_migrations").Scan(&maxV)
    t.Logf("SQLITE max migration = %d (PG должно совпадать)", maxV)
    _, err = sqlDB.QueryRow("SELECT " + nowUnixSQL()).Scan(new(string))
    t.Logf("nowUnixSQL()=%q err=%v", nowUnixSQL(), err)   // на SQLite должен работать!
    rows, _ := sqlDB.Query("PRAGMA table_info(exit_servers)")
    defer rows.Close()
    for rows.Next() {
        var cid, notnull, pk int; var name, ctype string; var dflt any
        _ = rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk)
        t.Logf("exit_servers.%s %s", name, ctype)
    }
}
```

```powershell
go test ./internal/db/ -run TestZZProbe -count=1 -v
Remove-Item internal/db/zz_tmp_probe_test.go   # не забыть!
```

## Известные на 2026-09-17 дефекты (эталон для сравнения)

| Место | Что не так |
|---|---|
| `now_unix.go:8` + `now_unix_postgres.go:4` | `nowUnixSQL()` **всегда** отдаёт `EXTRACT(EPOCH FROM now())::bigint` → на SQLite `near "FROM": syntax error` |
| `queries.go:570,571` | `tagged_at = ` + `nowUnix` → падает на SQLite |
| `queries.go:616`, `secrets.go:159,168` | `strftime('%s','now')` → падает на PG |
| `node_owner_map.go:806-809` | `?` + `strftime` → падает на PG, ошибка глотается на `:783-785` |
| `headscale_version/monitor.go:278-283` | `LIMIT ?` → падает на PG, глотается на `:211` |
| `migrations_sqlite.go:105,141,142,222` | `ADD COLUMN IF NOT EXISTS` + глотание → колонок нет |
| `migrations_sqlite.go:890-896` | `information_schema.columns` в SQLite-цепочке, ошибка глотается |
| `mesh/cleanup.go:127` | `ANY($1::bigint[])` → падает на SQLite |
| `nodeownership/auto.go:341`, `telegram/commands_phase3.go:182` | `EXTRACT(...)` → падает на SQLite |

## Правила правки

1. Плейсхолдеры — **только** `db.PlaceholdersList` / `PlaceholdersRange` /
   `PlaceholderAt`. `$N` работает на обоих драйверах (modernc матчит `$NNN` по
   ordinal), `?` — фатален для PG.
2. «Сейчас» — **только** `db.NowUnixSQL()`/`nowUnixSQL()`, никогда не литерал.
3. Никогда не глотать ошибку миграции/запроса «на всякий случай». Для
   идемпотентности — проверка через `PRAGMA table_info` (SQLite) /
   `information_schema.columns` (PG), а не `ignore errors`.
4. Новая миграция = пара `migrateVxxxPG` + `migrateVxxxSQLite` +
   запись в `pgMigrations` **и** `sqliteMigrations`.
5. PG-only подсистемы (`cluster*`, `pgmigrate`, `ha`, `elector`, `dbmigrate`)
   на SQLite должны возвращать явную ошибку, а не падать в середине запроса.

## Проверка результата

```powershell
go build ./... ; go vet ./...          # vet сейчас красный — см. derp.go:810
go test ./internal/db/... -count=1
go test ./... -count=1                 # PG-тесты скипнутся без SKYGATE_TEST_PG_DSN
```
