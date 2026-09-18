---
name: skygate-error-ux-migration
description: Переводит обработчики skygate с сырых http.Error (text/plain страница) на flash-сообщения в вёрстке и безопасный рендер ошибок. Использовать при жалобе «ошибка БД показывается отдельной страницей» и при добавлении новых POST-форм.
whenToUse: Ошибка БД/валидации показывается текстом вместо сообщения; добавление нового POST-хендлера или формы; ревизия обработки ошибок.
---

# Миграция обработки ошибок в skygate

## Диагноз

Общего механизма рендера ошибок **нет**:

* `renderError` / `serverError` / `ErrorPage` — 0 совпадений в боевом коде
* `internal/handlers/templates/` — нет `error.html`
* `cmd/skygate/main.go` — нет recover-мидлвари (единственные `recover()` —
  в cron/мониторах: `internal/derphealth/cron.go:67`,
  `internal/headscale/reconcile_cron.go:116`,
  `internal/feature/admin/derp_cert_sync.go:160`,
  `internal/deployrun/framework.go:109`)
* `internal/middleware/middleware.go` — только `RequireAuth` (проверка наличия
  cookie, без валидации JWT)

Есть ровно две поверхности ошибки:

| Поверхность | Как выглядит | Когда годится |
|---|---|---|
| `http.Error(w, msg, code)` | **text/plain, без вёрстки** — «отдельная страница» | только для JSON/API-роутов (да и там лучше явный JSON) |
| `http.Redirect(w, r, "<page>?err=…")` + `{{.FlashError}}` | сообщение внутри страницы | все HTML-формы |

Масштаб: **482 вызова `http.Error` в 68 файлах**, из них 136 с кодом 500.

## Правило

> Любой хендлер, отвечающий HTML, при ошибке обязан **редиректить на
> исходную страницу со `?err=`**, а не писать `http.Error`.

Текст ошибки пользователю не показывать вообще: в `log` + `audit_log` — полный
`err`, в UI — i18n-ключ.

## Как сделать (на примере `/my/keys`)

`internal/feature/my/keys.go:63-66` сейчас:

```go
rows, err := db.ListPreauthKeysByUser(s.dbc(), c.UserID)
if err != nil {
    http.Error(w, err.Error(), http.StatusInternalServerError)   // ← сырая страница
    return
}
```

Должно быть:

```go
rows, err := db.ListPreauthKeysByUser(s.dbc(), c.UserID)
if err != nil {
    log.Printf("web.my.keys: ListPreauthKeysByUser userID=%d err=%v", c.UserID, err)
    // GET — рендерим ту же страницу с сообщением; POST — редирект с ?err=
    s.renderKeysPage(w, r, c, flashErr(s.I18n.T(lang, "error.db")))
    return
}
```

И в `internal/handlers/templates/user/keys.html` добавить блок, которого там
**нет вообще**:

```html
{{if .FlashError}}<div class="alert alert-danger">{{.FlashError}}</div>{{end}}
{{if .FlashOk}}<div class="alert alert-success">{{.FlashOk}}</div>{{end}}
```

Образец работающей связки: `internal/feature/my/devices.go:732-733` (читает
`?ok`/`?err`) + `templates/user/devices.html`.

## Приоритетный список (начинать отсюда)

| Страница | Место | Комментарий |
|---|---|---|
| `GET /my/keys` | `my/keys.go:65` | в шаблоне нет блока ошибки — сырая страница безальтернативна |
| `POST /my/preauth` | `my/preauth.go:71,76,102` | `:71,76` — ошибка БД **отбрасывается** и подменяется ложным «no headscale user linked» |
| `POST /my/keys/{id}/expire` | `keys.go:536,541,552,571,584,594` | |
| `POST /my/keys/{id}/reissue` | `keys.go:350…427` (9 мест) | |
| `POST /my/keys/cleanup` | `keys.go:634` | |
| `GET /admin/exit-nodes` | `admin/exit_nodes.go:147` | на SQLite сюда прилетает ошибка отсутствующих колонок |
| Add/Delete/Tag/UseTS-IP/AcceptRoutes | `admin/exit_nodes.go:503,525,598,737,869,906,954` | |
| `POST /admin/users` (создание) | `admin/users.go:130-170` | на странице **уже есть** `{{.FlashError}}` (`users.html:70-74`) — хендлер просто им не пользуется |
| DB-ветка правил exit | `exit_rules/form_my.go:859`, `form_admin.go:872` | валидационные ветки в тех же функциях уже редиректят с `?err=` |
| `POST /login` (rate limit) | `internal/middleware/ratelimit.go:26-31` | 429 возвращает text/plain вместо сообщения на форме логина |
| подсети пользователя | `admin/user_subnet.go:212-555` (~18 мест) | все вызываются формами `user_subnet.html` |

## Отдельный класс: JSON-эндпоинт с HTML-ответом

`admin/exit_nodes.go:612,616` пишут `http.Error(w, `{"error":…}`, …)` — но
`http.Error` **принудительно** ставит `text/plain`, поэтому `fetch()`-путь
разбирает plain-text как JSON и показывает общее «sync error». Для
JS-эндпоинтов — `w.Header().Set("Content-Type","application/json")` **до**
записи тела и `json.NewEncoder`. Аналогично `admin/derp_dashboard.go:331-332`
ставит заголовок **после** `http.Error` (no-op).

## i18n

* нет общего ключа «ошибка БД» → добавить `error.db` (RU+EN) в
  `internal/i18n/catalog_common.go`
* `error.internal` / `error.try_again` (`catalog_common.go:131-135`) —
  **мертвы**, не упоминаются ни в Go, ни в шаблонах. Либо задействовать, либо
  удалить.
* `keys.reissue_err_used` / `keys.reissue_err_expired` есть, но
  `keys.go:373,381` пишут английский текст мимо каталога.
* захардкоженный русский в коде: `admin/tailscale.go` — 43 литерала,
  `admin/telegram.go` — 37. При EN-локали оператор видит русский.

## Гейты (чтобы класс не вернулся)

1. Grep-контракт `scripts/check_b*.sh`: в файлах, обслуживающих HTML, нет
   `http.Error(w, err.Error()` (allow-list — JSON-роуты).
2. Тест на каждый ключевой хендлер: «БД падает ⇒ ответ не text/plain и
   содержит сообщение». Сейчас таких тестов **нет вообще** — `my/preauth_test.go`
   покрывает только чистые хелперы.
3. Тест, что `PostMyPreauth` **не** подавляет ошибку `InsertPreauthKey`
   (`my/preauth.go:130-132`).

## Не забыть про подавление ошибок (важнее, чем вид страницы)

Сырая страница — это плохо, но **тихий успех хуже**. Эталон:

```go
// my/preauth.go:130-136 — ошибка только логируется, пользователь видит успех
if _, err := db.InsertPreauthKey(...); err != nil {
    log.Printf("web.my.preauth: InsertPreauthKey userID=%d err=%v", c.UserID, err)
}
```

Последствие: ключ создан в headscale, но строки в `preauth_keys` нет →
устройство никогда не сопоставится с пользователем и вечно висит «⏳ pending».
Тот же паттерн ещё в трёх местах: `my/keys.go:445-448`,
`my/devices.go:1487-1489`, `telegram/commands_user.go:749-751`.
Правильно: транзакция + при неудаче компенсирующее `ExpirePreauthKey` в
headscale и flash-ошибка пользователю.
