# Skygate v0.10.10

> **Headplane as optional pinned module.** No more silent
> `:latest` upgrades; integration contract now in
> [docs/headplane.md](docs/headplane.md).

---

## 🇬🇧 English

### What is Skygate?

A small Go web portal for [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale) that gives
every user a friendly UI for the things they'd otherwise have to
ask the admin to run on the headscale CLI. Read the v0.10.7
notes for the full intro.

### What's new in v0.10.10?

* **Headplane is now a documented optional module** —
  [Headplane](https://github.com/tale/headplane) is the visual
  ACL editor + admin cockpit for Headscale. Skygate has shipped
  a Headplane sidecar in its docker-compose stack since the
  v0.10.x line, but until now it was: (a) pinned to
  `:latest` (a silent upgrade hazard), (b) always-on (no
  opt-out), and (c) undocumented. v0.10.10 fixes all three.

* **Pinned version** — the deploy template uses
  `${HEADPLANE_IMAGE}` from `.env`, defaulting to
  `ghcr.io/tale/headplane:0.6.3` (the latest stable as of
  2026-04-09). A Skygate upgrade never silently bumps the
  dependency. To upgrade Headplane, edit the env var and
  re-run `./deploy/deploy.sh`.

* **Optional out of the box** — set
  `HEADPLANE_ENABLED=false` in `.env` to skip the sidecar
  entirely. The deploy script strips the `headplane` service
  from `docker-compose.yml` and skips the readiness check.
  No other Skygate functionality depends on Headplane.

* **Integration contract documented** —
  [docs/headplane.md](docs/headplane.md) describes:
  - What Headplane does that Skygate doesn't (visual ACL
    editing, machine management, OIDC, browser SSH, custom
    DNS records).
  - The version pin policy and a compatibility matrix
    (Skygate v0.10.10 + Headscale 0.29.x + Headplane 0.6.3 is
    the current recommended combination).
  - How to upgrade Headplane, how backup/restore handle
    the sidecar, and why Skygate doesn't replace Headplane.

* **Backup/restore respect the opt-out** —
  `deploy/backup.sh` records `HEADPLANE_ENABLED` and
  `HEADPLANE_IMAGE` in the inventory. When the sidecar is
  disabled, no `headplane-config.yaml`, `headplane-data/`,
  or `headplane-image.tar` is included in the archive —
  the backup stays consistent with the running stack.

* **Anonymised relay names** — `.env.example`'s
  `SKYGATE_EXIT_SSH_EMILIA` / `_SHARLOTTA` are now
  `_RELAY_B` / `_RELAY_C` to match the v0.10.7 release
  notes (which use `relay-a / -b / -c`). The old names
  leaked the operator's infrastructure into a public
  config example.

### Test status on VM

* `go test -count=1 -short ./...` — 12 / 12 packages PASS
* `bash scripts/smoke.sh` — 118 / 118 (59 ru + 59 en)
* `python3 scripts/check_exit_nodes.py` — 3 / 3 relay nodes OK
* The deploy script change is exercised by
  `deploy/validate.sh` (skygate-ssh-public-relay ACL) and
  the existing backup/restore scripts. The optional-path
  (HEADPLANE_ENABLED=false) is a doc + script conditional,
  not new logic that needs its own test.

### What we're working on next

* **`/clearrules` i18n** — body helper still has hardcoded
  English. Catalog has all the keys; just needs the body
  touched. v0.10.11 follow-up.
* **Butler voice v3** — urgency marks in the header
  (`🪶` / `🪶!` / `🪶!!`) and inline status marks in the
  body. Defer until user feedback on v2 lands.
* **Personal API token rotation** — TTL + auto-rotate for
  the AI integration. v0.10.11 candidate.

---

## 🇷🇺 Русский

### Что такое Skygate?

Небольшой Go-портал для [Tailscale](https://tailscale.com) /
[headscale](https://github.com/juanfont/headscale), дающий
каждому пользователю удобный UI для вещей, которые иначе
пришлось бы делать через CLI администратора.

### Что нового в v0.10.10?

* **Headplane — задокументированный optional-модуль** —
  [Headplane](https://github.com/tale/headplane) — визуальный
  ACL-редактор + админ-кабина для Headscale. Skygate
  деплоил Headplane-sidecar с линейки v0.10.x, но до сих пор
  это было: (а) запинено на `:latest` (тихая угроза апгрейда),
  (б) всегда включено (без opt-out), (в) не задокументировано.
  v0.10.10 фиксит все три.

* **Пин версии** — deploy-шаблон использует
  `${HEADPLANE_IMAGE}` из `.env`, по умолчанию
  `ghcr.io/tale/headplane:0.6.3` (последний стабильный на
  2026-04-09). Апгрейд Skygate никогда не поднимает
  зависимость автоматически. Чтобы обновить Headplane —
  правите env var и перезапускаете `./deploy/deploy.sh`.

* **Optional из коробки** — `HEADPLANE_ENABLED=false` в
  `.env` пропускает sidecar полностью. Deploy-скрипт
  вырезает `headplane` service из `docker-compose.yml` и
  пропускает readiness check. Никакая функциональность
  Skygate не зависит от Headplane.

* **Интеграционный контракт задокументирован** —
  [docs/headplane.md](docs/headplane.md) описывает:
  - Что Headplane делает такого, чего Skygate не умеет
    (визуальный ACL, machine management, OIDC, browser SSH,
    custom DNS).
  - Политику пина версий + compatibility matrix (Skygate
    v0.10.10 + Headscale 0.29.x + Headplane 0.6.3 — текущая
    рекомендованная комбинация).
  - Как обновлять Headplane, как backup/restore работают
    с отключённым sidecar, и почему Skygate не заменяет
    Headplane.

* **Backup/restore учитывают opt-out** —
  `deploy/backup.sh` записывает `HEADPLANE_ENABLED` и
  `HEADPLANE_IMAGE` в inventory. Когда sidecar выключен,
  `headplane-config.yaml`, `headplane-data/`, и
  `headplane-image.tar` НЕ включаются в архив — бэкап
  остаётся согласованным с запущенным стеком.

* **Анонимизированы имена relay** — `SKYGATE_EXIT_SSH_EMILIA`
  / `_SHARLOTTA` в `.env.example` теперь `_RELAY_B` / `_RELAY_C`,
  чтобы соответствовать release notes v0.10.7 (используют
  `relay-a / -b / -c`). Старые имена утекали инфраструктуру
  оператора в публичный конфиг-пример.

### Тест-статус на VM

* `go test -count=1 -short ./...` — 12 / 12 пакетов PASS
* `bash scripts/smoke.sh` — 118 / 118 (59 ru + 59 en)
* `python3 scripts/check_exit_nodes.py` — 3 / 3 relay-ноды OK
* Изменения в deploy-скриптах покрываются
  `deploy/validate.sh` (skygate-ssh-public-relay ACL) и
  существующими backup/restore скриптами. Optional-путь
  (`HEADPLANE_ENABLED=false`) — это doc + условный
  скрипт, а не новая логика, требующая отдельного теста.

### Что делаем дальше

* **i18n для `/clearrules`** — body-helper всё ещё с
  захардкоженным английским. Ключи в каталоге есть,
  нужно только заменить. Кандидат на v0.10.11.
* **Butler voice v3** — отметки срочности в заголовке
  (`🪶` / `🪶!` / `🪶!!`) и статуса в теле. Отложим до
  обратной связи по v2.
* **Ротация персональных API-токенов** — TTL + auto-rotate.
  Кандидат на v0.10.11.

---

## v0.10.9 → v0.10.10

* **Commit range**: `0fd8179..<HEAD>` (1+ commits, see git log)
* **Files changed**: `.env.example`, `deploy/templates/headscale-compose.yml.tmpl`,
  `deploy/deploy.sh`, `deploy/backup.sh`, `deploy/lib/env.sh`,
  `internal/acl/acl.go`, `README.md`, `README.ru.md`, `AGENTS.md`,
  `docs/headplane.md` (new), `RELEASE-NOTES-v0.10.10.md` (this file)
* **Schema**: unchanged. No migration required.
* **Smoke**: 118 / 118 (unchanged)
* **Packages**: 12 / 12 (unchanged)
* **Deploy-time behavior change**: Headplane deploys by default
  (was: same). Set `HEADPLANE_ENABLED=false` to skip.

---

## Migration from v0.10.9

No action required. The deploy template now uses
`${HEADPLANE_IMAGE}` (default `ghcr.io/tale/headplane:0.6.3`)
instead of `:latest`, but the `latest` tag on Docker Hub
currently points to `0.6.3`, so a fresh deploy will pull the
same image as before. Existing deployments keep their
currently-running image (no auto-upgrade triggered by the
template change).

To upgrade Headplane after this release:
1. Edit `.env`: set `HEADPLANE_IMAGE=ghcr.io/tale/headplane:<new-tag>`
2. Re-run `./deploy/deploy.sh`
3. The script does `docker compose pull headplane && up -d`
4. Verify the UI at `https://your-host:50445/admin/`

To opt out of Headplane entirely (the user's /my_* and admin
Telegram bot keep working — they don't need Headplane):
1. Set `HEADPLANE_ENABLED=false` in `.env`
2. Re-run `./deploy/deploy.sh` — the sidecar service is
   removed from `docker-compose.yml`, no container starts.
3. Backup will skip the `headplane-config.yaml`,
   `headplane-data/`, and `headplane-image.tar` artifacts.
