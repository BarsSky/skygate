# План: отладка авто-развертывания + покрытие тестами узких мест
**Дата:** 2026-09-09 (создан), 2026-09-09 (обновлён)
**Контекст:** Проверка автоматизации развертывания (новая копия skygate / дублирующий элемент кластера) на svi (replica, не несет ключевой нагрузки)
**Scope:** Только auto-deploy / auto-config. Глобальный рефакторинг в ядро+модули — отложен.

---

## 0. Текущее состояние (что имеем)

**Развернуто:**
- skygate (192.168.13.69, agent) — primary, skygate-host-1-1 (100.64.0.22) в Tailscale
- svi (<polygon-vm-public-ip>, <polygon-vm-hostname>) — standby/HA replica, 100.64.0.24
- karolina (193.233.130.178:18022) — российский VPS, jump host

**Что работает (подтверждено в сессии 2026-09-09):**
- B-check B-new (ha-state) — **73/73 PASS**
- B-check B-new-standby (provisioning) — **20/20 PASS**
- Всего в `verify_pre_deploy.sh` — **258 строк `run_check`** (161 файл `check_b*.sh`)
- skygate Tailscale mesh: karolina/emilia/sharlotta (3 пира)
- svi Tailscale mesh: karolina/emilia/sharlotta (те же 3 пира, НЕ skygate)
- Jump через karolina: `ssh svi` работает с skygate через ProxyCommand (настроен в `~/.ssh/config`)
- B175 Strategy E (OIDC auto-tag): работает на новых нодах
- B179 safety: `--netfilter-mode=nodir` (не `off`) — предотвращает iptables-ловушку
- karolina SSH config на skygate: `Host karolina/emilia/sharlotta/skyworker/base/skybars/svi` в `~/.ssh/config`

**Что НЕ работает (выявлено в этой сессии):**
- ❌ svi НЕ ВИДИТ skygate-host-1-1 в Tailscale mesh (grants policy фильтрует — нужен grant `tag:dev-skyadmin-skyworker ↔ tag:dev-infra-skygate-host-1-1`)
- ❌ <polygon-vm-public-ip> (публичный IP svi) НЕ достижим с skygate и с Windows-хоста — **блок на роутере оператора 192.168.13.1 или 192.168.1.254** (НЕ на svi, НЕ на хостере)
- ❌ svi в headscale user `tagged-devices` (synthetic, id=2147459555), а не в `infra` (id=85) — нужно re-provision или workaround
- ❌ headscale binary Go-side cache: после `UPDATE nodes SET user_id=85` в SQLite + `docker restart headscale` CLI продолжает показывать `tagged-devices`. **Workaround: re-provision через новый preauth flow**.

**Что проверено со стороны оператора (per user feedback 2026-09-09):**
- ✅ Из другой сети (не WiFi) <polygon-vm-public-ip> пингуется → **проблема точно в локалке оператора, не в svi**
- ✅ svi alive (operator пингует 95.165.170.190 с неё)
- ✅ `ssh svi` с skygate через karolina jump работает
- ❌ Прямой `ssh svi@100.64.0.24` не работает (Tailscale grants filter)

---

## 1. Модули проекта skygate (карта)

| Модуль | Где | Что делает | Узкое место в auto-deploy |
|---|---|---|---|
| **deploy/install-*.sh** | `deploy/install-debian.sh`, `install-alpine.sh`, `install-rh.sh`, `install-bare.sh`, `install-common.sh` | OS-уровневая установка: Docker, Tailscale, headscale, skygate binary, systemd | Все версии ОС, Tailscale in-container vs system, `apt install jq` обязателен |
| **deploy/deploy.sh** | `deploy/` | high-level orchestrator (install + bootstrap) | — |
| **deploy/oidc-sync.sh** | `deploy/` | B167: headscale OIDC config auto-sync (5 modes: docker/systemd/k8s/api/manual/download) | API URL changes, reg.ru DNS propagation, рестарт headscale |
| **deploy/setup-skygate-public.sh** | `deploy/scripts/` | B168: live OIDC на публичном hostname (nginx-skygate-oidc.conf) | DNS, certbot, reg.ru |
| **deploy/scripts/create-standby-preauth.sh** ⭐NEW | `deploy/scripts/` | B-new-standby: Tailscale preauth для нового standby | user name→id resolution, tagOwners, audit row |
| **deploy/headscale-bootstrap.sh** | `deploy/headscale-users/` | bootstrap headscale cluster (operator flow) | — |
| **deploy/backup.sh** | `deploy/` | S3 backup for skygate Postgres | — |
| **deploy/subnet-router/** | `deploy/subnet-router/` | Tailscale subnet router setup | preauth key TTL |
| **deploy/pg-ha/** | `deploy/pg-ha/` | Patroni + etcd + WAL-g setup | etcd cluster, replication |
| **scripts/bootstrap_standby.sh** | `scripts/` | B152: Phase 7 (bootstrap standby) | Tailscale auth (step 0 NEW), S3 pull, docker compose up |
| **scripts/dr_drill.sh** | `scripts/` | B153: Phase 9 (DR drill) | kill active, verify failover (60s timeout) |
| **scripts/rotate_ts_authkey.sh** | `scripts/` | B86: weekly rotate preauth key (cron) | `headscale preauthkeys create --user $HEADSCALE_USER_ID` |
| **scripts/ha-state/state.sh** ⭐ | `scripts/ha-state/` | B-new: state machine для ha-phase* | jq required, mkdir lock, fsync, 5-min stale lock |
| **scripts/ha-phase0.sh** ⭐ | `scripts/` | B-new: Tailscale mesh prerequisite (6 steps) | depends on ssh + tailscale + headscale CLI |
| **scripts/ha-phase7.sh** ⭐ | `scripts/` | B-new: state-tracked wrapper над bootstrap_standby.sh | delegates to bootstrap_standby.sh |
| **scripts/ha-phase9.sh** ⭐ | `scripts/` | B-new: state-tracked wrapper над dr_drill.sh | destructive, max 2 attempts |
| **scripts/ha-status.sh** | `scripts/` | B-new: state summary + crash marker + audit log tail | jq, /var/lib/skygate/ha-state/ |
| **scripts/init-headplane.sh** | `scripts/` | B151: auto-apply headplane API key | bundled vs external headplane |
| **scripts/init-skygate.sh** | `scripts/` | (нет — есть `deploy.sh` и `bootstrap_standby.sh`) | primary vs standby flow разный |
| **scripts/verify_pre_deploy.sh** | `scripts/` | каталог B-checkов (258 строк `run_check`) | добавить новые B-check, .git/hooks |
| **scripts/tailnet_probe.sh** | `scripts/` | tailscale state diagnostic | — |
| **scripts/recover_db_corruption.sh** | `scripts/` | DB corruption recovery (for HA setups) | — |
| **scripts/fix_tailnet_split.sh** | `scripts/` | Tailscale ACL split-brain recovery | — |
| **internal/acl/acl.go** | `internal/acl/` | v1.5.0 policy generation | `qSelectPerUserDeviceTags` JOIN с portal_users |
| **internal/acl/acl_perdevice.go** | `internal/acl/` | B-new: per-DEVICE grant block | `getInfraExitNodeTags` (фильтрует skygate-*) |
| **internal/db/queries.go** | `internal/db/` | SQL запросы (qSelectPerUserDeviceTags и др.) | JOIN semantic на portal_users |
| **internal/db/node_owner_map.go** | `internal/db/` | B175: per-device owner map | `UpsertNodeOwner`, `SyncTagsFromHeadscale`, `SyncNodesFromHeadscale` |
| **internal/db/preauth_keys.go** | `internal/db/` | B86: Tailscale preauth key tracking | notification when expires (14d buffer) |
| **internal/handlers/handlers_export.go** | `internal/handlers/` | B175: `BackfillNodeOwnershipFn` | called on /my/devices load |
| **internal/feature/admin/devices.go** | `internal/feature/admin/` | B169: admin device delete | uses `headscale.DeleteNode` |
| **internal/feature/admin/devices.go** | `internal/feature/admin/` | B-new: `PostAdminDevicesSyncFromHeadscale` | кнопка "Sync from headscale" в /admin/devices |
| **internal/feature/cluster/** | `internal/feature/cluster/` | B200+: cluster mgmt (cluster/invite/join/node/discovery/upgrade) | B202.5 SSHDumpTransport |
| **internal/cluster/invite_b200_test.go** | `internal/cluster/` | B200+ invite flow test (использует svi-direct-1/2 фикстуры) | — |
| **internal/cluster/upgrade_b222_test.go** | `internal/cluster/` | B222+ upgrade test | — |
| **internal/dbmigrate/** | `internal/dbmigrate/` | B202.5: cross-host DB migration | ssh "svi" (Tailscale MagicDNS, root) |
| **internal/nodeownership/** | `internal/nodeownership/` | B175: node ownership + Strategy E (OIDC) | backfillNodeOwnership |
| **internal/oidc/** | `internal/oidc/` | B161: OIDC provider (authcode/jwt/etc) | `head.skynas.ru` URL, JWKS, RS256 |
| **internal/headscale/** | `internal/headscale/` | headscale API client | ListAllNodes, DeleteNode |
| **internal/tailscale/** | `internal/tailscale/` | tailscale API client | peer status, netcheck |
| **.githooks/pre-commit** | `.githooks/` | credential guard (192.168.13.69, skygate_admin_pass) | bypass через --no-verify |

⭐ = B-new / B-new-standby (только что добавлено)

---

## 2. Узкие места (narrow places) в auto-deploy

### 2.1. Tailscale provisioning
- **`--user` flag expects numeric ID, not name** (B191 fix). Если передать "infra" вместо "85" — headscale примет, но в `GetPerUserDeviceTags` JOIN пропустит. `create-standby-preauth.sh` решает это через `headscale users list -o json` + name→id resolution.
- **Preauth key expiration**: 1h default, 24h для медленных сетей (svyatoslova-1 поднятие 60+ мин). B86 rotate weekly с 14d buffer.
- **`--netfilter-mode=off` trap**: B179 — оставляет stale ts-input chain. Использовать `nodir` (NEW B-new-standby).
- **Tag attachment at auth time**: ACL tags должны быть в preauth command (B175 Strategy E), а не добавляться через `headscale nodes tag` после.
- **headscale 0.29.1 не имеет `nodes move` CLI** (verified 2026-09-09) — нельзя перенести ноду в другой user после создания. Только re-provision.

### 2.2. Headscale user mapping (КРИТИЧНО — основной bug)
- **`tagged-devices` user trap**: если preauth key без `--user`, нода попадает в synthetic user (id=2147459555, sentinel). JOIN `qSelectPerUserDeviceTags` с `portal_users` его не видит → нода не в grants. **Это основная причина почему svi не видит skygate в mesh.**
- **tagOwners ownership**: tag `tag:dev-infra-<polygon-vm-hostname>` должен быть в `tagOwners` для `infra@tsnet.skynas.ru` (а не `skyadmin@`). Если user переехал, но tagOwners не обновлен — `headscale nodes tag --force` сработает, но grants не будут использовать.
- **headscale binary Go-side cache** (B237-mystery, verified 2026-09-09): `docker restart headscale` не сбрасывает кэш user. DB показывает `user_id=85`, CLI показывает `tagged-devices`. **Workaround: re-provision через new preauth flow, не DB UPDATE.**
- **node 2147459555** (synthetic `tagged-devices` user) — не появляется в `headscale users list` (только 6 реальных users), но CLI его возвращает для нод без валидного user mapping.

### 2.3. Policy generation
- **`writePerDeviceGrants` skip rule**: `if len(userTags) < 2 { continue }` — user с 0 или 1 device не получает grants. svi была одной нодой skyadmin → не было grant.
- **getInfraExitNodeTags skip `tag:dev-infra-skygate-*`**: фильтрует skygate хосты из catch-all `* → tag:dev-infra-*`. Это правильно для exit-node catch-all, но НЕ для per-DEVICE grants (там skygate должен быть DST).
- **ACLS count = 0 в policy**: все grants в `grants[]`, `acls[]` пустой. Это by design (grants-only policy), но ломает старый `headscale policy set` который ожидает `acls`.
- **Per-CIDR `via=` pin**: exit_node_pref + rule.exit_node_id должны совпадать. Если operator поменяет pref, grant становится more specific (правильно, но нужно тестировать).
- **25 grants в текущей policy** (verified 2026-09-09): 5 per-user (skyadmin/michail/guest/daniil/infra), 7 per-device для skyadmin-*, 3 per-device для michail-*, 5 per-device для infra-*, 3 catch-all `* → tag:dev-infra-<exit>`, 2 per-device with `via=`.

### 2.4. Bootstrap standby flow
- **⚠️ Tailscale NOT installed by `install-debian.sh`** (verified 2026-09-09). `install-debian.sh` ставит только skygate binary + systemd, **НЕ ставит Tailscale**. `bootstrap_standby.sh` step 0 (NEW B-new-standby) делает `command -v tailscale || die "tailscale CLI not found — install tailscale first"`. На свежем VM после `install-debian.sh` step 0 **упадёт**.
  - **Tailscale НЕТ в дефолтных Debian репах** (verified https://tailscale.com/download/linux/debian-bookworm) — нужно сначала добавить Tailscale's own apt repo:
    ```bash
    curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.noarmor.gpg | sudo tee /usr/share/keyrings/tailscale-archive-keyring.gpg >/dev/null
    curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.tailscale-keyring.list | sudo tee /etc/apt/sources.list.d/tailscale.list
    sudo apt-get update && sudo apt-get install -y tailscale
    ```
  - **Phase 7 fix**: добавить этот блок в `install-debian.sh` ПОСЛЕ существующего `apt-get install` (строки 60-67) — ДО `enable_and_start_service`. Также `install-alpine.sh` (apk) и `install-rh.sh` (dnf) — добавить эквивалент для каждой ОС.
  - **Альтернатива**: добавить pre-check в `bootstrap_standby.sh` step 0: `if ! command -v tailscale; then install via curl; fi`. Менее чисто, но fallback для existing bootstrap.
  - **Документация в коде**: `install-common.sh:310` имеет comment "the systemd install path doesn't use Tailscale at all" — **OUTDATED** после B-new-standby. Нужно обновить comment.
  - **Subnet-router path** (`deploy/subnet-router/setup.sh:9`): "The host must already have tailscale installed" — explicit pre-req, но не enforced.
- **Docker NOT installed** — аналогично. `bootstrap_standby.sh` требует `docker` и `docker compose`.
- **S3 credentials** — pre-existing, должны быть в `.env` от primary. Без них `bootstrap_standby.sh` падает на step 2 (silently — non-fatal warning).
- **HEADPLANE_HEADSCALE__API_KEY** — копируется из primary `.env`. Если истек — re-init на primary через `scripts/init-headplane.sh`.
- **/healthz check timeout** — 60s default. На медленных VM может не хватить. Нужно настраиваемое.
- **DNS for svi hostname** — `bootstrap_standby.sh` использует `$(hostname)` для S3 path. Если svi-hostname != <polygon-vm-hostname>, S3 pull fail.

### 2.5. HA state machine
- **jq required** — без jq все ha-phase*.sh падают. `apt install jq` в `install-common.sh` обязателен (B-check I проверяет).
- **`/var/lib/skygate/ha-state/`** — должен существовать с правильным owner. На чистой VM нет — нужен pre-step. ⚠️ Нужно добавить в `bootstrap_standby.sh` или `install-debian.sh`!
- **Crash detection false positive** — если `tailscale up` прерывается (Ctrl-C), state остается `running`, при следующем запуске `ha_state_crash_check` помечает phase как `failed`. Operator должен знать что делать (`--reset`).
- **Step-level status** — `ha_state_run_step` пишет в state file. Если state file corrupt (JSON parse error), весь phase fail. Нужен `jq .` validation.
- **5-min stale lock detection** — `ha_state_lock` ждёт 5 мин, потом автоматически берёт lock. ⚠️ Если процесс реально работает 5+ мин, может потерять lock.

### 2.6. DR drill
- **`docker kill -9 skygate-skygate-1`** — реально kill, требует auto-recovery через Patroni. На тестовой VM может занять >60s.
- **DNS propagation** — reg.ru API. На тесте не критично, но в продакшене — 5-15 мин propagation delay.
- **`skygate ha reclaim`** — не auto, требует operator manual action (per Decision #11).
- **Auto `--yes` mode в ha-phase9** — max 2 attempts, exponential backoff. Уже реализовано (NEW B-new).

### 2.7. Pre-deploy verification
- **`.githooks/pre-commit` active** — требует `git config core.hooksPath .githooks` после клонирования. Если забыли — credentials не guard.
- **Pre-commit hook blocks**: `192.168.13.69`, `skygate_admin_pass`. `<polygon-vm-public-ip>` НЕ блокируется (проверено). Use `--no-verify` как workaround.
- **B-check outdated** — если новый скрипт добавлен без записи в `verify_pre_deploy.sh`, он не запустится в CI/pre-deploy. (см. как B-new + B-new-standby зарегистрированы — 258 строк run_check).
- **B-check на приватные IP** — `check_b_standby_provision.sh` НЕ проверяет что код не содержит `<polygon-vm-public-ip>`. ⚠️ Можно добавить как новый контракт.

### 2.8. Network-layer blocks (operator's local network, not skygate)
- **Operator's gateway 192.168.13.1 / 192.168.1.254 блокирует <polygon-vm-public-ip>** (verified 2026-09-09). Не наш scope, но документируем в runbook как "operator action required".
- **Workaround**: `ssh svi` через karolina jump (ProxyCommand в `~/.ssh/config`).
- **Tailscale mesh на svi** — svi видит karolina/emilia/sharlotta (3 exit nodes), НЕ видит skygate-host-1-1 (grants filter).
- **Tailscale mesh на skygate** — skygate видит karolina/emilia/sharlotta (те же 3 exit nodes), НЕ видит svi (та же grants filter).

---

## 3. План тестирования (по фазам)

### Phase 0: Audit current state (на skygate + svi)
**Цель:** понять что есть сейчас, задокументировать gaps.

**Шаги:**
1. На skygate: запустить все 258 B-checks (`bash scripts/verify_pre_deploy.sh`). Записать pass/fail.
2. На skygate: `docker ps`, `docker exec headscale headscale nodes list`, `docker exec headscale headscale policy get` — задокументировать state.
3. На svi (через jump): `docker ps`, `tailscale status`, `systemctl status tailscaled` — задокументировать state.
4. Проверить reachability: ping <polygon-vm-public-ip> с skygate, ping 192.168.13.69 с svi. Задокументировать что работает/не работает.
5. Снять "baseline" — текущее состояние до изменений.
6. Сохранить backup: `headscale policy get -o json > /tmp/policy.baseline.json` (восстановить при rollback).

**Deliverable:** `docs/internal/auto-deploy-baseline-2026-09-09.md` с таблицей "что работает / что не работает".

### Phase 1: Tailscale grants policy validation
**Цель:** убедиться что grants между svi и skygate корректны (или выявить gaps).

**Шаги:**
1. На svi (через jump): `tailscale ping 100.64.0.22` (skygate). Должен быть `pong`. Если `no matching peer` — grants bug.
2. На skygate: `tailscale ping 100.64.0.24` (svi). Аналогично.
3. Сравнить текущий policy (`headscale policy get -o json`) с ожидаемым. Должны быть grants:
   - `tag:dev-skyadmin-skyworker (or skyworker) ↔ tag:dev-infra-skygate-host-1-1`
   - `tag:dev-infra-skygate-host-1-1 ↔ tag:dev-skyworker`
4. Если grants нет — это bug, который нужно починить (либо в policy, либо в headscale state).
5. `headplane` /admin/devices → "Sync from headscale" → проверить что `node_owner_map` обновлен.

**Deliverable:** отчет "Tailscale grants validation" + список gaps.

### Phase 2: Re-deploy svi через bootstrap_standby.sh (с новым step 0)
**Цель:** проверить что новая автоматизация Tailscale auth работает end-to-end.

**Предусловия:** svi взята из эксплуатации (operator останавливает Patroni replica).

**Шаги:**
1. На skygate: `NEW_KEY=$(bash deploy/scripts/create-standby-preauth.sh --hostname <polygon-vm-hostname>-test)` — минт test key.
2. На svi: `export SKYGATE_STANDBY_TS_AUTHKEY="$NEW_KEY"` + `bash scripts/bootstrap_standby.sh --reset` — re-bootstrap.
3. Проверить step 0 выполнение:
   - tailscale status был Running → skip ✓
   - tailscale status NoState → tailscale up выполнился ✓
4. Проверить step 1-6 (S3, docker compose, healthz, etcd).
5. На skygate: `docker exec headscale headscale nodes list` — svi должна быть в user `infra` (id=85), не `tagged-devices`.
6. Если user правильный → force policy reapply (через UI или restart skygate) и проверить grants.

**Deliverable:** отчет "bootstrap_standby.sh re-deploy test" + pass/fail по step 0-6.

### Phase 3: Create-standby-preauth.sh validation
**Цель:** убедиться что новый preauth script работает для разных user/tag комбинаций.

**Шаги:**
1. `bash deploy/scripts/create-standby-preauth.sh --hostname test-svi-a --user infra` — проверка default.
2. `bash deploy/scripts/create-standby-preauth.sh --hostname test-svi-b --user skyadmin` — проверка override.
3. `bash deploy/scripts/create-standby-preauth.sh --hostname test-svi-c --user 85` — numeric ID.
4. `bash deploy/scripts/create-standby-preauth.sh --hostname test-svi-d` (no --user) — default 85.
5. `bash deploy/scripts/create-standby-preauth.sh --hostname test-svi-e --user nonexistent` — error case.
6. `bash deploy/scripts/create-standby-preauth.sh --help` — usage.
7. Проверить audit_log: `SELECT * FROM audit_log WHERE action='ha.preauth.create' ORDER BY created_at DESC LIMIT 5`.
8. Проверить key не сразу consumed (т.е. test key не использовался).
9. `bash deploy/scripts/create-standby-preauth.sh --dry-run --hostname test-svi-f` — проверка dry-run mode.

**Deliverable:** отчет "create-standby-preauth.sh validation" + pass/fail по 9 тестам.

### Phase 4: Headscale policy reapply + grant verification
**Цель:** после Phase 2-3 убедиться что per-DEVICE grants правильно включают новые ноды.

**Шаги:**
1. Force policy regen — через UI или restart skygate (что триггерит policy apply).
2. `docker exec headscale headscale policy check -f /tmp/policy.json` — должна пройти без errors.
3. `headscale policy get -o json` → проверить что:
   - Есть grant `tag:dev-skyadmin-skyworker` (или новый тег) с DST включающим svi
   - Есть grant `tag:dev-infra-<polygon-vm-hostname> ↔ tag:dev-infra-skygate-host-1-1`
4. На svi: `tailscale ping 100.64.0.22` — должен быть `pong`.
5. На skygate: `tailscale ping 100.64.0.24` — должен быть `pong`.
6. SSH test: `ssh svi echo OK` из skygate через Tailscale (не через jump).
7. SSH test: `ssh root@100.64.0.22 echo OK` из svi через Tailscale.

**Deliverable:** отчет "Tailscale grants E2E" — pass/fail по 5 проверкам.

### Phase 5: Fresh skygate deploy (на отдельной VM)
**Цель:** убедиться что deploy.sh + install-common.sh + install-debian.sh работают с нуля.

**Предусловия:** новая VM (DigitalOcean/Hetzner), Ubuntu 24.04, SSH ключ.

**Шаги:**
1. Клонировать repo.
2. Запустить `deploy/install-debian.sh` — должен установить Docker, Tailscale, headscale, skygate binary, systemd service.
3. Запустить `deploy/scripts/create-standby-preauth.sh --hostname new-skygate` — минт key.
4. Настроить `.env` (HEADSCALE_URL, HEADSCALE_API_KEY, etc.).
5. `bash scripts/bootstrap_standby.sh` (или для primary — `bash deploy/deploy.sh`).
6. Проверить `/healthz` → 200.
7. Проверить Tailscale status → нода в mesh, видит skygate-host-1-1.
8. Проверить grants → svi видна новой skygate.

**Deliverable:** отчет "fresh skygate deploy" + timing/sequence каждого шага.

### Phase 6: HA DR drill
**Цель:** убедиться что DR drill работает в новой автоматизации.

**⚠️ ВАЖНО:** Phase 6 требует **maintenance window** — реально убивает skygate-skygate-1 на primary. НЕ делать в production без согласования.

**Шаги:**
1. `bash scripts/ha-phase9.sh` (auto --yes) — должно:
   - Verify Phase 0+7 completed
   - Run dr_drill.sh с auto --yes
   - Parse PASS count
2. `bash scripts/dr_drill.sh` (manual) — должен:
   - kill -9 skygate-skygate-1 на primary
   - Verify standby (svi) takes over в <60s
   - Restart primary, verify no-flap rejoin
3. Verify `skygate ha status` показывает правильный elector state.

**Deliverable:** отчет "DR drill validation" + timing failover.

### Phase 7: Regression tests + automation coverage
**Цель:** убедиться что все B-checks покрывают критические узкие места, добавить где пробелы.

**Шаги:**
1. Audit существующих B-checks (top-10 для auto-deploy):
   - `check_b86.sh` — preauth rotate
   - `check_b145.sh` — HA chain
   - `check_b149.sh` — /admin/ha
   - `check_b150.sh` — /admin/deploy
   - `check_b151.sh` — init-headplane
   - `check_b152.sh` — bootstrap_standby
   - `check_b153.sh` — dr_drill
   - `check_b167.sh` — OIDC auto-sync
   - `check_b168.sh` — live OIDC
   - `check_b191.sh` — device registration
   - `check_b202_5.sh` — SSHDumpTransport
   - `check_b_new.sh` — ha-state machine (73 contracts)
   - `check_b_standby_provision.sh` — provisioning (20 contracts)
2. Identify gaps — что НЕ покрыто B-checkами:
   - Tailscale grants policy (per-DEVICE grant после reapply)
   - node_owner_map ↔ portal_users JOIN
   - headscale user mapping для новых нод (id != tagged-devices sentinel)
   - tagOwners coverage для тегов
   - iptables trap (B179)
   - operator's local network block (не наш scope, но документируем)
3. Добавить новые B-checks где пробелы:
   - `check_b_tailscale_grants.sh` — emit per-DEVICE grants и проверить что новые ноды попадают
   - `check_b_node_owner_map.sh` — все ноды в node_owner_map с правильным username
   - `check_b_tag_owners.sh` — все `tag:dev-*` теги в `tagOwners`
   - `check_b_45_152_198.sh` — нет <polygon-vm-public-ip> в tracked code (вместо этого — env vars)
4. Запустить на skygate + svi.

**Deliverable:** новые B-check скрипты + полный каталог B-checks в `scripts/`.

---

## 4. Стратегия использования svi

**svi = <polygon-vm-hostname>, 100.64.0.24, текущее состояние:**
- etcd запущен (skygate-etcd container)
- Tailscale Running, user `tagged-devices` (НЕ `infra`)
- HA chain role: standby (предположительно)
- Patroni replica: возможно, нужно проверить
- НЕ критическая нагрузка (per user)

**Использование:**
- Phase 0: read-only на svi, без выключения.
- Phase 1: read-only.
- Phase 2: re-deploy svi — ОСТАНОВИТЬ Patroni replica, сделать test, восстановить.
- Phase 3: dry-run mode create-standby-preauth (не требует реального использования).
- Phase 4: depends on Phase 2 result.
- Phase 5: НЕ делать на svi (требует новую VM).
- Phase 6: НЕ делать в production (DR drill требует kill primary).

**Safeguards:**
- Перед Phase 2 — backup svi state (snapshot etcd, dump node_owner_map, dump policy).
- После Phase 2 — rollback plan (re-deploy svi с старой конфигурацией если новый flow fail).
- Время тестирования: 1-2 часа max, off-hours.

---

## 5. Метрики успеха

После прохождения всех фаз должны быть:
- **Все 7 фаз зелёные** (pass/fail задокументирован)
- **Новые B-check скрипты** в `scripts/check_b_*.sh` для покрытия gaps (target: 4 новых файла)
- **Обновлённый `docs/internal/auto-deploy-baseline-2026-09-09.md`** с финальным состоянием
- **CI pipeline** (если есть) запускает все B-check на каждый PR
- **Operator runbook** в `docs/internal/HA-STANDBY-PROVISIONING.md` (новый файл) с end-to-end flow
- **Headscale grants work** (svi ↔ skygate видят друг друга)
- **Tailscale auth auto** (svi registered правильно, user=infra)

---

## 6. Что НЕ делаем (deferred)

- **Глобальный рефакторинг** в ядро+модули (kernel + plugin API) — это **на будущее**, после стабилизации auto-deploy
- **CI/CD pipeline** полный (только если есть потребность) — пока manual B-check run
- **Метрики / observability** (Prometheus, Grafana) — пока логи в `/var/log/skygate/`
- **Multi-region / multi-DC** — не в scope текущего проекта
- **svi direct Tailscale mesh fix** (tagged-devices → infra user) — handled by Phase 2 (re-deploy), НЕ by DB UPDATE
- **Operator's local network block (<polygon-vm-public-ip>)** — operator action, document as known issue

---

## 7. Timeline (предложение)

- **Phase 0-1 (audit + grants validation):** 1-2 часа
- **Phase 2-3 (re-deploy + create-standby-preauth tests):** 2-3 часа (включая re-provisioning svi)
- **Phase 4 (policy reapply + E2E grants):** 1 час
- **Phase 5 (fresh skygate deploy):** 2-3 часа (нужна отдельная VM)
- **Phase 6 (DR drill):** 30-60 мин (в maintenance window)
- **Phase 7 (regression tests + new B-checks):** 2-3 часа

**Итого:** 1-2 дня работы, в зависимости от доступности VM и maintenance window.

---

## 8. Rollback plan

Если что-то пошло не так на Phase 2 (re-deploy svi):
1. На svi: остановить docker compose
2. Восстановить из snapshot (etcd + node_owner_map)
3. На skygate: удалить новую preauth key (`headscale preauthkeys delete`)
4. Восстановить policy (`headscale policy set -f policy.baseline.json`)

Если что-то пошло не так на Phase 5 (fresh deploy):
1. Удалить VM (Terraform/cloud console)
2. Никаких side-effects на skygate (новая VM изолирована)

Если что-то пошло не так на Phase 6 (DR drill):
1. Скрипт dr_drill.sh сам делает rollback (restart primary)
2. Если не сработало — manual `skygate ha status` + ручной failover

---

## 9. Known issues (out of scope, документируем)

- **Operator's gateway блокирует <polygon-vm-public-ip>** — нужно проверить firewall на 192.168.13.1 или 192.168.1.254. Workaround: `ssh svi` через karolina jump.
- **headscale binary Go-side cache** — workaround: re-provision (не DB UPDATE).
- **node 2147459555** (`tagged-devices` sentinel) — не реальный user, не появляется в `headscale users list`.
- **Tailscale grants на svi ↔ skygate** — отсутствуют (Phase 1-4 закроют).
- **`/var/lib/skygate/ha-state/` создание** — не покрыто ни `install-debian.sh`, ни `bootstrap_standby.sh`. Phase 7 кандидат на добавление.
- **Tailscale/Docker install на чистой VM** — не покрыто `bootstrap_standby.sh` step 0. Нужно добавить pre-step в `install-debian.sh` или перед bootstrap.
  - **Tailscale** (verified 2026-09-09): `install-debian.sh` НЕ устанавливает Tailscale. Tailscale package не в дефолтных Debian репах, нужен свой apt-repo (`https://pkgs.tailscale.com/stable/debian/bookworm`). **Fix**: добавить в `install-debian.sh` после строки 67 (после `apt-get install ... systemd`):
    ```bash
    # Tailscale (для bootstrap_standby.sh step 0)
    curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.noarmor.gpg | sudo tee /usr/share/keyrings/tailscale-archive-keyring.gpg >/dev/null
    curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.tailscale-keyring.list | sudo tee /etc/apt/sources.list.d/tailscale.list
    sudo apt-get update -qq && sudo apt-get install -y tailscale
    ```
  - **Аналогично для `install-alpine.sh`** (apk add tailscale после добавления community repo) и **`install-rh.sh`** (dnf install tailscale после добавления tailscale.repo).
  - **Pre-check в `bootstrap_standby.sh` step 0** (fallback): если `command -v tailscale` fail — установить через `curl -fsSL https://tailscale.com/install.sh | sudo sh`.
