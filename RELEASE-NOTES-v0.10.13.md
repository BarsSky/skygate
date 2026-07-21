# v0.10.13 — /lang в меню бота + lazy tag backfill + DERP page i18n

**Release date**: 2026-07-15
**Previous release**: v0.10.12

## What's in this release

A bug-fix release that closes three issues found in the
v0.10.12 post-review:

### Fixed

- **`base` device shows `tag:untagged` in the bot but
  `tag:private` in headscale.** The admin-tag path in
  `PostAdminNodeTag` had a guard `origUserName != "tagged-devices"`
  that skipped the `node_owner_map` row update when headscale
  had reassigned the node to the synthetic `tagged-devices`
  user (which happens automatically whenever a tag is applied
  in headscale). The headscale side got the new tag, but the
  portal's snapshot row stayed at `tag:untagged` — so the bot's
  `/nodes` and `/my_nodes` showed the wrong tag. **Source
  fix**: `PostAdminNodeTag` now uses the new
  `db.UpdateNodeOwnerTag` helper to update the tag in place
  while preserving the original portal-side owner when
  `origUserName == "tagged-devices"`. **Defensive read fix**:
  the bot's `/my_nodes` and `/nodes` now do a lazy tag sync
  via `db.SyncTagsFromHeadscale` — if any visible row's DB
  tag disagrees with the live headscale tag, the row is
  updated before the reply is rendered. The first `/my_nodes`
  or `/nodes` after upgrade self-heals any rows that were
  left stale by the old bug.

- **`/lang` was missing from the bot menu.** v0.10.12's menu
  spec had `help`, `version`, then went straight to the
  user-scope `my_*` commands. `/lang` (the per-chat language
  switch) was in the dispatcher but not in the menu, so
  users had to type it from memory. v0.10.13 adds it to
  `DefaultMyCommandsSpec.Common` between `/version` and
  `/my_status`; the description is the new
  `bot.menu.lang.description` key in both `ru` and `en`
  catalogs ("Язык чата: показать или переключить" /
  "Chat language: show or switch").

- **DERP admin page (`/admin/derp`) had untranslated English
  strings.** The page had hardcoded English in the
  `Service status` / `Traffic` / `Active connections` /
  `Activity log` / `Service` / `Help` sections, including the
  "Why a client may not use this DERP" explainer and the
  `force-prefer-derp` / `node-attrs` / Android-iOS pin
  snippets. v0.10.13 lifts every visible string into the
  i18n catalog (40 new `derp.*` keys) and the template now
  uses `{{t "..."}}` / `{{tf "..." arg}}` with a new
  `safeHTML` template function for the two paragraphs that
  contain `<code>` / `<b>` markup. RU-locale users see the
  full page in Russian.

### Migration guide

From v0.10.12 → v0.10.13:

1. **Pull and rebuild** (operator-driven, no auto-update):
   ```bash
   cd /home/skyadmin/skygate
   git pull
   docker compose restart skygate
   ```
2. **No DB migrations.** v0.10.13 is catalog + Go code + a
   new template function. Existing data is preserved; the
   base-device fix in the commit log shows the one operator
   action (an `UPDATE node_owner_map SET tag = 'tag:private'`
   for the affected node) that the lazy backfill would
   otherwise have done on the next `/my_nodes` call.
3. **Bot menu refreshes on the first restart** — the new
   `setMyCommandsAll` call re-registers all three scopes
   (default / en / ru) including the new `/lang` entry. Users
   see the updated menu in their Telegram app within ~60 s.
4. **DERP page is now translated** — the moment the new
   build is up, switch the chat language in the bottom-right
   of the page (or via `?lang=ru` on the URL) to see the
   Russian version.

### Verification

- 12/12 packages green (`go test ./...`)
- Unit tests for the new helpers (`db.AnyTagStale`,
  `db.SyncTagsFromHeadscale`, `db.UpdateNodeOwnerTag`,
  `hostnameMapFromHeadfill` reuse for tag map,
  `TestMyNodesReply_LazyTagBackfillStaleRow`,
  `TestMyNodesReply_NoTagBackfillWhenMatching`) all PASS
- Smoke 118/118 PASS on VM (`make test`) — `[ru] 59 + [en] 59`
- Build: `go build ./...` clean
- Templates: `safeHTML` registered in the funcmap; all
  `{{t "..." | safeHTML}}` and `{{tf "..." n | safeHTML}}`
  calls parse and render

### Files changed

| File | What |
|---|---|
| `internal/db/node_owner_map.go` | new `UpdateNodeOwnerTag`, `AnyTagStale`, `SyncTagsFromHeadscale` helpers; full doc comments |
| `internal/db/node_owner_map_test.go` | 4 new tests (tag stale detection, sync update, admin-override preservation, no-op on empty map) + 2 for `UpdateNodeOwnerTag` |
| `internal/handlers/handlers_admin_nodes.go` | `PostAdminNodeTag` now uses `UpdateNodeOwnerTag` when `origUserName == "tagged-devices"`; the old `origUserName != "tagged-devices"` guard is gone |
| `internal/handlers/templates.go` | new `safeHTML` template func |
| `internal/handlers/templates/admin/derp.html` | every visible string replaced with `{{t "derp.*"}}` / `{{tf "derp.*" n}}`; HTML markup uses `safeHTML` |
| `internal/i18n/catalog.go` | 40 new `derp.*` keys (ru + en) + 2 new `bot.menu.lang.description` keys |
| `internal/telegram/commands_set.go` | `/lang` added to `DefaultMyCommandsSpec.Common` |
| `internal/telegram/commands_user.go` | `myNodesReply` does hostname + tag lazy backfill in one headscale round-trip |
| `internal/telegram/commands_phase2.go` | `nodesReply` (admin view) does the same |
| `internal/telegram/commands_test.go` | 2 new bot tests for tag backfill (stale → updated; matching → no-op) |

## RU

# v0.10.13 — /lang в меню бота + lazy tag backfill + DERP page i18n

**Дата релиза**: 2026-07-15
**Предыдущий релиз**: v0.10.12

## Что в этом релизе

Баг-фикс релиз, закрывающий три проблемы, найденные в
обзоре после v0.10.12:

### Исправлено

- **Устройство `base` показывает `tag:untagged` в боте, но
  `tag:private` в headscale.** В `PostAdminNodeTag` стоял
  guard `origUserName != "tagged-devices"`, который
  пропускал обновление `node_owner_map`, когда headscale
  переназначал ноду синтетическому юзеру `tagged-devices`
  (это происходит автоматически при применении любого
  тега в headscale). На стороне headscale новый тег
  появлялся, но snapshot-строка портала оставалась
  `tag:untagged` — и бот `/nodes` и `/my_nodes` показывал
  неверный тег. **Source fix**: `PostAdminNodeTag` теперь
  использует новый `db.UpdateNodeOwnerTag`, который
  обновляет тег на месте, сохраняя оригинального
  portal-юзера, когда `origUserName == "tagged-devices"`.
  **Defensive read fix**: бот `/my_nodes` и `/nodes` теперь
  делает lazy tag sync через `db.SyncTagsFromHeadscale` —
  если в любой видимой строке DB-тег расходится с
  headscale-тегом, строка обновляется до рендера ответа.
  Первый `/my_nodes` или `/nodes` после апгрейда лечит
  любые строки, оставшиеся stale старым багом.

- **`/lang` отсутствовал в меню бота.** v0.10.12 в спеке
  меню шёл `help`, `version`, сразу за ним user-scope
  `my_*` команды. `/lang` (переключатель языка чата) был
  в диспетчере, но не в меню — пользователям приходилось
  набирать его по памяти. v0.10.13 добавляет его в
  `DefaultMyCommandsSpec.Common` между `/version` и
  `/my_status`; описание — новый ключ
  `bot.menu.lang.description` в RU- и EN-каталогах.

- **Страница DERP (`/admin/derp`) имела непереведённые
  английские строки.** Страница содержала жёсткий EN в
  секциях `Service status` / `Traffic` / `Active connections`
  / `Activity log` / `Service` / `Help`, включая объяснение
  «Why a client may not use this DERP» и сниппеты
  `force-prefer-derp` / `node-attrs` / Android-iOS. v0.10.13
  поднимает каждую видимую строку в i18n-каталог (40
  новых `derp.*` ключей), а шаблон теперь использует
  `{{t "..."}}` / `{{tf "..." arg}}` с новой функцией
  `safeHTML` для двух абзацев, содержащих разметку
  `<code>` / `<b>`. RU-локаль видит всю страницу на
  русском.

### Гайд по миграции

С v0.10.12 → v0.10.13:

1. **Подтянуть и пересобрать** (вручную, без
   авто-обновления):
   ```bash
   cd /home/skyadmin/skygate
   git pull
   docker compose restart skygate
   ```
2. **Миграций БД нет.** v0.10.13 — каталог + Go-код + новая
   функция шаблона. Существующие данные сохранены; фикс
   `base` в commit log показывает единственное действие
   оператора (`UPDATE node_owner_map SET tag = 'tag:private'`
   для затронутой ноды), которое иначе lazy backfill сделал
   бы при следующем `/my_nodes`.
3. **Меню бота обновляется при первом рестарте** — новый
   вызов `setMyCommandsAll` перерегистрирует все три
   области (default / en / ru) включая `/lang`. Пользователи
   увидят обновлённое меню в Telegram-приложении в
   течение ~60 с.
4. **DERP-страница теперь переведена** — как только новый
   билд встанет, переключите язык чата в правом-нижнем углу
   страницы (или через `?lang=ru` в URL) — увидите русскую
   версию.

### Проверка

- 12/12 пакетов зелёные (`go test ./...`)
- Unit-тесты новых хелперов (`db.AnyTagStale`,
  `db.SyncTagsFromHeadscale`, `db.UpdateNodeOwnerTag`,
  переиспользование `hostnameMapFromHeadfill` для tag map,
  `TestMyNodesReply_LazyTagBackfillStaleRow`,
  `TestMyNodesReply_NoTagBackfillWhenMatching`) — все PASS
- Smoke 118/118 PASS на VM (`make test`) — `[ru] 59 + [en] 59`
- Сборка: `go build ./...` чисто
- Шаблоны: `safeHTML` зарегистрирован в funcmap; все вызовы
  `{{t "..." | safeHTML}}` и `{{tf "..." n | safeHTML}}`
  парсятся и рендерятся корректно

### Изменённые файлы

| Файл | Что |
|---|---|
| `internal/db/node_owner_map.go` | новые хелперы `UpdateNodeOwnerTag`, `AnyTagStale`, `SyncTagsFromHeadscale`; doc-комментарии |
| `internal/db/node_owner_map_test.go` | 4 новых теста (tag stale detection, sync update, сохранение admin-override, no-op на пустой map) + 2 для `UpdateNodeOwnerTag` |
| `internal/handlers/handlers_admin_nodes.go` | `PostAdminNodeTag` теперь использует `UpdateNodeOwnerTag` для `tagged-devices`; старый guard убран |
| `internal/handlers/templates.go` | новая функция `safeHTML` в funcmap |
| `internal/handlers/templates/admin/derp.html` | каждая видимая строка заменена на `{{t "derp.*"}}` / `{{tf "derp.*" n}}`; HTML-разметка через `safeHTML` |
| `internal/i18n/catalog.go` | 40 новых `derp.*` ключей (ru + en) + 2 новых `bot.menu.lang.description` |
| `internal/telegram/commands_set.go` | `/lang` добавлен в `DefaultMyCommandsSpec.Common` |
| `internal/telegram/commands_user.go` | `myNodesReply` делает hostname + tag lazy backfill в одном headscale-запросе |
| `internal/telegram/commands_phase2.go` | `nodesReply` (admin) делает то же |
| `internal/telegram/commands_test.go` | 2 новых теста бота для tag backfill (stale → updated; matching → no-op) |
