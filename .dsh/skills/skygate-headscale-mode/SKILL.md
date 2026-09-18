---
name: skygate-headscale-mode
description: Определяет, где живёт headscale (docker / systemd / k8s / remote) и подставляет правильную форму команд для перезапуска, SIGHUP, ACL, DERP, тегов и approve-routes. Закрывает класс ошибок «скрипт жёстко прописан на docker».
whenToUse: Любая операция с headscale (рестарт, конфиг, ACL, DERP, теги, маршруты), headscale вне Docker, правка скриптов и Go-кода, дергающего headscale CLI.
---

# Режим headscale: docker / systemd / k8s / remote

## Проблема

Проект предполагает headscale в Docker почти везде, а **существующий образец
правильного решения есть только для OIDC**:

* `deploy/oidc-sync.sh:175-198` — автодетект режима: `docker` → `systemd` →
  `k8s` → `manual`
* `deploy/oidc-sync.sh:346-361` — рестарт: `docker restart` /
  `systemctl restart headscale` / `kubectl rollout restart` /
  `docker exec … headscale configure oidc`
* прокидка режима: `internal/oidc/sync.go:251-253` (`--mode`),
  форма: `internal/handlers/templates/admin/oidc_sync.html:116-126`

**Режим нигде не сохраняется** — `internal/feature/admin/oidc_sync.go:101` и
`cmd/skygate/main.go:824` каждый раз подставляют `"auto"`.

## Как определить режим вручную

```bash
# docker
command -v docker >/dev/null && [ -S /var/run/docker.sock ] && docker ps --format '{{.Names}}' | grep -Fx "${HEADSCALE_CONTAINER:-headscale}"
# systemd
systemctl list-unit-files headscale.service 2>/dev/null | grep -q '^headscale.service'
# k8s
kubectl get deploy headscale -n headscale 2>/dev/null
# remote (только REST API)
curl -fsS -H "Authorization: Bearer $HEADSCALE_API_KEY" "$HEADSCALE_URL/api/v1/health"
```

## Единая точка (то, к чему надо привести код и скрипты)

```bash
hs_exec() {
  case "${HEADSCALE_MODE:-auto}" in
    docker)  docker exec "${HEADSCALE_CONTAINER:-headscale}" "${HEADSCALE_BIN:-/ko-app/headscale}" "$@" ;;
    systemd) headscale "$@" ;;
    k8s)     kubectl exec deploy/headscale -n headscale -- headscale "$@" ;;
    remote)  ssh "$HEADSCALE_SSH" headscale "$@" ;;
    *)       echo "headscale mode unknown" >&2; return 1 ;;
  esac
}
hs_reload()  { systemctl reload headscale  2>/dev/null || kill -HUP "$(cat /var/run/headscale.pid)"; }
hs_restart() {
  case "$HEADSCALE_MODE" in
    docker)  docker restart "${HEADSCALE_CONTAINER:-headscale}" ;;
    systemd) systemctl restart headscale ;;
    k8s)     kubectl rollout restart deploy/headscale -n headscale ;;
    remote)  ssh "$HEADSCALE_SSH" systemctl restart headscale ;;
  esac
}
```

В Go — образцы, которые надо переиспользовать, а не изобретать:
* `internal/feature/admin/derp_cert_sync.go:486-513` — systemd → pid-file HUP
* `internal/feature/admin/tailscale.go:1437-1488` + `isRunningInContainer():1504-1509`
  — «в контейнере / на хосте»

## Docker-хардкод, который надо перевести (с указанием места)

| Место | Что жёстко |
|---|---|
| `internal/headscale/routes.go:86` | `docker exec headscale /ko-app/headscale nodes approve-routes` — игнорирует `HEADSCALE_CONTAINER` |
| `internal/headscale/acl.go:148-150` | `docker run -v /home/admin/headscale/config:/config alpine …` |
| `internal/headscale/acl.go:157` | `docker restart` |
| `internal/feature/admin/integrations_renderer.go:240` | `docker cp … headscale:/etc/headscale/config.yaml` |
| `internal/feature/admin/integrations_renderer.go:248` | `docker kill -s HUP headscale` (единственный SIGHUP в репо) |
| `internal/feature/admin/derp_apply_headscale_b237.go:270` | `docker restart` |
| `internal/headscale/provision.go:233` | `docker ps` |
| `scripts/{restore,backup,init-headplane,rotate_ts_authkey,bootstrap_standby,ha-phase0,reconcile_snapshots}.sh` | `docker exec/restart headscale` |
| `deploy/headscale-users/headscale-bootstrap.sh:218-256` | `docker exec <ctr> /ko-app/headscale` |
| `scripts/upgrade.sh:145` | захардкоженный ID контейнера `0c8931e2a82a` |

Образец абстракции в скриптах уже есть:
`scripts/b_mod_reregister_live.sh:100-148` (переменная `HEADSCALE_CLI`).

## Переменные окружения (фактическое положение)

| Переменная | Читается? | Где |
|---|---|---|
| `HEADSCALE_URL`, `HEADSCALE_API_KEY` | да, **один раз при старте** | `internal/config/config.go:497-500` |
| `HEADSCALE_CONTAINER` | да, но 4 места игнорируют | `internal/headscale/headscale.go:96` |
| `SKYGATE_HEADSCALE_CONFIG_PATH` | да | `derp_apply_headscale_b237.go:168` |
| `SKYGATE_HEADSCALE_CONTAINER` | да | `derp_apply_headscale_b237.go:123` |
| `HEADSCALE_SERVER_URL` | **нет, ни один Go-файл** — но `deploy/lib/env.sh:48-49` делает его обязательным | — |

## Куда двигаться

Ввести `headscale.location` в `global_settings` (`internal/db/globalsettings.go`)
со значениями `auto|docker|systemd|k8s|remote` и резолвером **DB > env >
autodetect**, плюс ключи `headscale.container`, `headscale.systemd_unit`,
`headscale.config_path`, `headscale.bin`. Образцы того же паттерна уже есть:
`headplane.mode` (`internal/db/integrations.go:69-87`) и
`tailscale.login_server` (`internal/feature/admin/tailscale.go:521-549`).

UI — секция на `/admin/integrations` или новая `/admin/headscale` с кнопкой
«Проверить» (реально проверяет каждый канал) и «Перезапустить headscale».

## Чего не делать

* Не добавлять новые `docker exec headscale` без `HEADSCALE_CONTAINER`.
* Не считать `HEADSCALE_SERVER_URL` рабочим — он нигде не читается.
* Не доверять `HEADSCALE_MODE=auto`: при отсутствии docker-сокета он молча
  уедет в `manual` и операция «успешно» ничего не сделает.
