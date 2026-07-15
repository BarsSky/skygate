# v0.15.1 — Final `/admin/telegram` localization

**Дата:** 2026-07-15
**Размер:** маленький, чисто строковый (RU/EN) — никаких миграций, никаких новых API,
никаких изменений в поведении.

## Что вошло

Финальный проход локализации страницы `/admin/telegram`. После v0.15.0 и v0.10.12
всё ещё оставалось ~28 hardcoded строк (EN only), привязанных к:

* **Status pills** (configured / not configured pill text + tooltip)
* **Probe section** — 3 state labels (`ok_direct`, `ok_relay`, `unreachable`),
  latency format string, resolved-IPs prefix, troubleshooting heading и
  4 bullet tips (advertise / approve / update / docs)
* **Token & chat_id help paragraphs** (botfather link, chat-id hint)
* **Test message form** — labels (`subject` / `body`), disabled-hint,
  `keep current` placeholder
* **Rotate / Disable confirm dialogs** — help, confirm text, post-action
  warning line
* **"Where other components look"** — 5 bullet list (backup / upgrade / env
  vars / docs)

Все эти строки теперь идут через `{{t "telegram.*"}}` в шаблоне
`internal/handlers/templates/admin/telegram.html`. Каталог
`internal/i18n/catalog.go` дополнен **32 новыми `telegram.*` ключами × 2 языка**
(64 строки). i18n parity test (`go test ./internal/i18n/...`) зелёный.

## Изменения по файлам

* `internal/i18n/catalog.go` — 32 новых ключа (× RU + EN = 64 строки)
* `internal/handlers/templates/admin/telegram.html` — все ранее-hardcoded строки
  заменены на `{{t "..."}}` / `{{t "..." | safeHTML}}` (для значений с `<code>`/`<a>`).
  Template-комментарий в шапке переписан, чтобы парсер Go templates не пытался
  вычислить `{{t ...}}` внутри `{{/* ... */}}` (это та же ловушка, что
  приводила к 4.4KB error page в v0.12.0.2 — на этот раз словлена до релиза,
  в code review).

## Совместимость

* Никаких изменений в БД, API, .env, docker-compose, deploy.sh
* Никаких новых зависимостей
* Сборка: `go build ./...` чистая
* Тесты: `go test ./...` — 11/11 пакетов зелёные
* Smoke: 118/118 (RU + EN), как в v0.15.0

## Апгрейд

```bash
cd /home/skyadmin/skygate
git pull
docker compose up -d --force-recreate --no-deps skygate
bash scripts/smoke.sh
```

После апгрейда визуально проверить `/admin/telegram` в обеих локалях — все
надписи и подсказки должны переключаться через переключатель языка в
правом верхнем углу (включая probe-banner при включённом боте).

## Что осталось / не вошло

* `v0.15.x follow-up` — wire `check_https.py` в smoke как optional HTTPS-smoke
  (сейчас smoke HTTP-only)
* `v0.14.1` — wire `db.SyncNodesFromHeadscale` в `ExitNodeMonitor.tick` (auto-heal)
* `v0.16.0` — per-plane ACL + ACL import/export с dry-run
* `BotEnv.HeadscaleRouter` per-user bot routing (отложено из v0.12.0)

— Ваш Дворецкий
