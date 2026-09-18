# Установка Skygate

**Все поддерживаемые способы установки в одном месте.** Выберите строку из
таблицы, выполните её раздел, затем общие шаги после установки
([§10](#10-шаги-после-установки-для-любого-способа)).

English version: [`docs/INSTALL.md`](../INSTALL.md).
Обновление уже установленного: [`docs/ru/UPDATE.md`](UPDATE.md).
Эксплуатация (бэкап, восстановление, обновление, HA): [`docs/operations.md`](../operations.md).

---

## 1. Какой способ выбрать?

| № | Способ | Когда | Менеджер сервисов | БД по умолчанию |
|---|---|---|---|---|
| A | [Docker Compose, сборка из репозитория](#a-docker-compose-сборка-из-репозитория) | нужен весь стек (skygate + headscale + headplane + DERP), собранный из этого репозитория | docker | PostgreSQL (профиль `local-pg`) или SQLite |
| B | [Docker Compose, готовый образ](#b-docker-compose-готовый-образ-ghcr) | быстрая установка/обновление, без Go на хосте | docker | как зададите образу |
| C | [Docker Compose, SQLite в одном контейнере](#c-docker-compose-sqlite-в-одном-контейнере) | небольшой self-host, без внешнего PG | docker | SQLite |
| D | [Podman Compose](#d-podman-compose) | rootless-контейнеры | podman | как A/B |
| E | [Нативный systemd — Debian/Ubuntu](#e-нативный-systemd--debianubuntu) | skygate как служба хоста, без docker | systemd | SQLite |
| F | [Нативный systemd — RHEL-семейство](#f-нативный-systemd--rhel-семейство) | то же на RHEL/Rocky/Alma/Fedora/Amazon | systemd | SQLite |
| G | [Нативный OpenRC — Alpine](#g-нативный-openrc--alpine) | Alpine / хосты с OpenRC | OpenRC | SQLite |
| H | [Голый бинарь](#h-голый-бинарь-без-менеджера-сервисов) | свой супервизор (nohup, supervisord, runit, s6) | нет | SQLite |
| I | [Тарбол релиза вручную](#i-тарбол-релиза-вручную-linux-macos) | air-gapped / своя раскладка | ваш | SQLite или PG |
| J | [Windows](#j-windows) | Windows-хост, headscale доступен по сети | служба Windows | SQLite |

**Архитектура docker-образа:** публикуемый образ — **только linux/amd64**
(осознанное решение, см. `AGENTS.md`). На ARM используйте тарболы Go-бинаря
(способ I) либо `docker buildx build --platform linux/arm64` из исходников.

---

## 2. Что нужно до начала (для всех способов)

* **Контрол-плейн headscale**, доступный с хоста skygate, и API-ключ к нему.
  Skygate работает через API headscale; без ключа процесс падает с
  `config: HEADSCALE_API_KEY is required`.
* **Выбор БД**: SQLite (один хост, без зависимостей) или PostgreSQL
  (прод / HA). Для SQLite ничего дополнительно не нужно.
* **Секрет** для сессионных JWT. Нативные установщики генерируют его сами в
  env-файл; для docker положите в `.env`.
* Сеть: исходящий HTTPS к GitHub, если нужны проверка обновлений и
  самообновление (для air-gapped — зеркало, см. `UPDATE.md`).
* Порты: `8080` (веб-интерфейс), `50444` (API headscale, если он на этом же хосте).

Справочник переменных окружения: [`docs/deploy.md` §1](../deploy.md#1-environment).

---

## A. Docker Compose, сборка из репозитория

Бинарь собирается внутри контейнера при каждом старте (`entrypoint.sh`
выполняет `go build` по bind-mount репозитория).

```bash
git clone https://github.com/BarsSky/skygate.git
cd skygate
cp .env.example .env          # затем заполнить: HEADSCALE_URL, HEADSCALE_API_KEY, SKYGATE_JWT_SECRET, …
sudo bash deploy/deploy.sh    # рендер конфигов, сборка, запуск стека
curl -fsS http://127.0.0.1:8080/healthz
```

* `deploy/deploy.sh` проходит весь стек (каталоги/сеть, конфиг headscale,
  опциональный Caddy с TLS, контейнеры). `--from-path <dir>` указывает на
  существующий чекаут.
* Профили compose: `local-pg` включает встроенный PostgreSQL; `caddy` —
  встроенный TLS-терминатор (не нужен, если TLS терминирует внешний прокси,
  например Nginx Proxy Manager).
* **`docker compose` всегда запускайте из каталога проекта.** При вызове
  `docker compose -f /path/compose.yml` из другого каталога `.env` НЕ
  подхватывается для интерполяции, и плейсхолдеры молча берут значения по
  умолчанию — на эталонном развёртывании это уже приводило к поломке
  (см. `docs/LESSONS.md`).

## B. Docker Compose, готовый образ (ghcr)

Go на хосте не нужен — контейнер запускает опубликованный образ.

```bash
git clone https://github.com/BarsSky/skygate.git && cd skygate
cp .env.example .env          # HEADSCALE_URL, HEADSCALE_API_KEY, SKYGATE_JWT_SECRET, SKYGATE_DB, …
# зафиксируйте версию (рекомендуется) — тег обязан быть в НИЖНЕМ регистре:
echo 'SKYGATE_IMAGE=ghcr.io/barssky/skygate:v1.5.9' >> .env
docker compose -f docker-compose.ghcr.yml up -d
```

* `docker-compose.ghcr.yml` использует
  `${SKYGATE_IMAGE:-ghcr.io/barssky/skygate:latest}`.
* Образ только linux/amd64. На ARM — собирайте из исходников.
* Именно этот вариант ожидает кнопка «Pull образ» на `/admin/update`.

## C. Docker Compose, SQLite в одном контейнере

Один контейнер, один файл SQLite, без внешней БД.

```bash
cp .env.example .env          # HEADSCALE_URL, HEADSCALE_API_KEY, SKYGATE_JWT_SECRET
docker compose -f docker-compose.sqlite.yml up -d
```

`docker-compose.sqlite.yml` задаёт
`SKYGATE_DB=${SKYGATE_DB:-sqlite:/var/lib/skygate/skygate.db}` и хранит базу в
именованном томе. `docker-compose.lite.yml` — то же с более скромным набором
сервисов.

> **Для релизов ≤ v1.5.8:** форма DSN `sqlite:<путь>` вообще не открывалась
> (исправлено в v1.5.9). На старом бинаре используйте путь без схемы
> (`SKYGATE_DB=/var/lib/skygate/skygate.db`) или обновитесь.

## D. Podman Compose

Поддерживается, но не прогоняется в CI. Файлы те же, что в A/B:

```bash
podman compose -f docker-compose.ghcr.yml up -d
```

Учитывайте обычные отличия rootless: владелец bind-mount (при необходимости
`:Z`/`:U`) и `podman` вместо `docker` во всех командах этой документации.

## E. Нативный systemd — Debian/Ubuntu

Одной командой или отдельным скриптом:

```bash
# через диспетчер (сам определит дистрибутив)
sudo bash deploy/install.sh --db-type=sqlite
# либо напрямую
sudo bash deploy/install-debian.sh --db-type=sqlite
```

Флаги: `--db-type=sqlite|postgres`, `--import-existing=true|false`,
`--install-kind=systemd|bare|openrc`.

Что создаётся:

```
/usr/local/bin/skygate                             бинарь (из тарбола релиза)
/etc/systemd/system/skygate.service                юнит (User=skygate, ProtectSystem=strict)
/etc/skygate/skygate.env                           ваш конфиг (НЕ перезаписывается при повторной установке)
/etc/systemd/system/skygate-update.{path,service}  привилегированный помощник самообновления
/usr/local/lib/skygate/skygate-apply-update.sh     скрипт, который он запускает
/etc/skygate/update-helper.conf                    root-овый конфиг помощника
/var/lib/skygate/                                  данные (SQLite, ts/, update/)
```

Переменные установщика: `SKYGATE_VERSION` (`latest` или тег), `SKYGATE_PORT`,
`SKYGATE_USER`, `SKYGATE_DATA_DIR`, `SKYGATE_ETC_DIR`, `SKYGATE_BIN`,
`SKYGATE_SKIP_VERIFY=1` (не скачивать контрольные суммы — нужно для релизов
без ассета `SHA256SUMS`, т.е. ≤ v1.5.8), `SKYGATE_TS_*` (модуль Tailscale,
опционально).

Далее: заполнить `/etc/skygate/skygate.env` и

```bash
sudo systemctl restart skygate
systemctl is-active skygate && curl -fsS http://127.0.0.1:8080/healthz
```

## F. Нативный systemd — RHEL-семейство

То же, что E, но другой пакетный менеджер:

```bash
sudo bash deploy/install-rh.sh --install-kind=systemd
```

Особенности RHEL: `firewalld` не открывает `8080` автоматически (обычно
skygate ставят за прокси), а на старых системах SELinux может потребовать
`semanage port -a -t http_port_t -p tcp 8080`.

## G. Нативный OpenRC — Alpine

```bash
sudo bash deploy/install-alpine.sh --install-kind=openrc
```

Отличия от E:

* сервис — `/etc/init.d/skygate` + `/etc/conf.d/skygate` (последний
  подключает `/etc/skygate/skygate.env`), управление через
  `rc-service skygate start|stop|restart` и `rc-update add skygate default`;
* systemd path-юнита нет, поэтому помощник самообновления запускается через
  узкий drop-in `/etc/sudoers.d/skygate-update` и перезапускает сервис
  командой `rc-service` (`SKYGATE_UPDATE_MODE=openrc`);
* `SKYGATE_INSTALL_KIND=openrc`, `SKYGATE_UPDATE_STATE_PATH` и
  `SKYGATE_UPDATE_DIR` экспортируются из `/etc/conf.d/skygate`, чтобы
  `/admin/update` знал платформу без догадок.

## H. Голый бинарь (без менеджера сервисов)

```bash
sudo bash deploy/install-bare.sh --install-kind=bare
```

Ставит бинарь, env-файл и помощник обновления (sudoers drop-in) и печатает
три способа запуска: `nohup`, `systemd-run --unit=skygate` или сниппет для
`supervisord`. Запуск, перезапуск при падении и ротация логов — на вас.

`SKYGATE_UPDATE_MODE=bare` заставляет помощника остановить записанный PID и
запустить процесс заново командой из `/etc/skygate/update-helper.conf`
(`SKYGATE_UPDATE_BARE_START`).

## I. Тарбол релиза вручную (Linux, macOS)

Ассеты каждого релиза (см. страницу релиза):

```
skygate-vX.Y.Z-linux-amd64.tar.gz      skygate-vX.Y.Z-darwin-amd64.tar.gz
skygate-vX.Y.Z-linux-arm64.tar.gz      skygate-vX.Y.Z-darwin-arm64.tar.gz
skygate-vX.Y.Z-windows-amd64.zip       SHA256SUMS   (прикладывается начиная с v1.5.9)
```

```bash
curl -fsSLO https://github.com/BarsSky/skygate/releases/download/v1.5.9/skygate-v1.5.9-linux-amd64.tar.gz
curl -fsSLO https://github.com/BarsSky/skygate/releases/download/v1.5.9/SHA256SUMS
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf skygate-v1.5.9-linux-amd64.tar.gz     # плоско: ./skygate + ./skygate.sha256
sudo install -m 0755 skygate /usr/local/bin/skygate
```

Запустите под своим супервизором либо укажите на бинарь существующий
systemd/OpenRC-юнит, затем выполните §10. **Проверяйте контрольную сумму до
установки** — если у релиза нет `SHA256SUMS` (≤ v1.5.8), возьмите digest из
GitHub Releases API вместо отказа от проверки.

## J. Windows

```powershell
# PowerShell от администратора
.\deploy\Setup-Skygate-Win.ps1 -Version v1.5.9
# параметры: -Version -InstallDir ("C:\Program Files\Skygate") -DataDir ("C:\ProgramData\Skygate")
#            -GitHubOwner -GitHubRepo
```

Скрипт скачивает zip `windows-amd64`, ставит в `-InstallDir`, пишет env-файл
в `-DataDir` и регистрирует службу Windows. headscale должен быть доступен по
сети (`HEADSCALE_URL` не может быть именем контейнера).

---

## 10. Шаги после установки (для любого способа)

1. **Указать skygate на headscale.** `HEADSCALE_URL` (например
   `http://headscale:50444` внутри compose-сети или `http://127.0.0.1:50444`
   на хосте) и `HEADSCALE_API_KEY`. Оба обязательны — без ключа процесс не
   стартует.
2. **Выбрать БД.** `SKYGATE_DB=sqlite:/var/lib/skygate/skygate.db` (или путь
   без схемы) для self-host; `SKYGATE_DB=postgres://user:pass@host:5432/db?sslmode=disable`
   для PG. `SKYGATE_DB_DSN` — устаревшее имя (поддерживается, но приоритет ниже).
3. **Задать админа и секрет JWT** (`SKYGATE_JWT_SECRET`,
   `SKYGATE_ADMIN_USER`, `SKYGATE_ADMIN_PASS`).
4. **Перезапустить и проверить:**
   ```bash
   curl -fsS http://127.0.0.1:8080/healthz          # процесс жив + метка сборки
   curl -fsS http://127.0.0.1:8080/readyz           # БД + headscale + headplane + tailscale
   ```
5. **Открыть интерфейс** (`/login`) и зайти на `/admin/update`:
   * страница показывает **вид установки** и **панель платформы**
     (`systemctl` / `rc-service` / `docker` / наличие помощника);
   * на нативной установке должно быть «помощник установлен». Если написано
     «не установлен» — перезапустите установщик (идемпотентен, `skygate.env`
     сохраняет).
6. **Опционально:** модуль Tailscale (`deploy/scripts/install-tailscale.sh`),
   вход по OIDC ([`docs/oidc.md`](../oidc.md)), публичный HTTPS
   ([`docs/https.md`](../https.md)), свой DERP ([`docs/derp.md`](../derp.md) +
   `deploy/derp-init.sh`), HA/кластер ([`docs/ha.md`](../ha.md)).

## 11. Обновление

Все способы (пересборка compose, pull образа, автообновление по расписанию,
`/admin/update`, нативное самообновление с откатом, зеркала для air-gapped и
ручные шаги по каждому виду) — в [`docs/ru/UPDATE.md`](UPDATE.md).

## 12. Удаление

```bash
sudo bash deploy/scripts/cleanup-skygate.sh              # спросит подтверждение
sudo bash deploy/scripts/cleanup-skygate.sh --yes --dry-run
sudo bash deploy/scripts/cleanup-skygate.sh --keep-data --keep-config
```

Останавливает и отключает сервис, удаляет бинарь, сервисного пользователя и
runtime-каталоги; `/var/lib/skygate` сохраняется, если не указано иное.
Для docker: `docker compose down` (флаг `-v` — только если действительно
нужно удалить тома).

---

## 13. Диагностика самой установки

| Симптом | Причина / решение |
|---|---|
| `config: HEADSCALE_API_KEY is required` (цикл перезапусков) | в env-файле остался плейсхолдер — заполните `HEADSCALE_URL` + `HEADSCALE_API_KEY` и `systemctl restart skygate` |
| `docker compose` игнорирует значения `.env` | запуск из другого каталога (или с путём); сначала `cd` в каталог проекта либо укажите `--env-file` |
| Установка обрывается сразу после «installed: /usr/local/bin/skygate» | до v1.5.9: отсутствие `xxd` на минимальном хосте ломало `write_env_file`; обновите установщик или поставьте `xxd`/`openssl` |
| `/admin/tailscale`: `read auth key: … no such file` | контейнерный режим Tailscale без файла ключа; вставьте ключ на странице или задайте `SKYGATE_TS_AUTHKEY_FILE=/dev/null`, чтобы отключить осознанно |
| Проверка суммы падает / `SHA256SUMS` 404 | релизы ≤ v1.5.8 не публикуют `SHA256SUMS`; используйте digest из GitHub API либо осознанно `SKYGATE_SKIP_VERIFY=1` |
| `no matching manifest for linux/amd64` | скачан ARM-тег; образ только amd64 |
| `docker pull` отвергает тег | теги GHCR регистрозависимы и обязаны быть в нижнем регистре (`ghcr.io/barssky/…`) |
| Страница открывается, но все устройства offline | правило firewall/DOCKER-USER блокирует путь прокси → headscale (см. `docs/LESSONS.md`) |
