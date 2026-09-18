---
name: skygate-b-check-runner
description: Как правильно запускать и читать B-чек контракты skygate (scripts/check_b*.sh — 214 штук, из них 203 в scripts/). Инкапсулирует CWD-требование docker compose, PSQL/SSH-зависимости и способ прогона на Windows.
whenToUse: Запуск scripts/check_b*.sh, scripts/verify_pre_deploy.sh, scripts/verify_post_deploy.sh; разбор падения B-чека.
---

# Запуск B-чеков skygate

## Что это

`scripts/` содержит **214** файлов `check_*.sh`, из них **203** — это
`check_b*.sh` контракты (B81, B140, B180, B260 …). Плюс
`verify_pre_deploy.sh` (гейт в `ci.yml:74`) и `verify_post_deploy.sh`.
Из них **31 использует `psql`** (PG-only) и **54 — `docker`** [проверено
2026-09-17].

## Три правила, без которых чек врёт

### 1. `docker compose` обязан запускаться из каталога проекта

`docker-compose` **не** подхватывает `.env` при вызове
`docker compose -f /path/to/file.yml` из другого CWD — плейсхолдеры молча
берут значение по умолчанию. Это стоило часа отладки в сеансе B260.

```bash
# ПРАВИЛЬНО
cd /home/skyadmin/skygate && sudo docker compose up -d --force-recreate skygate
# ЛИБО явно
docker compose -f /home/skyadmin/skygate/docker-compose.yml \
  --env-file /home/skyadmin/skygate/.env up -d
```

### 2. `--force-recreate` обязателен после изменения `extra_hosts`/volume

`docker compose restart` не перерисовывает `/etc/hosts` и бинд-маунты.
Для B260 это критично.

### 3. Чек, который ходит в БД, зависит от бэкенда

```powershell
Set-Location C:\Projects\skygate
# какой чек чем пользуется
Get-ChildItem scripts -Filter 'check_*.sh' |
  Select-String -Pattern 'psql' -List | ForEach-Object { "psql:   $($_.Filename)" }
Get-ChildItem scripts -Filter 'check_*.sh' |
  Select-String -Pattern 'sqlite3' -List | ForEach-Object { "sqlite: $($_.Filename)" }
```

Если чек использует `psql`, а развёртывание на SQLite — он **не проверяет
ничего** и/или падает ложно. Универсальный хелпер (`scripts/lib/db_exec.sh`)
уже запланирован в плане (`docs/ROADMAP.md`, §D.5).

## Запуск

### На VM (канонический путь)

```bash
ssh skyadmin@<VM_HOST>
cd /home/skyadmin/skygate
bash scripts/check_b180.sh            # одиночный
bash scripts/verify_pre_deploy.sh     # полный гейт (долго! 10+ мин)
```

### С Windows

Локальный прогон ограничен: большинство чеков требуют `bash`, `docker` и
живой VM. Использовать `git-bash` + SSH, либо Windows→VM скрипт:

```powershell
Set-Location C:\Projects\skygate
.\scripts\deploy_to_vm.sh             # push + pull + recreate + healthz
```

## Гейт перед push

`.git/hooks/pre-push` вызывает `scripts/verify_pre_deploy.sh`. На Windows в
git-bash он может висеть 10+ минут (каждый B-чек — отдельный bash-подпроцесс).
Для срочного деплоя:

```bash
git push origin main --no-verify       # осознанное исключение, не норма
```

## Разбор падения

1. Открыть сам чек — это grep-контракт, он печатает, какой паттерн не найден.
2. Найти целевой код по паттерну; частые причины:
   * код переехал в новый пакет (пример: B162/B169 пришлось править после
     рефакторинга в `internal/devicedelete`);
   * паттерн ищет литерал, а вызов обёрнут (пример: `InvalidateCache`);
   * чек требует живой VM/PG — тогда он не про регрессию, а про состояние.
3. **Не «подгонять» контракт под код молча.** Если поведение изменилось
   намеренно — обновить чек и записать это в `AGENTS.md` (правило проекта:
   трекеры не должны дрейфовать).

## Известные проблемы

* `go vet ./...` **красный** на `6fff2e25` (`internal/feature/admin/derp.go:810`,
  `append with no values`) → job `ci` в `.github/workflows/ci.yml:39` не
  проходит. Чинить первым.
* Нет `scripts/check_b254*.sh`, хотя коммит B253 явно отложил часть чистки
  `?`-плейсхолдеров «на B254» — то есть класс не закреплён контрактом.
* Нет ни одного чека на путь SSH-ключа exit-node (`SKYGATE_EXIT_SSH_KEY`,
  `ssh_key_path`) — поэтому поломка `SKYGATE_EXIT_SSH_KEY=/home/admin/.ssh/...`
  прожила незамеченной.
