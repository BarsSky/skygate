# v0.11.0 — /admin/integrations: web UI для DERP и Headplane

**Release date**: 2026-07-15
**Previous release**: v0.10.14

## What's in this release

The v0.10.12 deploy-time toggles (`DERP_EXTERNAL_URLS`,
`HEADPLANE_EXTERNAL_URL`, `HEADPLANE_ENABLED`) are now editable
from the web UI. Operators can manage DERP and Headplane
without touching `.env` or running `./deploy/deploy.sh` —
they go to `/admin/integrations`, click a link, fill the
form, save, and the new state is in `global_settings`. The
deploy-time env-vars are still consulted as a fallback for
operators who haven't visited the UI yet, so the v0.10.12
deploy model keeps working unchanged.

### What's editable

| Component | /admin page        | Fields                                                                 | Source of truth |
|-----------|-------------------|-------------------------------------------------------------------------|-----------------|
| DERP       | /admin/derp/config | `external_urls` (one per line / comma-separated) + `bundled_enabled` checkbox | `global_settings.derp.external_urls`, `derp.bundled_enabled` |
| Headplane  | /admin/headplane   | `mode` radio (bundled / external / off) + `external_url` text field   | `global_settings.headplane.mode`, `headplane.external_url` |

The landing page `/admin/integrations` shows the current state
of both components with a "Configure" link to each form.

### What's NOT in this release (deferred to v0.11.1)

- **Runtime renderer.** Saving the form persists to
  `global_settings` and audits the change. The page surfaces
  a banner telling the operator to run `./deploy/deploy.sh`
  to apply the headscale-side change. v0.11.1 will wire a
  Go-side renderer that:
  - re-reads `IntegrationConfig` from the DB
  - re-renders the headscale `docker-compose.yml` (port the
    Python one-liner in `deploy/deploy.sh` to Go)
  - `docker compose restart headscale` (or `kill -HUP` if we
    can get away with it)
  - shows the result inline on `/admin/integrations`
- **Test URL button.** The form has the URL field; the
  "Probe" action (5s `GET` + read first bytes) is wired in
  `probeDerpmapURL` but not surfaced in the form yet. v0.11.1
  adds the button + the result rendering.
- **Web-UI ACL / per-plane config (v0.12.0+).** The
  Skygate-as-shell roadmap has more (per-headplane-instance
  config, multi-control-plane URLs, etc.). v0.11.0 is the
  first slice; v0.12.0 builds on this storage layer.

### Migration guide

From v0.10.14 → v0.11.0:

1. **Pull and rebuild** (operator-driven, no auto-update):
   ```bash
   cd /home/skyadmin/skygate
   git pull
   docker compose restart skygate
   ```
2. **No DB migrations.** v0.11.0 is catalog + Go code + 3 new
   HTML templates. `global_settings` already has the schema
   (added in v0.21) — we just write to 4 new keys:
   `derp.external_urls`, `derp.bundled_enabled`,
   `headplane.mode`, `headplane.external_url`.
3. **No required action.** Existing deploy-time env-var
   config keeps working. Visit `/admin/integrations` to see
   the current state (rendered from `global_settings` with
   env-var fallback) and migrate at your own pace.
4. **Optional migration path:** when you visit
   `/admin/derp/config` and click Save, the form values go
   to `global_settings`. The env-var fallback is still
   active for the deploy script — both sources are read on
   every page load, DB wins.

### Verification

- 12/12 packages green (`go test ./...`)
- 9 new tests in `internal/db/integrations_test.go`:
  - empty-by-default, env-fallback, external-URL-flips-mode,
    HEADPLANE_ENABLED-false-maps-to-off, DB-overrides-env,
    DB-derived-URL-overrides-mode-off (the "operator pasted
    a URL into the off mode by accident" safety net),
    round-trip, empty-clear-persisted-empty, splitCSV
- 12 new tests in `internal/handlers/admin_integrations_test.go`:
  - 403 for non-admin (GET + POST on each endpoint),
    200 for admin, persist+reflects, newline-separated URLs,
    Headplane-save-doesn't-clobber-DERP, invalid mode
    rejected, external without URL rejected, non-HTTPS
    rejected, landing-page reflects state
- Smoke 118/118 PASS on VM (`make test`)
- i18n: 30 new `derp.*` / `headplane.*` / `integrations.*`
  catalog keys, parity holds

### Files changed

| File | What |
|---|---|
| `internal/db/integrations.go` | **NEW** — `IntegrationConfig` struct + `LoadIntegrations` / `LoadIntegrationsFromOS` / `SaveIntegrations` / `splitCSV` |
| `internal/db/integrations_test.go` | **NEW** — 9 tests pinning the env-var fallback, mode derivation, CSV splitting, round-trip |
| `internal/db/node_owner_map_test.go` | helper now also creates `global_settings` so other tests (incl. integrations) can share it |
| `internal/handlers/admin_integrations.go` | **NEW** — 5 handlers (GET integrations, GET+POST derp/config, GET+POST headplane) + 3 redirect helpers + `probeDerpmapURL` (used by the v0.11.1 Test button) + `splitAndTrimCSV` |
| `internal/handlers/admin_integrations_test.go` | **NEW** — 12 tests covering admin-gating, persist+reflects, validation, cross-config preservation |
| `internal/handlers/templates/admin/integrations.html` | **NEW** — landing page (current DERP + Headplane state with Configure links) |
| `internal/handlers/templates/admin/derp_config.html` | **NEW** — DERP edit form |
| `internal/handlers/templates/admin/headplane.html` | **NEW** — Headplane edit form |
| `internal/i18n/catalog.go` | 30 new `derp.*` / `headplane.*` / `integrations.*` keys (× 2 languages); all user-facing strings RU/EN |
| `cmd/skygate/main.go` | 5 new routes (`GET /admin/integrations`, `GET/POST /admin/derp/config`, `GET/POST /admin/headplane`) |

## RU

# v0.11.0 — /admin/integrations: web UI для DERP и Headplane

**Дата релиза**: 2026-07-15
**Предыдущий релиз**: v0.10.14

## Что в этом релизе

Deploy-time тогглы v0.10.12 (`DERP_EXTERNAL_URLS`,
`HEADPLANE_EXTERNAL_URL`, `HEADPLANE_ENABLED`) теперь
редактируются из web-интерфейса. Оператор может управлять
DERP и Headplane без правки `.env` и без запуска
`./deploy/deploy.sh` — идёт в `/admin/integrations`, кликает
по ссылке, заполняет форму, сохраняет, новое состояние
попадает в `global_settings`. Deploy-time env-ы всё ещё
работают как fallback для тех, кто UI ещё не открывал —
модель v0.10.12 продолжает работать без изменений.

### Что редактируется

| Компонент  | /admin страница      | Поля                                                                 | Source of truth |
|-----------|---------------------|----------------------------------------------------------------------|-----------------|
| DERP       | /admin/derp/config   | `external_urls` (по строке / через запятую) + `bundled_enabled` checkbox | `global_settings.derp.external_urls`, `derp.bundled_enabled` |
| Headplane  | /admin/headplane     | `mode` radio (bundled / external / off) + `external_url` text field | `global_settings.headplane.mode`, `headplane.external_url` |

Лендинг `/admin/integrations` показывает текущее состояние
обоих компонентов с кнопкой «Настроить» на каждом.

### Что НЕ вошло (отложено в v0.11.1)

- **Runtime-рендерер.** Сохранение формы пишет в
  `global_settings` и пишет audit. Страница показывает
  баннер с просьбой запустить `./deploy/deploy.sh`, чтобы
  изменения вступили в силу на стороне headscale. В v0.11.1
  появится Go-side рендерер, который:
  - перечитывает `IntegrationConfig` из БД
  - перегенерирует headscale `docker-compose.yml` (порт
    Python one-liner из `deploy/deploy.sh` на Go)
  - `docker compose restart headscale` (или `kill -HUP`,
    если получится)
  - показывает результат прямо в `/admin/integrations`
- **Кнопка Test URL.** Поле URL в форме есть; функция
  `probeDerpmapURL` (5-сек `GET` + чтение первых байт)
  написана, но кнопка в форму не добавлена. В v0.11.1
  добавится кнопка + рендеринг результата.
- **Web-UI для per-plane / ACL (v0.12.0+).** Roadmap
  Skygate-as-shell шире (per-headplane-instance config,
  multi-control-plane URLs и т.д.). v0.11.0 — первый
  срез; v0.12.0 строится на этом storage-слое.

### Гайд по миграции

С v0.10.14 → v0.11.0:

1. **Подтянуть и пересобрать** (вручную, без
   авто-обновления):
   ```bash
   cd /home/skyadmin/skygate
   git pull
   docker compose restart skygate
   ```
2. **Миграций БД нет.** v0.11.0 — каталог + Go-код + 3
   новых HTML-шаблона. `global_settings` уже имеет нужную
   схему (добавлена в v0.21) — пишем в 4 новых ключа:
   `derp.external_urls`, `derp.bundled_enabled`,
   `headplane.mode`, `headplane.external_url`.
3. **Обязательных действий нет.** Существующий
   deploy-time env-var конфиг продолжает работать.
   Зайдите в `/admin/integrations` чтобы увидеть
   текущее состояние (рендерится из `global_settings`
   с env-var fallback) и мигрируйте в своём темпе.
4. **Опциональный путь миграции:** когда зайдёте в
   `/admin/derp/config` и нажмёте «Сохранить», значения
   формы попадут в `global_settings`. Env-var fallback
   для deploy-скрипта остаётся активным — оба источника
   читаются при каждом page load, БД выигрывает.

### Проверка

- 12/12 пакетов зелёные (`go test ./...`)
- 9 новых тестов в `internal/db/integrations_test.go`:
  empty-by-default, env-fallback, external-URL-flips-mode,
  HEADPLANE_ENABLED-false-maps-to-off, DB-overrides-env,
  DB-derived-URL-overrides-mode-off (safety net на случай
  «оператор случайно вставил URL в режим off»), round-trip,
  empty-clear-persisted-empty, splitCSV
- 12 новых тестов в
  `internal/handlers/admin_integrations_test.go`:
  403 для не-админа (GET + POST на каждом эндпоинте),
  200 для админа, persist+reflects, newline-separated URLs,
  Headplane-save-doesn't-clobber-DERP, invalid mode
  rejected, external without URL rejected, non-HTTPS
  rejected, landing-page reflects state
- Smoke 118/118 PASS на VM (`make test`)
- i18n: 30 новых `derp.*` / `headplane.*` / `integrations.*`
  ключей в каталоге (× 2 языка); паритет держится

### Изменённые файлы

| Файл | Что |
|---|---|
| `internal/db/integrations.go` | **NEW** — `IntegrationConfig` + `LoadIntegrations` / `LoadIntegrationsFromOS` / `SaveIntegrations` / `splitCSV` |
| `internal/db/integrations_test.go` | **NEW** — 9 тестов env-var fallback, mode derivation, CSV split, round-trip |
| `internal/db/node_owner_map_test.go` | helper теперь создаёт и `global_settings` |
| `internal/handlers/admin_integrations.go` | **NEW** — 5 хендлеров + 3 redirect helpers + `probeDerpmapURL` (для v0.11.1) + `splitAndTrimCSV` |
| `internal/handlers/admin_integrations_test.go` | **NEW** — 12 тестов |
| `internal/handlers/templates/admin/integrations.html` | **NEW** — лендинг |
| `internal/handlers/templates/admin/derp_config.html` | **NEW** — DERP форма |
| `internal/handlers/templates/admin/headplane.html` | **NEW** — Headplane форма |
| `internal/i18n/catalog.go` | 30 новых ключей (× 2 языка) |
| `cmd/skygate/main.go` | 5 новых роутов |
