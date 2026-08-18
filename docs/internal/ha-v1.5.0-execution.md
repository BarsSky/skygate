# v1.5.0 — HA skygate-prod + skygate-standby (BL-2) — Execution Tracker

**Status**: UNBLOCKED (2nd VM `svyatoslava-1` available, S3 configured, Patroni + etcd already in place)
**Target release**: v1.5.0
**Started**: 2026-08-18
**Author**: Mavis (skygate)
**Operator**: admin
**Tracker type**: Phase-by-phase checklist with locked decisions + open questions

---

## 1. Locked-in decisions (do not revisit without operator sign-off)

| # | Decision | Source | Why locked |
|---|---|---|---|
| 1 | **DNS-side hostname**: `skygate.skynas.ru` is the public FQDN | operator reply 2026-08-18 | reg.ru is the registrar, no Cloudflare |
| 2 | **Active node hostname**: `skygate` (in headscale + Tailscale MagicDNS) | operator reply 2026-08-18 | "skygate-prod" was confusing; operator prefers `skygate` |
| 3 | **Standby node hostname**: `skygate-standby` | derived from #2 | consistent naming |
| 4 | **Tailscale Funnel: NO** | operator reply 2026-08-18 | "сеть tailscale скорейвсего не доступна" + headscale != Tailscale |
| 5 | **External DNS via reg.ru API** | reg.ru is the registrar | no Cloudflare dependency, reg.ru has its own API |
| 6 | **Active-Passive with priority chain** (not Active-Active) | operator reply 2026-08-18 | "стоит учесть что дубликатов может быть несколько и стоит делать сразу с учетом того что они могут иметь приоритет" |
| 7 | **Starter chain: 2 nodes** (P1=`skygate`, P2=`skygate-standby`) | operator reply 2026-08-18 | "Starter chain пока из двух как ты указал" |
| 8 | **Active = svyatoslava-1 (the new VM)**, current `95.165.170.190` becomes standby | operator reply 2026-08-18 | "текуший проект на VM передет на svyatoslava-1 и будет основным а текущий активный станет дублером" |
| 9 | **Public IP on svyatoslava: yes** | operator reply 2026-08-18 | "public ip на svyatoslava есть" |
| 10 | **Failover: auto + manual override** | operator reply 2026-08-18 | "сделал авто но оставил возможность и в ручную" |
| 11 | **Auto-reclaim: default OFF** (when P1 returns, no auto-flip; manual "Reclaim primary" button) | derived from operator's anti-flap intent | avoids flap |
| 12 | **Patroni config: NOT touched** | operator reply 2026-08-18 | "Зачем мучаться с перенастройкой Patroni есть ли в этом смысли если сейчас уже все настроено" |
| 13 | **S3 prefix: `s3://skygate-backups/ha/`** (revised 2026-08-18) | reusing existing backup bucket (`skygate-backups` at `http://172.18.0.5:9000`) with same IAM creds (`skygate-test` / `skygate-test-pass-2026`); no new bucket or IAM needed |
| 14 | **Cert acquisition: reg.ru DNS-01 via Caddy plugin** | reg.ru has RFC 2136 support | no Cloudflare dep |
| 15 | **Cert acquisition fallback: user upload via /admin/certificates** | operator asked for "прием их через загрузку от пользователя" | works without external DNS dep |
| 16 | **headplane API key: in `.env` (file-based), replicated via S3 deploy/** | operator: "при развертывании с нуля если он развертывается в единой докер системе с skygate и headscale то прописывается и применяется автоматом" | simpler than DB-encrypted storage |

---

## 2. State of the world (today, 2026-08-18)

| Component | Current | Target |
|---|---|---|
| Primary VM | `192.168.13.69` (public `95.165.170.190`) — runs headscale + skygate + PG-primary | **becomes `skygate-standby`** (P2) |
| Standby VM | none | **`svyatoslava-1`** — new skygate-prod (P1) |
| Patroni | running async replication, etcd at `45.152.198.217:2379` | unchanged (per decision #12) |
| External DNS | unknown (need to confirm reg.ru API access) | reg.ru API client in `internal/dns/regapi/` |
| Tailscale/headscale | tsnet.skynas.ru base domain, `head.skynas.ru` API | unchanged; add `skygate` and `skygate-standby` node identities |
| TLS cert | currently self-managed (Caddy OFF per v0.32.11) | reg.ru DNS-01 via Caddy plugin (decision #14) |
| Backup | S3 already configured (per B142 / backup system) | add `s3://skygate-backups/ha/` prefix for HA state (decision #13) |

---

## 3. Implementation phases (each is a release candidate or grouped with v1.5.0)

Legend: `[ ]` pending · `[~]` in progress · `[x]` done · `[!]` blocked

### Phase 1: HA chain + elector
- [ ] `internal/ha/chain.go` — `HaChain` struct + `HaMember` list (priority-ordered)
- [ ] `internal/ha/elector.go` — Patroni-derived role + heartbeat (5s) + missed-threshold (3 = 15s)
- [ ] `internal/ha/storage.go` — read/write `global_settings.ha_chain` (JSON-encoded members)
- [ ] `internal/ha/elector_test.go` — pure-Go unit tests for chain ordering
- [ ] `cmd/skygate/main.go` — wire `StartElector(ctx, deps)` after Patroni-detected PG primary
- [ ] `internal/config/config.go` — `SKYGATE_HA_ROLE` (active/standby/auto) + `SKYGATE_HA_PEER_HOSTNAME` + `SKYGATE_HA_HEARTBEAT_INTERVAL` (default 5s) + `SKYGATE_HA_MISSED_THRESHOLD` (default 3)
- [ ] `scripts/check_b145.sh` (6 contracts)

### Phase 2: reg.ru DNS client + failover
- [ ] `internal/dns/regapi/` — `RegAPIClient` + `UpdateRecord(zone, name, type, value, ttl)`
- [ ] `internal/dns/regapi/regapi_test.go` — mock HTTP server
- [ ] `internal/ha/dns_failover.go` — on role change (active/standby flip), call reg.ru API to update A-record
- [ ] `internal/config/config.go` — `SKYGATE_DNS_PROVIDER=regapi` + `SKYGATE_DNS_REGAPI_USER` + `SKYGATE_DNS_REGAPI_PASSWORD` + `SKYGATE_DNS_REGAPI_ZONE`
- [ ] `scripts/check_b146.sh` (5 contracts)

### Phase 3: certsync (S3 ↔ local certs)
- [ ] `internal/certsync/certsync.go` — 30s tick: HEAD S3 `.version` + pull newer .pem/.key + reload Caddy
- [ ] `internal/certsync/certsync_test.go` — version-bump + Caddy reload stub
- [ ] `cmd/skygate/main.go` — `StartCertSync(ctx, deps)` (mirrors B130/B142 pattern)
- [ ] `internal/config/config.go` — `SKYGATE_CERTSYNC_ENABLED` + `SKYGATE_CERTSYNC_S3_BUCKET` (default `s3://skygate-backups/ha/certs/`) + `SKYGATE_CERTSYNC_LOCAL_DIR` (default `/var/lib/skygate/certs/`) + `SKYGATE_CERTSYNC_INTERVAL` (default 30s)
- [ ] `scripts/check_b147.sh` (5 contracts)

### Phase 4: /admin/certificates (upload + reg.ru DNS-01 toggle)
- [ ] `internal/feature/admin/certificates.go` — `GetAdminCertificates` + `PostAdminCertificateUpload` (PEM + key validation per `crypto/x509` + `crypto/tls`)
- [ ] `internal/feature/admin/certificates_test.go` — 6 unit tests (cert parse, key match, SAN match, expiry check, chain validate, upload-to-S3 happy path)
- [ ] `internal/handlers/templates/admin/certificates.html` — upload form + current cert info + "Enable LE auto via reg.ru DNS-01" toggle
- [ ] `cmd/skygate/main.go` — `POST /admin/certificates/upload` + `POST /admin/certificates/toggle-dns01` routes
- [ ] `internal/i18n/catalog_admin.go` — 7 new keys (cert_title / cert_current / cert_upload_pem / cert_upload_key / cert_apply / cert_dns01_toggle / cert_dns01_help) in RU+EN
- [ ] `scripts/check_b148.sh` (5 contracts)

### Phase 5: /admin/ha (chain editor + failover controls)
- [ ] `internal/feature/admin/ha.go` — `GetAdminHA` + `PostAdminHAForcePromote` + `PostAdminHAForceDemote` + `PostAdminHAAutoReclaimToggle` + `PostAdminHAChainEdit`
- [ ] `internal/handlers/templates/admin/ha.html` — chain table + failover policy radio (auto/manual) + force buttons + reclaim button
- [ ] `cmd/skygate/main.go` — routes + audit log writes
- [ ] `internal/i18n/catalog_admin.go` — 9 new keys (ha_title / ha_chain / ha_priority / ha_status / ha_role_active / ha_role_standby / ha_policy / ha_force_promote / ha_force_promote_confirm) in RU+EN
- [ ] `scripts/check_b149.sh` (5 contracts)

### Phase 6: skygate deploy subcommand + /admin/deploy
- [ ] `internal/deploy/push.go` — build, push to `s3://skygate-ha/deploy/<target-hostname>/`
- [ ] `internal/deploy/pull.go` — pull from S3, restart with graceful drain
- [ ] `internal/deploy/subcommand.go` — `skygate deploy {push,pull,sync,status}` + `skygate ha {promote,demote,reclaim}` CLI
- [ ] `cmd/skygate/main.go` — `case "deploy-push"`, `case "deploy-pull"`, `case "ha-promote"`, `case "ha-demote"`
- [ ] `internal/feature/admin/deploy.go` — `GetAdminDeploy` + `PostAdminDeployPush` + `PostAdminDeployTestFailover` (dry-run)
- [ ] `internal/handlers/templates/admin/deploy.html` — cluster topology table + push/pull/promote/demote buttons + test-failover dry-run
- [ ] `internal/i18n/catalog_admin.go` — 8 new keys in RU+EN
- [ ] `scripts/check_b150.sh` (5 contracts)

### Phase 7: svyatoslava-1 bootstrap (manual operator runbook)
- [ ] Provision svyatoslava-1 (OS + Docker)
- [ ] `scripts/bootstrap_standby.sh` — install Patroni replica + headscale replica + skygate (in standby role) + certsync
- [ ] Wire skygate-standby → S3 deploy bucket
- [ ] Verify standby serves 200 on `/healthz` with role=standby banner

### Phase 8: init-headplane.sh (auto-apply API key on fresh deploy)
- [ ] `scripts/init-headplane.sh` — wait for headplane to generate key, copy to skygate env, restart
- [ ] On external headplane: prompt operator for URL + key on first boot, save to `.env`
- [ ] `.env` replication via S3 deploy/ subdir

### Phase 9: Live DR drill
- [ ] `scripts/dr_drill.sh` — script that operator runs in a maintenance window
  1. Confirm both VMs are at same version
  2. `kill -9` skygate on primary → verify failover in <60s
  3. Restart skygate on primary → verify it becomes standby (no flap)
  4. `kill -9` both skygates → verify DNS still resolves + reg.ru API works
  5. Restart both → verify both healthy

### Phase 10: v1.5.0 release + GitHub
- [ ] Tag `v1.5.0` after all B-checks PASS on VM
- [ ] GitHub release with notes: "BL-2 HA Tier 1 — active-passive skygate-prod + skygate-standby with Patroni auto-failover, reg.ru DNS, certsync, /admin/ha + /admin/certificates + /admin/deploy"
- [ ] Deploy to skygate-standby (then run live failover test to bring skygate-prod online)

---

## 4. Open questions (block Phase start)

| # | Question | Blocker for | Operator answer | Status |
|---|---|---|---|---|
| 1 | reg.ru API credentials (user + password) | Phase 2, 4 | **NEEDED** — see "How to provide" below | ⏳ PENDING |
| 2 | reg.ru API IP whitelist — add both VM public IPs | Phase 2 | **NEEDED** if reg.ru has IP whitelist enabled | ⏳ PENDING |
| 3 | Tailscale Funnel: NO (decided 2026-08-18) | n/a | DECIDED (decision #4) | ✅ DONE |
| 4 | S3 bucket `s3://skygate-ha/` creation status | Phase 3+ | **RESOLVED** — reusing `s3://skygate-backups/ha/` prefix (existing bucket, same IAM) | ✅ DONE 2026-08-18 |
| 5 | S3 IAM credentials for skygate process | Phase 3+ | **RESOLVED** — using existing backup.s3_* credentials (skygate-test / skygate-test-pass-2026 / endpoint http://172.18.0.5:9000) | ✅ DONE 2026-08-18 |
| 6 | Auto-failover default: ON or OFF? | Phase 1 | DECIDED — default ON (per operator's "сделал авто"), with manual override | ✅ DONE 2026-08-18 |
| 7 | svyatoslava-1 hostname in headscale | Phase 7 | DECIDED — rename to `skygate` (decision #2) | ✅ DONE |
| 8 | Caddy installation on svyatoslava-1 (LE + reg.ru plugin) | Phase 4 | **REMOVED** — decided on-site during Phase 4 impl (standard apt install + caddyserver.com binary) | 🗑️ REMOVED 2026-08-18 |
| 9 | Live DR drill date | Phase 9 | **NEEDED before Phase 9** — operator to pick a maintenance window | ⏳ PENDING (not blocking) |
| 10 | Backup bucket already in S3: name + IAM | Phase 3 reference | **RESOLVED** — see Q4/Q5 | ✅ DONE 2026-08-18 |

### How to provide reg.ru API credentials (Q1)

1. Log in: https://www.reg.ru/user/
2. Navigate: "Настройки" → "API" (or https://www.reg.ru/user/api/)
3. Create API key with scope: **DNS only** (read + write records, no billing/domain-transfer)
4. Send back as file/env block (any secure channel):
   ```bash
   SKYGATE_DNS_PROVIDER=regapi
   SKYGATE_DNS_REGAPI_USER=<your_reg_ru_login>
   SKYGATE_DNS_REGAPI_PASSWORD=<api_key_password>
   SKYGATE_DNS_REGAPI_ZONE=skynas.ru
   ```
5. I will:
   - Save to `internal/secretbox/` (age-encrypted, master key from `SKYGATE_SECRET_KEY`)
   - Replicate to standby via S3 deploy/ subdir
   - NOT store in postgres (per your "при миграции headplane все равно новый уникальный ключ")

### How to verify reg.ru IP whitelist (Q2)

1. Log in: https://www.reg.ru/user/
2. Navigate: "Настройки" → "Безопасность" → "API IP whitelist"
3. If whitelist is enabled: add `<svyatoslava-1-public-ip>` and `<current-VM-public-ip>` (95.165.170.190)
4. If whitelist is disabled: nothing to do
5. Tell me which case applies (so I document the right behavior in the deploy script)

---

## 5. Risks + open architectural questions

| Risk | Mitigation | Severity |
|---|---|---|
| reg.ru API rate-limited | 5-10s timeout on DNS update + retry once | low |
| Tailscale split-brain during failover | Patroni state is ground truth; tiebreak by `patronictl list` JSON | low |
| reg.ru API misconfigured → active stuck with old IP | Manual `Force demote` button in /admin/ha + alert via Telegram | low |
| Operator forgets to run `bootstrap_standby.sh` on svyatoslava-1 | Phase 7 is a hard gate before Phase 10 release | medium |
| S3 IAM too permissive | Use scoped credentials (only `s3://skygate-ha/*` for skygate process) | medium |
| Cert upload bypasses validation → Caddy breaks | `crypto/x509` parse + chain check + key match in handler | low |
| Heartbeat flaps on transient network hiccup | Missed-threshold 3 (= 15s), not 1 (= 5s) | low |
| Active-passive means standby is idle | Acceptable per operator's "Active-Passive" decision | low |

---

## 6. Status updates (chronological log)

Each Mavis session that touches v1.5.0 should append a `### YYYY-MM-DD HH:MM` block here.

### 2026-08-18 (initial)
- v1.5.0 plan created based on operator's "BL-2 detailed design" conversation
- Decisions locked: `skygate.skynas.ru` (DNS) / `skygate` (active) / `skygate-standby` (standby) / reg.ru DNS / no Tailscale Funnel / Active-Passive / starter chain 2 nodes
- 10-phase plan + 10 open questions created
- Patroni config NOT touched (operator's explicit guidance)
- Status: PLANNING complete, awaiting operator answers to open questions + go-ahead

### 2026-08-18 (S3 resolution + open questions cleanup)
- S3 layout: `s3://skygate-backups/ha/` (reusing existing `skygate-backups` bucket at `http://172.18.0.5:9000`, same IAM `skygate-test` / `skygate-test-pass-2026`); no new bucket or credentials needed
- Q4, Q5, Q10 RESOLVED (S3 layout)
- Q8 REMOVED (Caddy install path — decided on-site during Phase 4)
- Q9 reclassified (DR drill date — needed only at Phase 9, not blocking)
- **Only Q1 (reg.ru creds) and Q2 (reg.ru IP whitelist) remain as blocking**
- Step-by-step reg.ru API credential guide added to §4
- Status: S3 unblocked, awaiting reg.ru creds to start Phase 1 + Phase 2

---

## 7. References

- `docs/PLANS.md` §v1.5.0 — public-facing plan
- `docs/BACKLOG.md` Priority 3 — BL-2 entry, updated UNBLOCKED status
- `docs/internal/ha-architecture.md` — Tier 1 architecture (now v1.5.0-aligned)
- `docs/internal/v0.27.0-postgres-ha.md` — Patroni + etcd reference (NOT modified, only consulted)
- `docs/internal/https-setup.md` — Caddy config patterns (v0.32.11 baseline, extended for reg.ru DNS-01)
- `docs/disaster-recovery.md` — Tier 0 fallback when v1.5.0 isn't deployed
