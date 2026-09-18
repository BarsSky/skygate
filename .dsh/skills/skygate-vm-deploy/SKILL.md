---
name: skygate-vm-deploy
description: Деплой skygate на VM (<VM_HOST>) — push, git pull, пересборка в контейнере, проверка healthz. Учитывает CWD-требование docker compose, git stash при локальных правках и обязательный --force-recreate.
whenToUse: Выкатка изменений на VM, «задеплой», проверка что фикс доехал, разбор неудачного деплоя.
---

# Деплой skygate на VM

## Ключевые факты

* VM: `<VM_HOST>`, пользователь `skyadmin`, каталог проекта
  `/home/skyadmin/skygate`.
* Контейнер skygate **собирает бинарь на старте** через `entrypoint.sh`
  (`go build` по бинд-маунту `/home/skyadmin/skygate → /app`). Значит
  `docker compose restart skygate` после `git pull` **уже пересобирает** —
  `docker compose build` для правок только в Go-исходниках не нужен.
* `--force-recreate` требуется, если менялись `extra_hosts`, `environment`
  или volume.
* `docker compose` обязан вызываться из каталога проекта (иначе `.env` не
  подхватится и плейсхолдеры уедут в дефолты).

## Ручной путь (что делает скрипт)

```bash
# На Windows
git push origin main
# На VM
ssh skyadmin@<VM_HOST>
cd /home/skyadmin/skygate
git stash                 # если есть локальные правки (напр. .env)
git pull --ff-only
git stash pop
sudo docker compose stop skygate
sudo docker compose up -d --force-recreate --no-deps skygate
# ждать healthz
for i in $(seq 1 30); do curl -fsS http://127.0.0.1:8080/healthz && break; sleep 2; done
```

## Скриптом (предпочтительно)

```powershell
Set-Location C:\Projects\skygate
.\scripts\deploy_to_vm.sh
```

`scripts/rebuild_deploy.sh` — то же плюс pre-flight проверки; поддерживает
override пути репозитория:

```bash
SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate bash scripts/rebuild_deploy.sh
```

## Обязательная проверка после деплоя

```bash
curl -s http://127.0.0.1:8080/healthz; echo
sudo docker compose logs --tail=80 skygate | grep -Ei 'error|warn|panic|migrate'
sudo docker compose config | grep -A3 extra_hosts      # рендер, а не догадки
```

Полезно сразу зафиксировать, **какой бэкенд БД реально поднялся** — это
частая причина «фикс не сработал»:

```bash
sudo docker compose exec skygate env | grep -E '^SKYGATE_DB'
sudo docker compose logs skygate | grep -i 'migrate'
```

## Грабли

| Симптом | Причина |
|---|---|
| Фикс «не доехал», бинарь старый | запускали `docker compose` из `/` — `.env` не подхватился |
| `extra_hosts` не применились | сделан `restart` вместо `up -d --force-recreate` |
| `/admin/exit-nodes` отдаёт text/plain страницу | БД = SQLite, а в ней нет колонок `ssh_target`/`ssh_key_path` — см. скилл `skygate-db-dialect-audit` |
| деплой прошёл, но exit-node SSH молчит | `SKYGATE_EXIT_SSH_KEY` указывает на путь, не смонтированный в контейнер — см. `skygate-exit-node-ssh-debug` |
| `git pull` падает на конфликте `.env` | `.env` на VM правится вручную; использовать `git stash`/`git stash pop` |

## Правило проекта

Если правка касается HA-цепочки, certsync, DNS-failover или подкоманд
`deploy` — обновить статус-лог в
`docs/internal/runbooks/ha-v1.5.0-execution.md` §6 **в том же коммите**.
