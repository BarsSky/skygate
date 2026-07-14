# v0.10.12 — Lazy hostname backfill + i18n bot menu + RU/EN polish + Headplane/DERP external

**Release date**: 2026-07-15
**Previous release**: v0.10.11

## What's in this release

A maintenance + UX release that fixes two long-standing bot UX
bugs, polishes the Russian translations, and lays the
groundwork for "Skygate as a shell" (operator plugs in
existing Headscale / Headplane / DERP instead of running
the bundled stack).

### Fixed

- **`/my_nodes` and `/nodes` now show hostname, not bare node id**.
  Migration v0.34 added the `node_owner_map.hostname` column
  in v0.10.9, but `backfillNodeOwnership` only ran via
  `/admin/devices` — so any user who opened the bot before
  that page saw the bare-id view (`• 6`, `• 8`, `• 9` …)
  with no way to know which device was which. v0.10.12
  adds a **lazy backfill**: when `/my_nodes` or `/nodes`
  finds an empty hostname, it calls `hs.ListAllNodes()` and
  fills the empty cells in `node_owner_map`. The first call
  after upgrade self-heals; subsequent calls are a fast
  indexed read. (`internal/db/node_owner_map.go` +
  `internal/telegram/commands_user.go` +
  `commands_phase2.go`.)
- **Telegram bot menu is now per-language**. The
  `feature/telegram-bot-ux` worktree (v0.10.4) had a
  hardcoded-English `commands_set.go` that leaked English
  into RU-locale chats — the screenshot in the v0.10.12
  release notes shows `/help The Threshold's codex` even
  when the chat's language is Russian. v0.10.12 supersedes
  the worktree with an i18n-aware version: every menu entry
  references a `bot.menu.<cmd>.description` catalog key
  (`internal/i18n/catalog.go` adds 19 × 2 = 38 new keys),
  and `SetMyCommandsAll` registers one menu per language via
  the `language_code` parameter (Telegram Bot API 7.0+).
  RU-locale chats now see the menu in Russian.

### Changed

- **Full RU translation audit (~270 bot keys)**. The
  post-v0.10.11 review flagged the Russian text as rough,
  with leftover "gate / threshold / warden" metaphor from
  v0.10.4's gatekeeper voice that didn't survive the
  switch to the butler voice in v0.10.8. Every
  user-visible bot string is rewritten in a consistent
  butler register: **"Вы"** (formal "you") instead of the
  casual "ты" where appropriate, no leftover epic-fantasy
  metaphor, smoother Russian word order, "Формат:" instead
  of "использование:" for command syntax, and a new
  consistent "чат ещё не привязан к аккаунту skygate"
  not-bound message across every command. Sign-off changed
  to **"— Ваш Дворецкий"** / **"— Your butler"** (variant D
  from the four options the assistant proposed — fully in
  the butler role, no brand anchor in the sign-off itself;
  the brand anchor is the butler mark `🪶` at the top).
  Headers reworked: `Врата` → `Добро пожаловать`, `Свиток
  версии` → `Версия`, `Закрытая дверь` → `Дверь закрыта`,
  `Добавление` → `Готово — добавлено`, `Удаление` → `Готово
  — удалено`. Personality keys (`bot.personality.*`)
  re-keyed to the new voice (`⟡ Ваш Дворецкий`,
  `вы — хозяин своих устройств, я — при них`).
  EN keys also touched in places where the original phrasing
  had drifted (e.g. "Yours in service, the Warden of the
  Threshold" → "Your butler"). All changes are
  catalog-only — no Go code touched for the polish.

- **Headplane: existing-instance mode**. New
  `HEADPLANE_EXTERNAL_URL` env var (added to `.env.example`
  and `deploy/lib/env.sh`). When set, `deploy/deploy.sh`
  skips the `headplane` service block in the rendered
  `docker-compose.yml` and the readiness check; the
  `/admin/acls` view links to the existing URL instead of
  the bundled sidecar. `deploy/backup.sh` records
  `HEADPLANE_EXTERNAL_URL` in the inventory so a
  `deploy.sh --from-path` restore on another host
  reproduces the same wiring. See **"Use an existing
  Headplane"** in `docs/headplane.md` (new section). The
  bundled sidecar remains the default for backward compat.

- **DERP relay: external-URLs mode**. New
  `DERP_EXTERNAL_URLS` env var (comma-separated, same
  format as Tailscale's own `derp.urls`). When set, the
  URLs are appended to `headscale_config.derp.urls`
  alongside `HEADSCALE_DERP_URLS` and the bundled `derper`
  (if `DERP_ENABLED=true`). New **`docs/derp.md`** (6KB)
  documents the two modes + the "use both" combination +
  the `/admin/derp` health surface + the v0.11.0 web-UI
  follow-up. Web-UI config for DERP / Headplane is the
  next-step roadmap item (see below).

- **`docs/skygate-as-shell.md`** (8.4KB) — the architectural
  roadmap for v0.11.0 → v0.13.0. Captures the user's
  "skygate as autonomous shell" idea: operator plugs in
  existing Headscale / Headplane / DERP modules, Skygate
  provides the user-facing surface on top. v0.11.0 = the
  current v0.10.12 deploy-time toggles lifted into the
  web UI (`/admin/integrations` page + `global_settings`
  storage). v0.12.0 = pluggable headscale (multi-control
  plane per user). v0.13.0 = ACL import / export (B+C
  from the user's request). No code in v0.10.12; this is
  the planning artifact.

### Migration guide

From v0.10.11 → v0.10.12:

1. **Pull and rebuild** (operator-driven, no auto-update):
   ```bash
   cd /home/skyadmin/skygate
   git pull
   docker compose restart skygate
   ```
2. **No DB migrations.** v0.10.12 is catalog + Go code +
   deploy scripts only; the SQLite schema is unchanged.
   Existing `node_owner_map.hostname` rows are filled in
   on the first `/my_nodes` or `/nodes` after upgrade
   (lazy backfill — no operator action needed).
3. **No `.env` changes required** for the new env vars
   (`HEADPLANE_EXTERNAL_URL`, `DERP_EXTERNAL_URLS`); they
   default to empty, which preserves v0.10.11 behaviour.
4. **Signoff change is automatic** — every bot reply's
   footer line is now `— Ваш Дворецкий` / `— Your butler`
   (was `Искренне Ваш, Хранитель Порога` / `Yours in
   service, the Warden of the Threshold`).
5. **Telegram menu change is automatic** — first bot
   restart after upgrade re-registers the per-language
   menu via `setMyCommands`. Users see the new menu on
   their next app refresh (~60 s after the registration
   completes).

### Verification

- 12/12 packages green (`go test ./...`)
- 12/12 unit tests across all 10 internal packages PASS
  (the new lazy-backfill tests + the menu i18n tests are
  part of the count)
- Smoke 118/118 PASS on VM (`make test`)
- Build: `go build ./...` clean
- Shell scripts: `bash -n` clean on `deploy.sh`,
  `backup.sh`, `lib/env.sh`

### Files changed

| File | What |
|---|---|
| `internal/db/node_owner_map.go` | new `BackfillEmptyHostnames` + `AnyHostnameEmpty` helpers |
| `internal/db/node_owner_map_test.go` | tests for the new helpers (4 cases) |
| `internal/telegram/commands_user.go` | lazy backfill in `myNodesReply`; new `hostnameMapFromHeadscale` helper; headscale import |
| `internal/telegram/commands_phase2.go` | lazy backfill in `adminNodesReply` |
| `internal/telegram/commands_set.go` | **NEW** — i18n-aware `MyCommandsSpec` + `SetMyCommandsAll` orchestrator |
| `internal/telegram/commands_set_test.go` | **NEW** — coverage for every spec entry, language resolution, payload shape, end-to-end HTTP |
| `internal/telegram/commands_test.go` | new lazy-backfill tests; new `fakeNodeServer` helper |
| `internal/telegram/personality.go` | envelope example comment updated to new signoff |
| `internal/telegram/personality_test.go` | updated to match new headers / signoff / greeting text |
| `internal/i18n/catalog.go` | full RU audit (~270 `bot.*` keys); 19 × 2 new `bot.menu.*.description` keys; signoff + personality re-keyed |
| `internal/handlers/handlers.go` | new `App.HeadplaneExternalURL` field |
| `internal/handlers/handlers_admin_pages.go` | `acls` view uses `HeadplaneExternalURL` when set |
| `internal/handlers/templates/user/devices.html` | link to `{{.HeadplaneURL}}` instead of hardcoded tsnet.skynas.ru |
| `internal/handlers/templates/user/exit_nodes.html` | same |
| `internal/config/config.go` | new `HeadplaneExternalURL` field + `HEADPLANE_EXTERNAL_URL` env read |
| `cmd/skygate/main.go` | wires `cfg.HeadplaneExternalURL` into `app.HeadplaneExternalURL`; calls `SetMyCommandsAll` on bot startup |
| `deploy/deploy.sh` | new `HEADPLANE_EXTERNAL_URL` branch in headplane-strip + readiness-check gates; new `DERP_EXTERNAL_URLS` branch in the headscale-config render step |
| `deploy/backup.sh` | skip headplane + headplane-image when `HEADPLANE_EXTERNAL_URL` is set; record `HEADPLANE_EXTERNAL_URL`, `DERP_ENABLED`, `DERP_EXTERNAL_URLS` in inventory |
| `deploy/lib/env.sh` | defaults for `HEADPLANE_EXTERNAL_URL` and `DERP_EXTERNAL_URLS` |
| `.env.example` | new env vars with comments |
| `docs/headplane.md` | new "Use an existing Headplane" section |
| `docs/derp.md` | **NEW** — DERP integration contract |
| `docs/skygate-as-shell.md` | **NEW** — v0.11.0+ roadmap for pluggable headscale / multi-control-plane / ACL import |
| `AGENTS.md` | release status bumped; doc reference list updated; "what we're working on next" re-pointed at v0.11.0 candidates |

### Known issues (carried over from v0.10.11)

- `/clearrules` body still has hardcoded English. The
  catalog has all the keys (`bot.clearrules.*`); just the
  body helper wasn't updated when the rest of the bot got
  i18n-ized in v0.10.9. Tracked in AGENTS.md "v0.11.0
  candidates"; deliberately deferred (low impact, the
  command is rarely used).
- Personal API token rotation (TTL + auto-rotate) is still
  on the v0.11.0+ roadmap. Currently the only way to
  rotate a token is admin /my/tokens → revoke + re-issue.
- Butler voice v3 (urgency marks) is still deferred until
  the user has lived with v2 long enough to give feedback
  on whether the simpler envelope is the right long-term
  shape.

## RU

# v0.10.12 — Ленивое заполнение hostname + i18n-меню бота + полировка RU/EN + внешние Headplane/DERP

**Дата релиза**: 2026-07-15
**Предыдущий релиз**: v0.10.11

## Что в этом релизе

Сервисный + UX-релиз: правит два давно висевших бага в UX
бота, полирует русские переводы и готовит почву для идеи
«Skygate как оболочка» (админ подключает существующие
Headscale / Headplane / DERP вместо развёртывания
комплектного стека).

### Исправлено

- **`/my_nodes` и `/nodes` теперь показывают hostname, а не
  голый node id**. Миграция v0.34 добавила колонку
  `node_owner_map.hostname` ещё в v0.10.9, но
  `backfillNodeOwnership` запускался только через
  `/admin/devices` — так что любой юзер, открывший бота до
  этой страницы, видел голые id (`• 6`, `• 8`, `• 9` …) и
  никак не мог понять, какое устройство где. v0.10.12
  добавляет **ленивое заполнение**: когда `/my_nodes` или
  `/nodes` встречает пустой hostname, он вызывает
  `hs.ListAllNodes()` и заполняет пустые ячейки в
  `node_owner_map`. Первый вызов после апгрейда лечит
  таблицу; дальше — быстрое индексное чтение.
  (`internal/db/node_owner_map.go` +
  `internal/telegram/commands_user.go` +
  `commands_phase2.go`.)
- **Меню Telegram-бота теперь per-language**. В worktree
  `feature/telegram-bot-ux` (v0.10.4) лежал
  `commands_set.go` с жёстко прописанным английским — он
  протекал в RU-локали (на скриншоте в release notes
  видно `/help The Threshold's codex` даже в русском
  чате). v0.10.12 заменяет эту работу на i18n-версию:
  каждая запись меню ссылается на ключ каталога
  `bot.menu.<cmd>.description` (в `internal/i18n/catalog.go`
  добавлено 19 × 2 = 38 новых ключей), а `SetMyCommandsAll`
  регистрирует по меню на каждый язык через параметр
  `language_code` (Telegram Bot API 7.0+). В русском чате
  теперь меню на русском.

### Изменено

- **Полный аудит русских переводов (~270 ключей бота)**.
  В обзоре после v0.10.11 текст признали грубоватым — в
  нём оставалась метафора «врата / порог / хранитель» из
  gatekeeper-голоса v0.10.4, которая не пережила переход
  на дворецкого в v0.10.8. Каждая видимая пользователю
  строка переписана в едином дворецком регистре: **«Вы»**
  (формальное «вы») вместо разговорного «ты» где
  уместно, без остатков эпической фэнтези-метафоры,
  ровный русский порядок слов, «Формат:» вместо
  «использование:» для синтаксиса команд, единое
  «чат ещё не привязан к аккаунту skygate» в каждой
  команде. Подпись изменена на **«— Ваш Дворецкий»** /
  **«— Your butler»** (вариант D из четырёх, что я
  предложил — целиком в роли дворецкого, без бренда в
  самой подписи; бренд держится на знаке `🪶` сверху).
  Заголовки переработаны: `Врата` → `Добро пожаловать`,
  `Свиток версии` → `Версия`, `Закрытая дверь` →
  `Дверь закрыта`, `Добавление` → `Готово — добавлено`,
  `Удаление` → `Готово — удалено`. Ключи личности
  (`bot.personality.*`) переписаны под новый голос
  (`⟡ Ваш Дворецкий`, `вы — хозяин своих устройств, я —
  при них`). EN-ключи тоже подправлены там, где старые
  формулировки разошлись (например, «Yours in service, the
  Warden of the Threshold» → «Your butler»). Все правки —
  только в каталоге; Go-код не трогали.

- **Headplane: режим существующего инстанса**. Новая
  переменная окружения `HEADPLANE_EXTERNAL_URL` (добавлена
  в `.env.example` и `deploy/lib/env.sh`). Если она
  задана, `deploy/deploy.sh` вырезает блок `headplane` из
  рендеренного `docker-compose.yml` и пропускает
  readiness-проверку; страница `/admin/acls` ведёт на
  внешний URL вместо комплектного sidecar.
  `deploy/backup.sh` записывает `HEADPLANE_EXTERNAL_URL` в
  inventory, так что `deploy.sh --from-path` на другом
  хосте воспроизводит ту же схему. Подробности — в
  разделе «Use an existing Headplane» в
  `docs/headplane.md` (новый). Комплектный sidecar
  остаётся умолчанием для обратной совместимости.

- **DERP relay: режим внешних URL**. Новая
  `DERP_EXTERNAL_URLS` (через запятую, формат как у
  Tailscale `derp.urls`). Если задана, URL-ы
  дописываются в `headscale_config.derp.urls` рядом с
  `HEADSCALE_DERP_URLS` и комплектным `derper` (если
  `DERP_ENABLED=true`). Новый **`docs/derp.md`** (6 КБ)
  описывает оба режима + комбинацию «и то, и другое» +
  страницу здоровья `/admin/derp` + v0.11.0 follow-up
  по web-UI конфигу. Web-UI для DERP / Headplane — в
  roadmap ниже.

- **`docs/skygate-as-shell.md`** (8.4 КБ) — архитектурный
  roadmap v0.11.0 → v0.13.0. Фиксирует идею
  пользователя про «skygate как автономную оболочку»:
  админ подключает существующие модули Headscale /
  Headplane / DERP, Skygate даёт пользовательскую
  поверхность сверху. v0.11.0 = текущие deploy-time
  тогглы v0.10.12 поднимаются в web UI (страница
  `/admin/integrations` + хранение в `global_settings`).
  v0.12.0 = pluggable headscale (multi-control plane per
  user). v0.13.0 = импорт/экспорт ACL (B+C из запроса
  пользователя). Кода в v0.10.12 нет; это плановый
  документ.

### Гайд по миграции

С v0.10.11 → v0.10.12:

1. **Подтянуть и пересобрать** (вручную, без
   авто-обновления):
   ```bash
   cd /home/skyadmin/skygate
   git pull
   docker compose restart skygate
   ```
2. **Миграций БД нет.** v0.10.12 — только каталог +
   Go-код + деплой-скрипты; схема SQLite не меняется.
   Существующие строки `node_owner_map.hostname`
   заполняются при первом `/my_nodes` или `/nodes` после
   апгрейда (ленивое заполнение — действий от админа не
   требуется).
3. **Менять `.env` не нужно** для новых переменных
   (`HEADPLANE_EXTERNAL_URL`, `DERP_EXTERNAL_URLS`); они
   по умолчанию пустые, что сохраняет поведение v0.10.11.
4. **Смена подписи автоматическая** — в подвале каждого
   ответа бота теперь `— Ваш Дворецкий` / `— Your
   butler` (было `Искренне Ваш, Хранитель Порога` /
   `Yours in service, the Warden of the Threshold`).
5. **Смена меню бота автоматическая** — первый рестарт
   бота после апгрейда перерегистрирует per-language
   меню через `setMyCommands`. Пользователи увидят новое
   меню при следующем обновлении приложения (~60 с
   после завершения регистрации).

### Проверка

- 12/12 пакетов зелёные (`go test ./...`)
- 12/12 юнит-тестов по всем 10 внутренним пакетам PASS
  (включая новые тесты ленивого заполнения + i18n-тесты
  меню)
- Smoke 118/118 PASS на VM (`make test`)
- Сборка: `go build ./...` чисто
- Shell-скрипты: `bash -n` чисто на `deploy.sh`,
  `backup.sh`, `lib/env.sh`

### Известные проблемы (переходящие с v0.10.11)

- В теле `/clearrules` всё ещё жёстко прописан английский.
  В каталоге все ключи есть (`bot.clearrules.*`); не
  обновлён только helper, когда бот i18n-изировался в
  v0.10.9. В AGENTS.md «v0.11.0 candidates»; специально
  отложено (низкое влияние, команда редкая).
- Ротация personal API-токенов (TTL + auto-rotate) — в
  roadmap v0.11.0+. Сейчас единственный способ — admin
  /my/tokens → revoke + re-issue.
- Butler voice v3 (метки срочности) — отложено, пока
  пользователь не поживёт с v2 достаточно, чтобы дать
  обратную связь по форме конверта.
