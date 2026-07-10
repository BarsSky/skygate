# План рефакторинга и чистки проекта skygate

**Дата:** 2026-07-08
**Версия:** 0.5.0 → 0.6.0
**Объём:** 5615 строк Go + 3035 строк HTML + 23 .bak файла
**Repo:** github.com/BarsSky/skygate

---

## 0. Резюме

Главные проблемы по убыванию риска:
1. **21 .bak-файл в git (9354 строки)** — мёртвый код
2. **god-object `handlers.go` (1750 строк, 47 функций)** — 9 concern'ов
3. **god-object `exit_rules.go` (1749 строк, 23 функции)**
4. **`headscale.go` (749 строк, 27 функций)** — 4 ответственности
5. **Inline-CSS в `layout.html` (365 строк, 4 темы)**
6. **Дублирование** — 10× `INSERT INTO exit_rule_logs`, 23× `http.Error`
7. **Hardcoded SQL/пути** — 57 SQL-строк, 11 путей
8. **Тесты** — отсутствуют

Принципы: ничего не удаляем в первом проходе, только выносим. Каждый этап = коммит + push + smoke. Backup БД перед каждым этапом с SQL/handlers.

---

## Этап 0. Пре-условия (1ч)

- [ ] Backup: `cp /data/skygate.db /data/skygate.db.pre-refactor`
- [ ] Скриншот `/my/exit-rules` + `/admin/exit-nodes`
- [ ] Создать `scripts/smoke.sh` (см. Этап 5)
- [ ] Smoke-тест: login → /my/devices → /my/exit-rules → /admin/exit-nodes → /admin/exit-nodes/sync → проверить 3 exit-node через headscale API → /admin/backup/save
- [ ] `git checkout -b refactor/v0.6.0` от main (текущий HEAD = 440a1ab)

---

## Этап 1. Чистка .bak (30м, low)

**Проблема:** 21 файл `*.bak*` в репо (9354 строк — 60% от всего Go-кода!).

**Действия:**
1. `find . -name "*.bak*" -not -path "./.git/*" -delete`
2. `git ls-files | grep '\.bak' | xargs git rm`
3. `scripts/cleanup-baks.sh` на будущее
4. Commit: `chore: remove 21 obsolete .bak files (9354 lines) from repo`
5. Build + restart + smoke

**Откат:** `git revert HEAD`

---

## Этап 2. Layout скриптов/деплоя (1ч, low)

**Проблема:** `scripts/`, `deploy/`, `docs/`, `backup/`, `data/` — пустые/дублирующиеся.

**Действия:**
1. `data/` и `backup/` — удалить (пустые)
2. `scripts/test.sh` → `scripts/smoke.sh`
3. `docs/SYNC.md` → `docs/agent-knaga-workflow.md`
4. Commit: `chore: organize scripts/deploy/docs layout`

---

## Этап 3. Извлечь inline-CSS из layout.html (1.5ч, low)

**Проблема:** `internal/handlers/templates/layout.html` содержит ~300 строк CSS для 4 тем.

**Действия:**
1. Создать `static/css/themes.css` (содержимое из `<style>`)
2. Заменить `<style>...</style>` на `<link rel="stylesheet" href="/static/css/themes.css">`
3. Создать `static/css/skygate.css` для базовых layout-стилей
4. Commit: `refactor: extract themes.css from layout.html to static/css/`
5. Visual smoke во всех 4 темах

---

## Этап 4. Декомпозиция handlers.go (4-6ч, medium)

**Проблема:** 1750 строк, 47 функций, 9 concern'ов.

**Целевая структура:**
```
internal/handlers/
├── app.go              // struct App, New(), render, renderWithLayout, currentUser, audit
├── auth.go             // GetLogin/PostLogin/PostLogout/PostSettingsTheme
├── user.go             // GetDashboard, GetMyKeys, PostMyKeyExpire, GetMyDevices, PostMyPreauth
├── admin_users.go      // GetAdminUsers, PostAdminUser, PostAdminDeleteUser
├── admin_nodes.go      // GetAdminDevices, PostAdminNodeTag, PostAdminNodeUntag
├── admin_acl.go        // GetAdminACLs, PostAdminRollbackACL
├── admin_derp.go       // GetAdminDERP + collectDerpStatus + parseDerper*
├── admin_metrics.go    // computeTailnetMetrics, countMyPreAuthKeys, backfillNodeOwnership
├── admin_audit.go      // GetAdminAudit
├── tokens.go           // GetMyTokens, PostMyToken, PostMyTokenRevoke
├── exit_nodes.go       // GetExitNodes (для пользователя)
├── exit_rules_api.go   // GetExitRulesAPI, PostExitRulesAPI, GetExitRulesAPIHelp
└── ... (остальные существующие)
```

**Алгоритм:**
- Шаг 4.1: app.go (1ч)
- Шаг 4.2: auth.go (1ч)
- Шаг 4.3: user.go (1.5ч)
- Шаг 4.4: admin/* (1.5ч)
- Шаг 4.5: tokens.go + exit_nodes.go (1ч)
- Шаг 4.6: build + restart + smoke (30м)

**Правила:** только cut+paste+`package handlers`. Никаких rename/optim. Отдельный commit на каждый блок. Build+restart+smoke между шагами.

**Цель:** `handlers.go` ≤ 200 строк (только App + render + claims + audit + page-title).

---

## Этап 5. Smoke-test (30м, low)

**Файл: `scripts/smoke.sh`** — login + 5 страниц + sync + 3 exit-node verified + backup. Запускается за 5с.

**Файл: `scripts/check_exit_nodes.py`** — читает `HS_TOKEN` env, проверяет все 3 ноды на 0.0.0.0/0=True и ::/0=True.

**Файл: `Makefile`**:
```makefile
.PHONY: build run test smoke
build: ; GOTOOLCHAIN=local go build -o ./skygate ./cmd/skygate
run: build; ./skygate
test: smoke
smoke: ; bash scripts/smoke.sh
```

**Commit:** `test: add scripts/smoke.sh + scripts/check_exit_nodes.py + Makefile`

---

## Этап 6. Извлечь route-scripts в свой пакет (3-4ч, medium)

**Проблема:** `GenerateRouteSetupScript` — 300 строк inline-шелла в `exit_rules.go:214-512`. 200 строк `case "windows"` + 100 строк `default // linux / mac`.

**Целевая структура:**
```
internal/routescript/
├── routescript.go
└── templates/
    ├── windows_apply.tmpl
    ├── windows_restore.tmpl
    ├── linux_apply.tmpl
    ├── linux_restore.tmpl
    ├── macos_apply.tmpl
    └── macos_restore.tmpl
```

**Действия:**
1. Скопировать содержимое каждой `case` в `.tmpl` с `{{.ExitNodeIP}}`, `{{range .Routes}}...{{end}}`
2. `type OS string; const (Windows OS = "windows"; Linux OS = "linux"; MacOS OS = "macos")`
3. `type Input struct { ExitNodeIP, ExitNodeName string; Routes []Route; Restore bool }`
4. `func Generate(os OS, in Input) (string, error)`
5. `GenerateRouteSetupScript` сводится к 3 строкам

**Commit:** `refactor(exit_rules): extract inline-bash to internal/routescript package with .tmpl files`

---

## Этап 7. Декомпозиция headscale.go (4-6ч, medium-high)

**Проблема:** 749 строк, 27 функций, 4 ответственности (HTTP/SSH/CLI/models/cache).

**Целевая структура:**
```
internal/headscale/
├── client.go      // struct Client, New(), do() — base HTTP
├── api_users.go   // ListUsers, CreateUser, DeleteUser
├── api_keys.go    // CreatePreauthKey, ExpirePreauthKey
├── api_nodes.go   // ListAllNodes, ListExitNodes, TagNode, UntagNode, DeleteNode
├── api_acl.go     // GetACL, SetPolicy, InvalidateCache
├── ssh.go         // SetAdvertisedRoutes (SSH на karolina/emilia/sharlotta)
├── cli.go         // createPreauthViaCLI, ApproveAllRoutesWithList (docker exec headscale)
├── model.go       // HSUser, HSNode, NodeView, PreauthKey, DerpPeer + helpers
├── cache.go       // TTL cache
├── exits.go       // (existing, keep)
└── retry.go       // withRetry для transient 5xx
```

**Алгоритм:** один файл = один commit = один restart = один smoke.

**Главное:** НИЧЕГО не менять в логике, только move. `SetAdvertisedRoutes` — особенно осторожно (только что починили karolina-bug).

---

## Этап 8. Юнит-тесты (8-12ч, medium)

**Минимум (high value):**
1. `internal/auth/auth_test.go` — 5 тестов: generate/parse/expired/wrong-secret/empty-claims
2. `internal/headscale/model_test.go` — toView(), hasExitNodeTag() (включая баг karolina 0.0.0.0/0)
3. `internal/headscale/headscale_test.go` (httptest) — ListUsers, ListAllNodes, SetAdvertisedRoutes (mock SSH)
4. `internal/routescript/routescript_test.go` — golden file tests для 6 комбинаций
5. `internal/handlers/exit_rules_test.go` — GenerateACL: пустые/1/100 правил/дубликаты/domain→IP
6. `internal/db/db_test.go` — миграции v_X → v_{X+1}

**Цель:** покрытие ~40%. Стек: `testing`, `httptest`, `mattn/go-sqlite3`.

---

## Этап 9. SQL в internal/db (3-4ч, medium)

**Проблема:** 57 SQL-строк, разбросанных по handlers. 10× `a.DB.Exec("INSERT INTO exit_rule_logs ...")`.

**Целевая структура:**
```
internal/db/
├── db.go
├── migrations.go
├── queries.go          // const для всех SQL-строк
├── exit_rules.go       // ListExitRules, InsertRuleUnique, DeleteExitRuleByID
├── acl_snapshots.go    // SaveACLSnapshot, ListACLSnapshots, GetACLSnapshot, MarkACLApplied
├── exit_rule_logs.go   // AppendExitRuleLog (унифицированный helper)
├── users.go            // GetUserByID, GetUserByName, UpsertUser
└── nodes.go            // GetDeviceRulesForUser, GetUserDevices
```

**Helper:** `func AppendExitRuleLog(db *sql.DB, version int, action, detail string)` заменяет 10 inline-вызовов.

---

## Этап 10. Унифицировать error-handling (2-3ч, low)

**Проблема:** 23× `http.Error(w, "...", code)` + 47× `log.Printf`. Каждое ad-hoc.

**Решение:** `internal/httpx/error.go`:
```go
func BadRequest(w, msg); Unauthorized(w); Forbidden(w); NotFound(w)
func ServerError(w, err); JSON(w, code, body)
```

---

## Этап 11. Документация (2-3ч, low)

1. `README.md` — переписать
2. `docs/architecture.md` — диаграмма слоёв
3. `docs/db-schema.md` — все таблицы
4. `docs/api.md` — все endpoints + curl примеры
5. `docs/deploy.md` — как накатить
6. `CHANGELOG.md` — секции по версиям

---

## Этап 12. Финальная валидация (1ч)

- [ ] `go vet ./...` — zero issues
- [ ] `go test ./...` — green
- [ ] `make smoke` — green
- [ ] `make build` — green
- [ ] backup → restore на чистый контейнер → работает
- [ ] Все 4 темы визуально ок
- [ ] Tag v0.6.0 + push (github + synology)

---

## Оценка

| Этап | Время | Риск | Impact |
|------|-------|------|--------|
| 0. Пре-условия | 1ч | - | - |
| 1. .bak cleanup | 30м | low | -9354 строк, repo -15% |
| 2. Layout | 1ч | low | - |
| 3. CSS extract | 1.5ч | low | кешируемость |
| 4. handlers split | 4-6ч | medium | god-object → 12 файлов |
| 5. smoke.sh | 30м | low | тест за 5с |
| 6. routescript | 3-4ч | medium | bash в .tmpl |
| 7. headscale split | 4-6ч | medium-high | god-object → 9 файлов |
| 8. unit tests | 8-12ч | medium | покрытие ~40% |
| 9. db layer | 3-4ч | medium | 57 SQL → typed |
| 10. httpx | 2-3ч | low | consistency |
| 11. docs | 2-3ч | low | discoverability |
| 12. release | 1ч | - | v0.6.0 |
| **Итого** | **30-44ч** | | |

**MVP (80% value, 7-9ч):** этапы 0, 1, 3, 4, 5

**Стратегия:**
- Этап = сессия = коммит = push
- После каждого этапа — smoke
- Если упал — `git revert HEAD`, не разбираться
- Сначала low-risk (1, 2, 3, 5, 10, 11), потом medium (4, 6, 7, 9), тесты (8) в конце
- После больших этапов — синхронизация с пользователем

---

## Подводные камни

1. **SetAdvertisedRoutes** (только что починили karolina) — НЕ трогать в Этапе 7
2. **GenerateACL** — вызывается из 4 мест, после move все 4 должны работать
3. **exit_rule_logs** — схема не менять (старые логи не прочитаются)
4. **go:embed** — templates/ должна быть в той же директории, что templates.go
5. **Static serving** — путь в layout.html должен быть абсолютным `/static/css/...`
6. **Backup/restore** — тестировать на отдельном контейнере перед релизом

---

## Out of scope

- PostgreSQL (отдельный проект)
- React/Vue вместо html/template (overkill)
- Миграция с go-shadowsocks (не наша зона)
- 80%+ test coverage (40% достаточно)
- Оптимизация hot paths (нет профайлинга)
- Hexagonal/DDD архитектура (overkill)
