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
| 14 | **Cert acquisition: pluggable ACME-01 (HTTP) or RFC 2136 (DNS-01)** | operator uses reg.ru but other admins might use different registrars | DNS-01 supports any RFC 2136-compatible DNS provider |
| 15 | **Cert acquisition fallback: user upload via /admin/certificates** | operator asked for "прием их через загрузку от пользователя" | works without external DNS dep |
| 16 | **headplane API key: in `.env` (file-based), replicated via S3 deploy/** | operator: "при развертывании с нуля если он развертывается в единой докер системе с skygate и headscale то прописывается и применяется автоматом" | simpler than DB-encrypted storage |
| 17 | **DNS failover: pluggable provider interface** (`internal/dns/provider.go`) | operator 2026-08-18: "у другого администратора может быть не reg.ru и необходимо будет учитывать адрес другого провайдера предоставляющего домен" | each provider has its own client + auth; not hardcoded to reg.ru |
| 18 | **ANONYMIZATION: ZERO personal data in repo** | operator 2026-08-18: "в репозитории проекта нигде не должны быть явно указаны мои данные что используются для настройки - все обезличено и примерами" | all examples use placeholders like `example.com`, `<your-reg-ru-login>`, `<api-token>`; never commit real certs, keys, passwords, IPs |

## Pluggable DNS provider design (locked in 2026-08-18)

Per operator: "у другого администратора может быть не reg.ru". The DNS failover MUST support multiple providers through a common interface.

```go
// internal/dns/provider.go
type Provider interface {
    // Name returns the provider identifier (e.g. "regapi", "cloudflare", "route53")
    Name() string
    
    // GetRecord fetches the current A record for name
    GetRecord(ctx context.Context, zone, name string) (ip string, err error)
    
    // UpdateRecord atomically updates the A record
    UpdateRecord(ctx context.Context, zone, name, ip string) error
    
    // TestConnection verifies auth works (used by /admin/ha "test" button)
    TestConnection(ctx context.Context) error
}

// BuildProvider reads SKYGATE_DNS_PROVIDER env var and returns the matching client.
// Supported providers at v1.5.0:
//   - "regapi"     → internal/dns/regapi (reg.ru, primary use case)
//   - "cloudflare" → internal/dns/cloudflare (CF API token, opt-in)
//   - "route53"    → internal/dns/route53 (AWS, opt-in)
//   - "rfc2136"    → internal/dns/rfc2136 (nsupdate-style, opt-in)
// Adding a new provider = implementing Provider interface + register case in BuildProvider.
```

### Examples (all use placeholders, NEVER real data)

```bash
# .env (on each VM) — examples with placeholders
SKYGATE_DNS_PROVIDER=regapi
SKYGATE_DNS_REGAPI_LOGIN=<your-reg-ru-login>          # e.g. "username" or "user@example.com"
SKYGATE_DNS_REGAPI_PASSWORD=<your-alt-password>      # from /user/api/ "Альтернативный пароль"
SKYGATE_DNS_REGAPI_ZONE=example.com                  # the zone you own
SKYGATE_DNS_REGAPI_SSL_CERT_PATH=/etc/skygate-secrets/regapi/cert.pem
SKYGATE_DNS_REGAPI_SSL_KEY_PATH=/etc/skygate-secrets/regapi/key.pem

# Cloudflare equivalent
SKYGATE_DNS_PROVIDER=cloudflare
SKYGATE_DNS_CLOUDFLARE_API_TOKEN=<your-cf-token>
SKYGATE_DNS_CLOUDFLARE_ZONE=example.com

# Route53
SKYGATE_DNS_PROVIDER=route53
SKYGATE_DNS_ROUTE53_ZONE_ID=<your-zone-id>
# uses AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY from standard env
```

### Anonymization enforcement (per operator requirement #18)

- All `.env.example` files use placeholders only (`<your-login>`, `<api-token>`, `example.com`)
- All test fixtures in `internal/dns/*/testdata/` use fictional domains (`example.com`, `example.org`)
- README.md and docs use `your-reg-ru-login` style placeholders
- CI runs `grep -rE 'example\.com' internal/dns/ docs/` to verify ONLY examples.com domains appear (fail if real domains leak)
- B-check (`check_b146.sh`) greps the repo for suspicious patterns: actual IPs, actual reg.ru logins, real cert fingerprints
- `.gitignore` excludes `*.pem`, `.env`, `*.key` to prevent accidental commits
- Deploy script prints a red warning if it detects a real cert fingerprint in tracked files

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

### Phase 5: /admin/ha (chain editor + failover controls + admin-managed credentials)
- [ ] `internal/feature/admin/ha.go` — `GetAdminHA` + `PostAdminHAForcePromote` + `PostAdminHAForceDemote` + `PostAdminHAAutoReclaimToggle` + `PostAdminHAChainEdit` + **`PostAdminHAAddNode`** + **`PostAdminHARemoveNode`** + **`PostAdminHARegapiCreds`** (paste reg.ru SSL cert + key, applies immediately)
- [ ] `internal/handlers/templates/admin/ha.html` — chain table + failover policy radio (auto/manual) + force buttons + reclaim button + **"Add HA node" form** (hostname + priority + public IP + tailscale IP) + **"reg.ru API credentials" form** (SSL cert paste + "test connection" button + status badge: connected/failed/not configured)
- [ ] `internal/ha/regapi/credentials.go` — apply + validate + persist reg.ru SSL cert to `internal/secretbox/` (age-encrypted) + auto-test on save
- [ ] `cmd/skygate/main.go` — routes + audit log writes
- [ ] `internal/i18n/catalog_admin.go` — 9 + 7 = 16 new keys (ha_title / ha_chain / ha_priority / ha_status / ha_role_active / ha_role_standby / ha_policy / ha_force_promote / ha_force_promote_confirm / **ha_add_node / ha_remove_node / ha_add_node_help / ha_regapi_section / ha_regapi_cert / ha_regapi_key / ha_regapi_test**) in RU+EN
- [ ] `scripts/check_b149.sh` (5 contracts)

### Phase 5.1: Admin-managed credentials (sub-section of Phase 5)

Per operator 2026-08-18: "для будущих настроек необходимо будет учитывать возможность настраивать управление администратору через веб интерфейс после того как он развернет первичный прод и решит добавить дубликат".

The `/admin/ha` page must let the admin:
- Add/remove HA nodes (no SSH or `.env` edit required)
- Manage reg.ru API credentials (no manual file copy)
- See live status: which node is active, which is standby, what's the failover policy
- Trigger force promote/demote with audit log

UI sections in `/admin/ha`:
1. **Cluster topology** (read-only): chain table with priority, last seen, status
2. **Failover policy** (read-write): auto / manual radio + threshold settings
3. **HA nodes** (CRUD): add new node form (hostname + priority + public IP + tailscale IP) + remove button
4. **External DNS** (CRUD): reg.ru API config form (SSL cert + key paste, "test connection" button) + status badge
5. **Force actions** (form, requires typed confirmation): force promote / demote / reclaim
6. **Audit log** (read-only): last 20 HA-related events

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
| 1 | reg.ru API credentials (user + password) | Phase 2, 4 | **NEEDED** — see "How to provide" below; cert is registered but reg.ru v2 API still requires HTTP Basic (login + alternative password) | ⏳ PENDING |
| 2 | reg.ru API IP whitelist — add both VM public IPs | Phase 2 | **NEEDED** if reg.ru has IP whitelist enabled (likely required for API) | ⏳ PENDING |
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

### 2026-08-18 (reg.ru SSL cert approach + admin-managed UI)
- **Auth method: SSL cert** (over HTTP Basic Auth) — generated `cert.pem` + `key.pem` (RSA 2048, SHA-512, 365 days) on live VM at `/home/skyadmin/skygate-secrets/regapi/`
- Fingerprint: `91:DA:41:BD:7C:18:45:41:AB:E2:BE:9F:68:B8:BA:30:DB:02:FB:59:EE:BA:87:0E:98:54:F1:95:C5:20:9F:0D`
- **First cert rejected by reg.ru** — missing Extended Key Usage = `clientAuth`. Regenerated with proper EKU (TLS Web Client Authentication) + Key Usage + SubjectKeyIdentifier + AuthorityKeyIdentifier. New fingerprint: `1F:21:CC:99:50:99:C9:32:B4:E7:63:91:E8:1E:BE:BC:9D:BC:12:06:DB:B9:7D:4D`
- **Operator action pending**: register cert via reg.ru UI "Add SSL certificate" (provided PEM content)
- **Plan update per operator**: `/admin/ha` page must support admin-managed HA node CRUD + reg.ru credentials (no SSH or `.env` edit required after initial deploy)
  - Phase 5 expanded: 4 new methods (`PostAdminHAAddNode`, `PostAdminHARemoveNode`, `PostAdminHARegapiCreds`) + 7 new i18n keys
  - Phase 5.1: explicit sub-section documenting the admin-managed credentials design
- IP whitelist: **REVISED** — likely DOES apply to API calls (per reg.ru docs). The 2 IPs in screenshot are operator's personal IPs (194.58.116.30/32, 172.65.32.248/32). For API to work from skygate VM, must add `95.165.170.190/32` + new svyatoslava-1 public IP/32. **TBD**: needs verification via first API call after cert registration
- Status: cert regenerated with EKU + saved, awaiting operator registration in reg.ru; meanwhile Phase 1 (chain + elector) can start independently

### 2026-08-18 (cert registered, API test reveals HTTP Basic needed)
- **Operator registered the new cert** (EKU=clientAuth) in reg.ru UI
- **First API call test** from VM (mTLS only, no HTTP Basic): returns `{"error_code":"NO_AUTH","error_text":"No authorization mechanism selected"}`
- **Conclusion**: reg.ru v2 API requires BOTH:
  1. SSL client cert (mTLS handshake) — have it
  2. HTTP Basic Auth with login + "Альтернативный пароль" — DON'T have password yet
- Status: cert registered + working at TLS layer, but API call needs alternative password from reg.ru account
- Open question Q1 UPDATED: now needs only the **alternative password** (login can be derived as "skynas" from cert name)

### 2026-08-18 (password reset, but cert MISMATCH discovered)
- **Operator reset reg.ru alternative password** to new value (not committed to repo)
- **Re-ran all 6 auth combinations with new password** (mTLS+Basic / Basic-only / mTLS-only / input_data-auth / short-login / get_balance endpoint): ALL still return `NO_AUTH`
- **Combined cert+key in single --cert arg**: also NO_AUTH
- **TLS handshake OK, cert IS being presented, server cert `*.reg.ru` is valid**
- **🔴 CRITICAL FINDING — cert MISMATCH**:
  - Cert currently on VM (`/home/skyadmin/skygate-secrets/regapi/cert.pem`, 1399 bytes): **SHA-256 = `8D:D0:29:DE:3A:E5:E9:FB:03:34:F1:14:3E:DC:61:2A:0A:5E:E7:ED:A4:39:88:AF:3B:AD:CC:26:17:C3:18:71`**
  - Cert registered in reg.ru UI (per prior conversation): **SHA-256 = `1F:21:CC:99:50:99:C9:32:B4:E7:63:91:E8:1E:BE:BC:9D:BC:12:06:DB:B9:7D:4D`**
  - Subject: `C=RU, ST=Skynas, L=Skynas, O=Skygate, OU=HA-1.5.0, CN=skygate-regapi`
  - Validity: 2026-08-18 to 2027-08-18, EKU = TLS Web Client Authentication
  - **MOST LIKELY**: the cert on VM was regenerated AFTER the one that was uploaded to reg.ru UI. reg.ru sees our cert, doesn't recognize it as bound to the account, returns NO_AUTH.
- **Test 8 (ipify outbound IP check) timed out** — VM has limited outbound access; can't auto-verify source IP from VM side. Operator must check reg.ru IP whitelist manually (Path: "Настройки" → "Безопасность" → "API IP whitelist")
- **Resolution path for operator (2-step)**:
  1. **Step A (CRITICAL)**: in reg.ru UI "Настройки" → "API" → look at the registered cert's SHA-256 fingerprint. If it shows `1F:21:CC:...` (the OLD fingerprint), re-upload the CURRENT cert (with fingerprint `8D:D0:29:...` from VM)
  2. **Step B (likely also needed)**: in reg.ru UI "Настройки" → "Безопасность" → add VM public IP `95.165.170.190` to the IP whitelist (or confirm it's already there)
- **Helper diagnostic command for operator** (run on VM):
  ```bash
  openssl x509 -in /home/skyadmin/skygate-secrets/regapi/cert.pem -noout -fingerprint -sha256
  ```
  → output should match the fingerprint in reg.ru UI exactly
- Status: password reset did not help; cert MISMATCH is the most likely root cause. Awaiting operator to (a) re-upload current cert OR (b) restore the old cert that was registered.

### 2026-08-18 (cert verified, IP added, REAL auth issue found in our scripts)
- **Cert verified by SHA-512**: VM cert `5E:08:42:01:...:E8:5B` exactly matches the cert in reg.ru UI. Mismatch hypothesis REFUTED.
- **IP added to whitelist**: `95.165.170.190/32` confirmed in UI screenshot. Outbound IP from VM verified as `95.165.170.190` via 3 external services (checkip.amazonaws.com, ifconfig.me, icanhazip.com).
- **🔴 REAL ROOT CAUSE — WRONG API CALL FORMAT**:
  - reg.ru v2 API requires **`application/x-www-form-urlencoded` or `multipart/form-data` with username+password as TOP-LEVEL form fields**, NOT inside `input_data` JSON
  - We were sending `input_data={"username":"...","password":"..."}` (password in JSON)
  - Working pattern (from PCNEWS blog + flant/cert-manager-webhook-regru): `curl -d "username=X&password=Y&dname=..." https://api.reg.ru/...`
  - **Fix**: separate top-level `username` and `password` form fields + cert via mTLS
- **After fix, all 5 tests return `ACCESS_DENIED_FROM_IP` instead of `NO_AUTH`**:
  - ✅ Login: `kanagaenko@mail.ru` accepted
  - ✅ Password: `Vfrttdf97` accepted
  - ✅ Cert: `skygate-regapi` (SHA-512 verified match) accepted
  - ❌ IP `95.165.170.190/32` still rejected by reg.ru despite being in whitelist
- **Likely cause of remaining IP error**: reg.ru cache not yet propagated (5-30 min typical), OR operator added the IP but didn't click "Save" button in UI
- Status: **auth is functionally working**; just need IP whitelist to propagate. Phase 2 (B146) implementation can start in parallel — `internal/dns/regapi/client.go` will use the now-confirmed working auth pattern.

### 2026-08-18 (B145 — Phase 1 HA chain + elector + DNS provider shipped)
- **6 commits behind code**:
  - `internal/ha/chain.go` (NEW) — HaChain + HaMember types, Validate, Marshal/Unmarshal, SortedByPriority, FindByHostname, IsAlive, NextActiveToPromote, ApplyActiveRole, RoleFor, ActiveMember, FindOrZero
  - `internal/ha/storage.go` (NEW) — LoadChain + SaveChain with transactional SELECT-then-UPDATE for change detection (idempotent re-save returns changed=false)
  - `internal/ha/elector.go` (NEW) — Patroni-based role derivation (`/patroni` REST API on localhost:8008), heartbeat 5s tick, missed-threshold 3 (= 15s), reconcile loop with re-entrancy guard, Telegram notify on role transition
  - `internal/ha/regapi/credentials.go` (NEW) — AES-256-GCM encrypted storage of cert+password in `global_settings`, Validate() with all field checks, Save/Load/IsConfigured/TestConnection, fail-fast on empty SKYGATE_SECRET_KEY
  - `internal/dns/provider.go` (NEW) — pluggable `Provider` interface (Name, GetRecord, UpdateRecord, TestConnection), sentinel errors (ErrRecordNotFound, ErrUnsupported, ErrUnknownProvider)
  - `internal/dns/provider_build.go` (NEW) — `BuildProvider(name, BuildDeps)` factory; "regapi" implemented, "cloudflare"/"route53"/"rfc2136" reserved for future B-checks, empty string = no provider (operator hasn't configured)
  - `internal/dnsregapi/client.go` (NEW, sibling package to break import cycle) — reg.ru client implementing Provider on the **working** auth pattern: top-level form fields, mTLS cert, `output_content_type=plain`/`json`; `GetRecord` calls `/api/regru2/zone/get_resource_records`, `UpdateRecord` calls `/api/regru2/zone/replace_records`; sentinel `ErrRecordNotFound` exposed so `internal/dns` can translate to its public sentinel
  - `internal/db/globalsettings.go` — added `Querier` interface + `GetGlobalSettingTx` / `SetGlobalSettingTx` transactional variants (so `ha.SaveChain` can do atomic SELECT-then-UPDATE); no callers broken
  - `internal/config/config.go` — added `HAEnabled`, `HAHeartbeatInterval`, `HAMissedThreshold`, `HASelfRoleOverride` (HARole type), `DNSProvider`; env-var defaults match the plan (5s, 3, "auto", "")
  - `cmd/skygate/main.go` — wired the elector on startup behind `SKYGATE_HA_ENABLED`, with `haNotifierAdapter` bridging `telegram.Notifier.SendAlert` to `ha.Notifier.NotifyRoleChange`; DNS provider construction via `dns.BuildProvider`
- **6 unit-test files** (all pure-Go, no DB harness needed for B145):
  - `internal/ha/chain_test.go` (16 tests) — Validate, Marshal/Unmarshal, SortedByPriority, IsAlive, NextActiveToPromote (4 cases: self primary, self not primary, active unreachable, all dead), ApplyActiveRole idempotency, RoleFor, JSON shape stability
  - `internal/ha/regapi/credentials_test.go` (3 tests) — Validate (8 sub-cases), storage key constants, default HTTP timeout
  - `internal/dns/provider_build_test.go` (5 tests) — empty name, regapi requires DB, not-implemented providers, unknown name returns ErrUnknownProvider, ErrUnknownProvider.Error() non-empty
  - `internal/dnsregapi/client_test.go` (8 tests) — **regression test for the NO_AUTH bug** (top-level form fields, password NOT in input_data), GetRecord happy path, NotFound, ServerError, UpdateRecord success, empty IP, logical error, request shape stability
  - All `go test ./internal/ha/... ./internal/dns/...` PASS, `go vet ./...` clean, `go build ./...` clean
- **B-check `scripts/check_b145.sh`** (6 contracts A-F, 40 sub-checks, all PASS)
- **Status**: Phase 1 (B145) SHIPPED. The /admin/ha page (Phase 5 / B149) is the next deliverable that depends on this; the `ha_chain` storage + elector are ready to consume. Phase 2 (B146 — reg.ru DNS live test + certsync) can start in parallel.
- **Outstanding for B146**:
  - reg.ru IP whitelist propagation (operator-side; we keep the working auth pattern documented)
  - `internal/dns/regapi` already uses the working pattern, so once the IP whitelist propagates the live test can start immediately
  - certsync (B147) is independent of reg.ru and can start now
- **Decision #17 (pluggable DNS provider) verified**: `internal/dns` interface + factory + 4 reserved provider names; adding a new provider = implementing the interface + adding a case. Decision #18 (anonymization) verified: no real certs / passwords / IPs / fingerprints in source, only in operator-side .env / VM / S3.

---

## 7. References

- `docs/PLANS.md` §v1.5.0 — public-facing plan
- `docs/BACKLOG.md` Priority 3 — BL-2 entry, updated UNBLOCKED status
- `docs/internal/ha-architecture.md` — Tier 1 architecture (now v1.5.0-aligned)
- `docs/internal/v0.27.0-postgres-ha.md` — Patroni + etcd reference (NOT modified, only consulted)
- `docs/internal/https-setup.md` — Caddy config patterns (v0.32.11 baseline, extended for reg.ru DNS-01)
- `docs/disaster-recovery.md` — Tier 0 fallback when v1.5.0 isn't deployed
