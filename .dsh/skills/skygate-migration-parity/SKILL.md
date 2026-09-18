---
name: skygate-migration-parity
description: Сверяет цепочки миграций PostgreSQL и SQLite в skygate — версии, функции, колонки и таблицы. Обязательный шаг перед любой правкой migrations_*.go и перед переключением бэкенда БД.
whenToUse: Добавление/правка миграции; расхождение схем SQLite и PG; ошибки «no such column» / «relation does not exist».
---

# Паритет миграций PG ⇄ SQLite (skygate)

## Зачем

Цепочки ведутся вручную в двух местах и **уже разошлись** (проверено на
`6fff2e25`):

* `internal/db/driver_postgres.go:106-157` → 49 записей, максимум **V071**
* `internal/db/driver_sqlite.go:47-97` → 48 записей, максимум **V070**
* `migrateV071SQLite` **не существует** (`migrations_v0_71_derp_cert_sync.go:72`
  содержит только PG-вариант) → таблицы `derp_cert_sync` в SQLite нет никогда.

## Проверка №1 — списки версий

```powershell
Set-Location C:\Projects\skygate
$pg = Select-String -Path internal\db\migrations_pg.go -Pattern 'func migrateV(\w+?)PG\(' -AllMatches |
      ForEach-Object { $_.Matches[0].Groups[1].Value }
$sq = Select-String -Path internal\db\migrations_sqlite.go -Pattern 'func migrateV(\w+?)SQLite\(' -AllMatches |
      ForEach-Object { $_.Matches[0].Groups[1].Value }
"PG=$($pg.Count) SQLite=$($sq.Count)"
"только в PG:      " + ((Compare-Object $pg $sq | Where-Object SideIndicator -eq '<=' | ForEach-Object InputObject) -join ' ')
"только в SQLite:  " + ((Compare-Object $pg $sq | Where-Object SideIndicator -eq '=>' | ForEach-Object InputObject) -join ' ')
```

Ожидаемый результат — обе строки пустые.

## Проверка №2 — записи в таблицах-реестрах

```powershell
$pgv = (Select-String -Path internal\db\driver_postgres.go -Pattern '^\s*\{(\d+),' -AllMatches |
       ForEach-Object { $_.Matches } | ForEach-Object { [int]$_.Groups[1].Value }) | Sort-Object -Unique
$sqv = (Select-String -Path internal\db\driver_sqlite.go  -Pattern '^\s*\{(\d+),' -AllMatches |
       ForEach-Object { $_.Matches } | ForEach-Object { [int]$_.Groups[1].Value }) | Sort-Object -Unique
"PG versions:     " + ($pgv -join ',')
"SQLite versions: " + ($sqv -join ',')
"пропущено в SQLite: " + (((Compare-Object $pgv $sqv) | Where-Object SideIndicator -eq '<=' | ForEach-Object InputObject) -join ',')
```

## Проверка №3 — метаданные `SourceFile`

Третья колонка `MigrationEntry` должна указывать на **существующий** файл.
Уже есть дрейф: `driver_sqlite.go:96` ссылается на `migrations_v0_70_b236.go`,
которого в дереве нет (PG использует `migrations_v0_70_b238.go`).

```powershell
Select-String -Path internal\db\driver_*.go -Pattern '"migrations_[^"]+"' -AllMatches |
  ForEach-Object { $_.Matches } | ForEach-Object { $_.Groups[0].Value.Trim('"') } |
  Sort-Object -Unique | ForEach-Object { if (-not (Test-Path "internal\db\$_")) { "НЕТ ФАЙЛА: $_" } }
```

## Проверка №4 — фактическая схема

См. скилл `skygate-db-dialect-audit` (пробный тест с `PRAGMA table_info`).
Минимум, который обязан присутствовать в SQLite:

```
exit_servers:  ssh_target, ssh_key_path, accept_routes, ssh_port
device_rules:  device_ip, parent_domain, user_name, device_hostname
таблицы:       derp_cert_sync, derp_health, applied_migrations
```

## Идемпотентность вместо глотания ошибок

`migrations_sqlite.go` **нельзя** оставлять с `d.Exec(q) // ignore errors`:
SQLite не понимает `ALTER TABLE … ADD COLUMN IF NOT EXISTS`, поэтому «игнор»
превращается в «колонки нет». Правильный хелпер:

```go
// addColumnIfMissing: идемпотентность через каталог, а не через подавление ошибки.
func addColumnIfMissing(d *sql.DB, table, column, ddl string) error {
    switch BackendOf(d) {
    case BackendSQLite:
        rows, err := d.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
        // ... если column уже есть → return nil
        return exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, ddl))
    case BackendPostgres:
        return exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s", table, ddl))
    }
    return fmt.Errorf("addColumnIfMissing: unknown backend")
}
```

## Definition of done для новой миграции

- [ ] `migrateVxxxPG` и `migrateVxxxSQLite` существуют
- [ ] запись есть в `pgMigrations` **и** `sqliteMigrations`, `SourceFile` существует
- [ ] обе цепочки прогоняются на пустой БД без ошибок
- [ ] schema-parity проверка не показывает расхождений
- [ ] добавлен/обновлён `scripts/check_b*.sh` контракт
