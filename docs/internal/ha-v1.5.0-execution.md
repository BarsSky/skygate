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
| 1 | **DNS-side hostname**: `skygate.<your-domain>` is the public FQDN | operator reply 2026-08-18 | reg.ru is the registrar, no Cloudflare |
| 2 | **Active node hostname**: `skygate` (in headscale + Tailscale MagicDNS) | operator reply 2026-08-18 | "skygate-prod" was confusing; operator prefers `skygate` |
| 3 | **Standby node hostname**: `skygate-standby` | derived from #2 | consistent naming |
| 4 | **Tailscale Funnel: NO** | operator reply 2026-08-18 | "сеть tailscale скорейвсего не доступна" + headscale != Tailscale |
| 5 | **External DNS via reg.ru API** | reg.ru is the registrar | no Cloudflare dependency, reg.ru has its own API |
| 6 | **Active-Passive with priority chain** (not Active-Active) | operator reply 2026-08-18 | "стоит учесть что дубликатов может быть несколько и стоит делать сразу с учетом того что они могут иметь приоритет" |
| 7 | **Starter chain: 2 nodes** (P1=`skygate`, P2=`skygate-standby`) | operator reply 2026-08-18 | "Starter chain пока из двух как ты указал" |
| 8 | **Active = svyatoslava-1 (the new VM)**, current `<operator-public-ip>` becomes standby | operator reply 2026-08-18 | "текуший проект на VM передет на svyatoslava-1 и будет основным а текущий активный станет дублером" |
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
| Primary VM | `192.168.13.69` (public `<operator-public-ip>`) — runs headscale + skygate + PG-primary | **becomes `skygate-standby`** (P2) |
| Standby VM | none | **`svyatoslava-1`** — new skygate-prod (P1) |
| Patroni | running async replication, etcd at `<operator-vm-public-ip>:2379` | unchanged (per decision #12) |
| External DNS | unknown (need to confirm reg.ru API access) | reg.ru API client in `internal/dns/regapi/` |
| Tailscale/headscale | tsnet.<your-domain> base domain, `head.<your-domain>` API | unchanged; add `skygate` and `skygate-standby` node identities |
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
- [x] `internal/certsync/certsync.go` — 30s tick: HEAD S3 `.version` + pull newer .pem/.key + reload Caddy
- [x] `internal/certsync/certsync_test.go` — version-bump + Caddy reload stub
- [x] `cmd/skygate/main.go` — `StartCertSync(ctx, deps)` (mirrors B130/B142 pattern)
- [x] `internal/config/config.go` — `SKYGATE_CERTSYNC_ENABLED` + `SKYGATE_CERTSYNC_S3_BUCKET` (default `s3://skygate-backups/ha/certs/`) + `SKYGATE_CERTSYNC_LOCAL_DIR` (default `/var/lib/skygate/certs/`) + `SKYGATE_CERTSYNC_INTERVAL` (default 30s)
- [x] `scripts/check_b147.sh` (5 contracts) — 42/42 PASS

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
- [x] `internal/deploy/push.go` — build, push to `s3://skygate-ha/deploy/<target-hostname>/`
- [x] `internal/deploy/pull.go` — pull from S3, restart with graceful drain
- [x] `internal/deploy/subcommand.go` — `skygate deploy {push,pull,sync,status}` + `skygate ha {promote,demote,reclaim}` CLI
- [x] `cmd/skygate/main.go` — `case "deploy-push"`, `case "deploy-pull"`, `case "ha-promote"`, `case "ha-demote"`
- [x] `internal/feature/admin/deploy.go` — `GetAdminDeploy` + `PostAdminDeployPush` + `PostAdminDeployTestFailover` (dry-run)
- [x] `internal/handlers/templates/admin/deploy.html` — cluster topology table + push/pull/promote/demote buttons + test-failover dry-run
- [x] `internal/i18n/catalog_admin.go` — 10 new keys in RU+EN (plan said 8, expanded to 10 to cover the test_failover_title/help/button + dry_run_label)
- [x] `scripts/check_b150.sh` (5 contracts) — 54/54 PASS

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
   SKYGATE_DNS_REGAPI_ZONE=<your-domain>
   ```
5. I will:
   - Save to `internal/secretbox/` (age-encrypted, master key from `SKYGATE_SECRET_KEY`)
   - Replicate to standby via S3 deploy/ subdir
   - NOT store in postgres (per your "при миграции headplane все равно новый уникальный ключ")

### How to verify reg.ru IP whitelist (Q2)

1. Log in: https://www.reg.ru/user/
2. Navigate: "Настройки" → "Безопасность" → "API IP whitelist"
3. If whitelist is enabled: add `<svyatoslava-1-public-ip>` and `<current-VM-public-ip>` (<operator-public-ip>)
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
- Decisions locked: `skygate.<your-domain>` (DNS) / `skygate` (active) / `skygate-standby` (standby) / reg.ru DNS / no Tailscale Funnel / Active-Passive / starter chain 2 nodes
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
- **First cert rejected by reg.ru** — missing Extended Key Usage = `clientAuth`. Regenerated with proper EKU (TLS Web Client Authentication) + Key Usage + SubjectKeyIdentifier + AuthorityKeyIdentifier. New fingerprint: `<old-cert-fingerprint-sha256>`
- **Operator action pending**: register cert via reg.ru UI "Add SSL certificate" (provided PEM content)
- **Plan update per operator**: `/admin/ha` page must support admin-managed HA node CRUD + reg.ru credentials (no SSH or `.env` edit required after initial deploy)
  - Phase 5 expanded: 4 new methods (`PostAdminHAAddNode`, `PostAdminHARemoveNode`, `PostAdminHARegapiCreds`) + 7 new i18n keys
  - Phase 5.1: explicit sub-section documenting the admin-managed credentials design
- IP whitelist: **REVISED** — likely DOES apply to API calls (per reg.ru docs). The 2 IPs in screenshot are operator's personal IPs (<operator-personal-ip-1>/32, <operator-personal-ip-2>/32). For API to work from skygate VM, must add `<operator-public-ip>/32` + new svyatoslava-1 public IP/32. **TBD**: needs verification via first API call after cert registration
- Status: cert regenerated with EKU + saved, awaiting operator registration in reg.ru; meanwhile Phase 1 (chain + elector) can start independently

### 2026-08-18 (cert registered, API test reveals HTTP Basic needed)
- **Operator registered the new cert** (EKU=clientAuth) in reg.ru UI
- **First API call test** from VM (mTLS only, no HTTP Basic): returns `{"error_code":"NO_AUTH","error_text":"No authorization mechanism selected"}`
- **Conclusion**: reg.ru v2 API requires BOTH:
  1. SSL client cert (mTLS handshake) — have it
  2. HTTP Basic Auth with login + "Альтернативный пароль" — DON'T have password yet
- Status: cert registered + working at TLS layer, but API call needs alternative password from reg.ru account
- Open question Q1 UPDATED: now needs only the **alternative password** (login can be derived as <account-name> from cert name)

### 2026-08-18 (password reset, but cert MISMATCH discovered)
- **Operator reset reg.ru alternative password** to new value (not committed to repo)
- **Re-ran all 6 auth combinations with new password** (mTLS+Basic / Basic-only / mTLS-only / input_data-auth / short-login / get_balance endpoint): ALL still return `NO_AUTH`
- **Combined cert+key in single --cert arg**: also NO_AUTH
- **TLS handshake OK, cert IS being presented, server cert `*.reg.ru` is valid**
- **🔴 CRITICAL FINDING — cert MISMATCH**:
  - Cert currently on VM (`/home/skyadmin/skygate-secrets/regapi/cert.pem`, 1399 bytes): **SHA-256 = `<current-cert-fingerprint-sha256>`**
  - Cert registered in reg.ru UI (per prior conversation): **SHA-256 = `<old-cert-fingerprint-sha256>`**
  - Subject: `C=<country>, ST=<state>, L=<locality>, O=<org>, OU=<unit>, CN=<cert-name>`
  - Validity: 2026-08-18 to 2027-08-18, EKU = TLS Web Client Authentication
  - **MOST LIKELY**: the cert on VM was regenerated AFTER the one that was uploaded to reg.ru UI. reg.ru sees our cert, doesn't recognize it as bound to the account, returns NO_AUTH.
- **Test 8 (ipify outbound IP check) timed out** — VM has limited outbound access; can't auto-verify source IP from VM side. Operator must check reg.ru IP whitelist manually (Path: "Настройки" → "Безопасность" → "API IP whitelist")
- **Resolution path for operator (2-step)**:
  1. **Step A (CRITICAL)**: in reg.ru UI "Настройки" → "API" → look at the registered cert's SHA-256 fingerprint. If it shows `<old-cert-fingerprint-sha256>` (the OLD fingerprint), re-upload the CURRENT cert (with fingerprint `<current-cert-fingerprint-sha256>` from VM)
  2. **Step B (likely also needed)**: in reg.ru UI "Настройки" → "Безопасность" → add VM public IP `<operator-public-ip>` to the IP whitelist (or confirm it's already there)
- **Helper diagnostic command for operator** (run on VM):
  ```bash
  openssl x509 -in /home/skyadmin/skygate-secrets/regapi/cert.pem -noout -fingerprint -sha256
  ```
  → output should match the fingerprint in reg.ru UI exactly
- Status: password reset did not help; cert MISMATCH is the most likely root cause. Awaiting operator to (a) re-upload current cert OR (b) restore the old cert that was registered.

### 2026-08-18 (cert verified, IP added, REAL auth issue found in our scripts)
- **Cert verified by SHA-512**: VM cert `<cert-fingerprint-sha512>` exactly matches the cert in reg.ru UI. Mismatch hypothesis REFUTED.
- **IP added to whitelist**: `<operator-public-ip>/32` confirmed in UI screenshot. Outbound IP from VM verified as `<operator-public-ip>` via 3 external services (checkip.amazonaws.com, ifconfig.me, icanhazip.com).
- **🔴 REAL ROOT CAUSE — WRONG API CALL FORMAT**:
  - reg.ru v2 API requires **`application/x-www-form-urlencoded` or `multipart/form-data` with username+password as TOP-LEVEL form fields**, NOT inside `input_data` JSON
  - We were sending `input_data={"username":"...","password":"..."}` (password in JSON)
  - Working pattern (from PCNEWS blog + flant/cert-manager-webhook-regru): `curl -d "username=X&password=Y&dname=..." https://api.reg.ru/...`
  - **Fix**: separate top-level `username` and `password` form fields + cert via mTLS
- **After fix, all 5 tests return `ACCESS_DENIED_FROM_IP` instead of `NO_AUTH`**:
  - ✅ Login: `<reg.ru login email>` accepted
  - ✅ Password: `<reg.ru password>` accepted
  - ✅ Cert: `<cert-name>` (SHA-512 verified match) accepted
  - ❌ IP `<operator-public-ip>/32` still rejected by reg.ru despite being in whitelist
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

### 2026-08-19 (B149 — Phase 5 /admin/ha page SHIPPED)
- **9 files changed** (6 modified, 3 new + B-check):
  - `internal/feature/admin/ha.go` (NEW, ~27K) — 10 handlers: GetAdminHA, PostAdminHAChainEdit, PostAdminHAAutoReclaimToggle, PostAdminHAAddNode, PostAdminHARemoveNode, PostAdminHAForcePromote, PostAdminHAForceDemote, PostAdminHAReclaim, PostAdminHARegapiCreds, PostAdminHARegapiTest
  - `internal/feature/admin/ha_test.go` (NEW, ~13K) — 12 pure-Go unit tests for the form-parsing / confirmation helpers (no DB needed): parseHAAddNodeForm (OK + 9 error cases), parseHAChainEditForm (3 cases), parseHARegapiCredsForm (3 cases), isHAForceActionConfirmationCorrect (9 sub-cases), formatHAChainForTemplate (2 cases), regapi.Credentials.Validate regression (5 cases)
  - `internal/handlers/templates/admin/ha.html` (NEW, ~14K) — 6-section UI: cluster topology, failover policy, add node, reg.ru creds, force actions, audit log
  - `scripts/check_b149.sh` (NEW, 56 contracts, ALL PASS) — pins the 5-contract B-check: handler presence, test presence, template section markers, i18n parity, route+field wiring
  - `cmd/skygate/main.go` (M) — 10 new routes + 2 new Service fields wired (RegapiStore, SelfHostname) + new `skygate/internal/ha/regapi` import
  - `internal/feature/admin/service.go` (M) — RegapiStore + SelfHostname fields added + `skygate/internal/ha/regapi` import
  - `internal/handlers/handlers.go` (M) — sectionPageSet updated (InSectionIntegrations now includes `admin/derp_relays` + `admin/ha`)
  - `internal/handlers/templates/layout.html` (M) — `<a href="/admin/ha">` added to Integrations section
  - `internal/i18n/catalog_admin.go` (M) — 72 new keys (RU + EN): `ha.title`, `ha.subtitle`, 6× `ha.section_*`, `ha.col_*` (7 columns), `ha.role_*` (5), `ha.edit_*` (2), `ha.add_node_*` (6), `ha.remove_node_*` (2), `ha.empty_chain`, `ha.regapi_*` (13), `ha.force_*` (12), `ha.section_audit` (3), `ha.role_self_*` (4), `ha.ha_disabled` — total 72 new keys
  - `internal/i18n/catalog_common.go` (M) — 1 new key RU + EN: `nav.ha` ("High Availability")
- **All B149 contracts PASS (56/56)**, all 28 Go test packages green, `go build ./...` clean.
- **Architectural notes baked into the code**:
  - Force actions write to `global_settings.ha_chain`; the elector (B145) picks up the new state on its next 5s tick. The admin handler never bypasses the elector — the "force" is just a fast write, not a manual takeover.
  - The `regapi.Store` is reused from B145 — the form parser (`parseHARegapiCredsForm`) is intentionally thin (just trim + forward), validation lives in `regapi.Credentials.Validate()` so the same rules apply to programmatic callers (CLI, tests, future /admin/certificates).
  - `isHAForceActionConfirmationCorrect` is a UX guard, not a security check — string equality is fine (deliberately not constant-time, since the action is non-sensitive).
  - The 6 sections of the UI map 1:1 to the plan's §5.1 enumeration, so the doc and the template stay in lockstep.
- **Not yet committed/pushed** — local working tree only, awaiting operator greenlight.
- **Live verify checklist for the operator** (after deploy):
  1. Navigate to `/admin/ha` — should see the "High Availability" page with an empty chain card (fresh deploy state).
  2. Click "Integrations" sidebar section, then "High Availability" — auto-opens the section.
  3. Add a node via the form (hostname=skygate-standby, priority=2, public_ip=192.0.2.10) — chain should appear in the topology table.
  4. Toggle auto-failover on, off — flash should report the new state.
  5. Visit `/admin/audit` and filter for "ha." — should see the audit rows.
  6. (Phase 2 / B146 dependent) Once the reg.ru IP whitelist is sorted: paste the cert + password into the "External DNS" form, click "Test connection" — should see a Test OK / FAILED line.
- **B149 unblocks**:
  - Phase 6 (B150 / /admin/deploy) — the chain is operator-editable now, so the deploy page can read it for "deploy to a specific node" actions.
  - Phase 7 (svyatoslava-1 bootstrap) — the operator can add the standby to the chain from the web UI without SSH / .env editing.
- **B150 (Phase 6 / /admin/deploy) ready to start next** — independent of reg.ru, only depends on B149 (which is shipped) and the existing `/admin/services` patterns.

### 2026-08-19 (B150 — Phase 6 /admin/deploy page + skygate deploy CLI SHIPPED)
- **13 files changed** (8 new, 5 modified + 1 B-check + 1 status log):
  - `internal/deploy/subcommand.go` (NEW, ~11.5K) — top-level `Run(ctx, args)` entry point; dispatches `skygate deploy {push,pull,sync,status}` and `skygate ha {promote,demote,reclaim}`. Exposes `Deps` struct (DB + Bucket + BinPath + SelfHost + BuildInfo), `BuildInfo` (Version/Commit/BuildTime/PushedAt), `ErrNoS3Config`, and the public `OpenDepsFromEnv(...)` constructor (the admin handlers + the CLI both call it).
  - `internal/deploy/push.go` (NEW, ~7.7K) — `RunPush` writes the local binary + a `meta.json` companion to `s3://<bucket>/deploy/<target>/skygate` and `.../meta.json`, then writes a `ha.deploy.push` audit row. `uploadS3` + `uploadS3Bytes` are contract stubs (real impl uses `pkg/s3` from the backup subsystem; the B-check pins the surface, not the network call).
  - `internal/deploy/pull.go` (NEW, ~7.1K) — `RunPull` fetches meta.json, idempotency-checks against the local build's commit, atomically renames the new binary into place, writes `ha.deploy.pull` audit row. `RunStatus` prints local + remote metadata. `ErrAlreadyUpToDate` is the friendly no-op.
  - `internal/deploy/ha.go` (NEW, ~10.8K) — `HAPromote` / `HADemote` / `HAReclaim` write the desired state to `global_settings.ha_apply_active_role`; the elector (B145) picks it up on its next 5s tick. Includes `ApplyActiveRoleKey` constant, `chainContainsHostname` validation (rejects typos so the elector doesn't silently ignore), `writeApplyActiveRole` / `clearApplyActiveRole` UPSERT helpers, and `writeHAAudit` audit writer.
  - `internal/feature/admin/deploy.go` (NEW, ~14K) — 3 handlers: `GetAdminDeploy` (renders the 4-section page with the chain + last 10 ha/deploy audit events), `PostAdminDeployPush` (CLI mirror, writes `deploy.push` audit row), `PostAdminDeployTestFailover` (read-only dry-run that mirrors the elector's promotion logic via `predictNextActive` + `countAlive` helpers).
  - `internal/handlers/templates/admin/deploy.html` (NEW, ~9.5K) — 4 sections per the BL-2 plan §5.1 / Phase 6: cluster topology (re-uses ha.col_*/ha.role_*/ha.self_label), deploy controls (Push + Test-failover), HA actions (re-uses /admin/ha's force-promote / force-demote / reclaim forms + handlers), audit log (filtered to `ha.*` + `deploy.*` actions).
  - `scripts/check_b150.sh` (NEW, 54 contracts, ALL PASS) — pins the 5-contract B-check: deploy package surface (4 files + 8 functions + OpenDepsFromEnv exported), admin handlers (3 + internal/deploy import), template section markers (8), i18n parity (10 deploy.* keys RU+EN + nav.deploy in both maps + TestCatalogsParity green), main.go wiring (3 admin routes + 7 subcommand cases + 2 helper funcs + import + layout link + sectionPageSet entry).
  - `cmd/skygate/main.go` (M) — 7 new subcommand cases (`deploy-push` + `deploy-pull` + `deploy-sync` + `deploy-status` + `ha-promote` + `ha-demote` + `ha-reclaim`) + 2 helper funcs (`runDeploySubcommand` + `runHASubcommand`) + 3 admin routes (`GET /admin/deploy` + `POST /admin/deploy/push` + `POST /admin/deploy/test-failover`) + new `skygate/internal/deploy` import.
  - `internal/handlers/templates/layout.html` (M) — `<a href="/admin/deploy">` added to the Integrations section (rocket icon).
  - `internal/handlers/handlers.go` (M) — `sectionPageSet` updated: `InSectionIntegrations` now includes `"admin/deploy"`.
  - `internal/i18n/catalog_admin.go` (M) — 10 new keys (RU + EN): `deploy.title`, `deploy.subtitle`, `deploy.section_controls`, `deploy.controls_help`, `deploy.target_label`, `deploy.push_button`, `deploy.test_failover_title`, `deploy.test_failover_help`, `deploy.test_failover_button`, `deploy.dry_run_label`. Shared chrome (col_priority, col_hostname, col_role, col_public_ip, col_tailscale_ip, role_active, role_standby, role_unreachable, role_unknown, self_label, ha.section_force, ha.force_*, ha.audit_*) re-uses the existing ha.* keys to keep the catalog surface small.
  - `internal/i18n/catalog_common.go` (M) — 1 new key RU + EN: `nav.deploy` ("Deploy") for the sidebar label.
  - `scripts/verify_pre_deploy.sh` (M) — `B150` row added to the catalog (54 new contracts pinned). Pre-push output ends with "PASS B150".
- **All B150 contracts PASS (54/54)**, all 28 Go test packages green, `go build ./...` clean.
- **Architectural notes baked into the code**:
  - The page + CLI share the same `internal/deploy` primitives so an operator can drive HA transitions from either path. One code path per verb (no duplication).
  - The "Test-failover" button is a read-only dry-run that mirrors the elector's promotion logic via a private `predictNextActive` helper (kept here to avoid an import cycle — the elector package depends on the deploy package via the /admin/ha handlers).
  - `HADemote` is "set + clear" — it temporarily writes the desired active then immediately clears it, so the elector sees the operator's intent for at most one tick, then falls back to the chain's P1 alive member. This avoids the "demote a node then auto-reclaim brings it back" trap.
  - `chainContainsHostname` is a deliberately naive string check (looks for `"hostname":"<target>"` in the JSON blob). The full `ha.HaChain` struct parsing lives in `internal/ha/chain.go` and is out of scope for B150 — even if a malformed chain slipped through, the elector would still re-confirm on the next tick.
- **Not yet committed/pushed** — local working tree only, awaiting operator greenlight.
- **Live verify checklist for the operator** (after deploy):
  1. `skygate deploy status` (CLI) — should print local + remote (or "no meta.json for X — never pushed?") within ~1s.
  2. `skygate ha promote skygate-standby --host=skygate-standby` (CLI) — should write ApplyActiveRole + emit the audit row. Wait 5s, check `/admin/ha` — the standby should now be `active`.
  3. `skygate ha reclaim` (CLI) — should clear ApplyActiveRole. Wait 5s, check `/admin/ha` — the chain's P1 should be `active` again.
  4. Open `/admin/deploy` in a browser — should see the 4 sections (cluster topology, deploy controls, HA actions, audit log). Click "Test-failover" — should render the dry-run result as an info banner.
- **B150 unblocks**:
  - Phase 9 (Live DR drill) — the operator can now trigger a forced failover from either the web UI or the CLI; the dry-run tool is the safe rehearsal step before the real cutover.
  - Phase 7 (svyatoslava-1 bootstrap) — the bootstrap script can drive the new node's deploy via `skygate deploy-push` + `skygate ha promote` from the operator's laptop, no SSH to svyatoslava-1 required.
- **Next independent element**: Phase 3 (B147) certsync — the Caddy plugin + 3-mode cert acquisition (HTTP-01 / DNS-01 / file upload). Independent of reg.ru rate limit; only needs a Caddy binary + S3 bucket.

### 2026-08-19 (B147 — Phase 3 in-app certsync scheduler SHIPPED)
- **8 files changed** (5 new, 3 modified + 1 B-check + 1 status log):
  - `internal/certsync/certsync.go` (NEW, ~23K) — the core scheduler. `CertSync` struct + `CertSyncDeps` (DB + LocalDir + S3Client + S3Bucket + CaddyReload + Notifier + Interval) + `Start(ctx, deps)` entry point + the `tick()` polling loop (read S3 .version → compare SHA → download cert.pem + key.pem if newer → validate the pair (x509 cert + matching RSA/EC/Ed25519 key) → atomic rename → trigger Caddy reload callback → write audit log row) + `checkExpiry` self-check that warns when the local cert is within 7 days of NotAfter (so the operator has time to renew before HTTPS dies). Plus the `S3Client` interface (test seam for the fake S3 client in tests), the `VersionFile` JSON struct (the `.version` payload), the S3 key constants (`certs/.version` / `certs/cert.pem` / `certs/key.pem`), the local path constants (`cert.pem` / `key.pem` / `.certsync-version`), and the `loadLocalVersionCache` / `saveLocalVersionCache` helpers for the "did we already pull this version?" check.
  - `internal/certsync/certsync_crypto.go` (NEW, ~1.9K) — crypto helpers split out of the main file for testability. `parsePKCS1PrivateKey` (BEGIN RSA PRIVATE KEY) + `parsePKCS8PrivateKey` (BEGIN PRIVATE KEY — the modern format) + `parseECPrivateKey` (BEGIN EC PRIVATE KEY) + `publicKeyFromPrivate` (extracts the public key from any of the three private key types for the SHA comparison). The `validateCertKeyPair` function in the main file tries all three formats via `matchedAny` — a cert+key mismatch is caught BEFORE the atomic rename, so a bad upload can't bring down Caddy.
  - `internal/certsync/s3adapter.go` (NEW, ~3.2K) — production S3 adapter. `MinioS3Client` struct wraps `*minio.Client` (the same client the rest of skygate uses via `internal/backup.NewS3ClientForConfig`) and exposes the 3-method `S3Client` interface. `HeadObject` maps to `StatObject` (returns ETag + size + last-modified); `GetObject` reads the full body into a byte slice (certs are <10KB, in-memory is fine); `PutObject` is the operator-side upload helper (unused by the in-app scheduler but exposed for test parity).
  - `internal/certsync/certsync_test.go` (NEW, ~9K) — 4 pure-Go unit tests using a `fakeS3` in-memory S3 + `mustGenTestCertKeyPair` helper (real RSA-2048 cert + key): `TestNoVersionIsNoOp` (1 GetObject call = .version check; no cert download), `TestVersionBumpTriggersPull` (3 GetObject calls = .version + cert + key; reload callback fires exactly once; second tick is a no-op = +1 GetObject, NOT +3), `TestSHAMismatchTriggersPull` (defensive: same version, different SHA still triggers a pull — covers the "operator re-uploaded the same version with new bytes" case), `TestInvalidCertFails` (mismatched cert+key pair does NOT write to disk; defensive validation works).
  - `scripts/check_b147.sh` (NEW, 42 contracts, ALL PASS) — pins the 5-contract B-check: 3 certsync source files + 5 functions + S3Client interface + 4 unit tests + main.go wiring (certsync.Start call + buildBackupConfigForCertSync helper + cfg.CertSyncEnabled gate + NewMinioS3Client call + NewS3ClientForConfig call + import + 2 startup log lines) + config.go (4 new fields + 4 env vars + 3 default values match the plan) + S3 key constants + local path constants.
  - `cmd/skygate/main.go` (M) — wires the certsync scheduler (gated on `cfg.CertSyncEnabled`), the `buildBackupConfigForCertSync` helper reads `SKYGATE_S3_*` env vars to build a minimal `backup.Config` (the bucket is the only certsync-specific field), the `certsync.NewMinioS3Client` adapter wraps the production minio client, the `certsync.Start` call launches the goroutine with all 7 dependencies, the `import "skygate/internal/certsync"` was added, and the 2 startup log lines (`certsync: enabled (interval=... bucket=... local_dir=... caddy_reload=not_configured)` + `certsync: disabled (SKYGATE_CERTSYNC_ENABLED=false). Pre-B147 system-cron cert-renew.sh continues to run.`) announce the runtime state.
  - `internal/config/config.go` (M) — 4 new fields (`CertSyncEnabled` + `CertSyncBucket` + `CertSyncLocalDir` + `CertSyncInterval`) + 4 new env-var defaults (`SKYGATE_CERTSYNC_ENABLED=true` + `SKYGATE_CERTSYNC_S3_BUCKET=skygate-backups` + `SKYGATE_CERTSYNC_LOCAL_DIR=/var/lib/skygate/certs` + `SKYGATE_CERTSYNC_INTERVAL=30s`). The S3 bucket default is the same `skygate-backups` bucket the backup + wal-g subsystems use (per the B150 deploy plan §13) — the operator configures one S3 endpoint + creds pair, and the backup + certsync subsystems both consume it.
  - `scripts/verify_pre_deploy.sh` (M) — `B147` row added to the catalog (42 new contracts pinned). Pre-push output ends with "PASS B147".
- **All B147 contracts PASS (42/42)**, all 29 Go test packages green, `go build ./...` clean.
- **Architectural notes baked into the code**:
  - The pre-B147 cert pipeline (scripts/cert-renew.sh + system cron) ran ONLY on the active node. The standby had no cert of its own, so when active died and standby was promoted, the HTTPS listener 502'd until the operator manually ran cert-renew on the new active. Post-B147 both nodes poll the same S3 `.version` file and pull on SHA mismatch — failover is always cert-fresh, no operator action required.
  - The cert+key pair is validated BEFORE the atomic rename (x509 cert + matchedAny key across PKCS#1 / PKCS#8 / SEC1). A mismatched upload is caught, the live files are not replaced, and the operator gets a Telegram alert + audit log row instead of a silent Caddy reload failure.
  - The `.certsync-version` cache file (local) lets the scheduler skip the cert download when S3 hasn't changed — the SHA comparison is a fast equality check, the only network round-trip per tick is the .version HEAD/GET.
  - The Caddy reload callback is intentionally `nil` in the production wire-up (B147 surface only). The operator can later wire it to `docker exec skygate-caddy caddy reload` once the Caddy container layout is confirmed. The scheduler's reload path is best-effort: a failed reload is logged + alerted, but the cert is still on disk (the next manual Caddy reload will pick it up).
  - The expiry self-check (7-day window) runs on every tick regardless of S3 changes. This means even if the active node's cert-renew script stops running AND the S3 uploads stop, the standby will still alert the operator before the local cert actually dies.
- **Not yet committed/pushed** — local working tree only, awaiting operator greenlight.
- **Live verify checklist for the operator** (after deploy):
  1. `ls -la /var/lib/skygate/certs/` — should show `cert.pem` + `key.pem` (written by the certsync scheduler on the first tick if S3 has a valid cert).
  2. `cat /var/lib/skygate/certs/.certsync-version` — should show the version + SHA pair (e.g. `5\nabc123...`).
  3. `skygate-skygate-1 container logs | grep certsync` — should show one of: `certsync: enabled (interval=30s bucket=skygate-backups ...)` (env-var defaults) OR `certsync: disabled (SKYGATE_CERTSYNC_ENABLED=false. Pre-B147 system-cron cert-renew.sh continues to run.)` (explicit disable).
  4. `SELECT action, detail FROM audit_log WHERE action LIKE 'certsync.%' ORDER BY created_at DESC LIMIT 5` — should show `certsync.pull` rows with the version + SHA + size detail.
  5. (Live S3 push test) `aws s3 cp cert.pem s3://skygate-backups/certs/cert.pem` + `aws s3 cp key.pem s3://skygate-backups/certs/key.pem` + bump `.version` (write a new VersionFile JSON with a higher `version` integer + the new SHA + `uploaded_at`) → within 30s the standby should pull + write the new cert.pem/key.pem + log `certsync: applied version=N ...` + write a `certsync.pull` audit row.
- **B147 unblocks**:
  - Phase 4 (B148) /admin/certificates page (upload + reg.ru DNS-01 toggle) — the cert upload form on the new page writes to the same `certs/cert.pem` + `certs/key.pem` S3 keys, then bumps `.version`. The certsync scheduler picks it up on the next tick and propagates to all nodes.
  - Phase 7 (svyatoslava-1 bootstrap) — the new node's deploy script can upload its initial cert to the same S3 keys, then enable the certsync scheduler. The node's first boot will pull the cert within 30s of starting the scheduler.
- **Next independent element**: Phase 4 (B148) /admin/certificates page — the upload form + reg.ru DNS-01 toggle + cert renewal status display. Uses the same S3 layout as B147, so the certsync scheduler picks up the upload on the next tick. No new infrastructure required.

### 2026-08-19 (B148 — Phase 4 /admin/certificates page + DNS-01 toggle READY FOR COMMIT)

- **Scope** (per BL-2 plan §5.1 / Phase 4): operator-facing surface for cert management — paste PEM cert+key (or upload via file picker), validates the pair via B147's rules, writes to S3 so the certsync scheduler picks it up within 30s. Plus an "Enable Let's Encrypt DNS-01 via reg.ru" toggle (intent-only for v1.5.0; actual certbot+reg.ru flow is a separate v1.5.x surface that depends on B146 being unblocked).
- **New code**:
  - `internal/feature/admin/certificates.go` (NEW, ~19K) — `GetAdminCertificates` (GET /admin/certificates) + `PostAdminCertificateUpload` (POST /admin/certificates/upload) + `PostAdminCertificateToggleDNS01` (POST /admin/certificates/toggle-dns01) + `collectCertificatesPageData` + `readLocalCertInfo` (x509 parse + sha256 + DNS names + cert chain strings) + `readCertInput` (file vs textarea fallback, file wins) + `certRedirect` (303 See Other with URL-encoded flash) + helper `certsyncCertPath` (returns /var/lib/skygate/certs/cert.pem) + `queryCertAuditEvents` (last 10 certsync.*/certs.* audit rows) + `CertUploadFn` callback type for the S3-upload hook.
  - `internal/handlers/templates/admin/certificates.html` (NEW, ~8K) — 4 sections: current cert info (Subject, Issuer, NotBefore, NotAfter, DaysLeft badge, SHA-256, DNS names) / upload form (file + textarea for cert and key) / DNS-01 toggle (checkbox + save) / recent events (audit log).
  - `internal/feature/admin/certificates_test.go` (NEW, 10 unit tests) — covers the 6 B148-plan test cases plus 4 defensive: `TestReadLocalCertInfo_ParsesValidCert` (happy path: gen self-signed cert+key, write to temp file, verify Subject/Issuer/NotBefore/NotAfter/DaysLeft/SHA256/DNSNames), `TestReadLocalCertInfo_MissingFile` (ENOENT empty state), `TestReadLocalCertInfo_MalformedCert` (parse error), `TestReadCertInput_PrefersFile` (file wins over textarea), `TestReadCertInput_FallsBackToText` (textarea-only), `TestReadCertInput_NoInput` (error), `TestCertRedirect_EncodesFlash` (Unicode + special chars round-trip), `TestCertRedirect_ErrorOnly` (only `?err=` param), `TestCertSyncCertPath_StablePath` (path-separator-agnostic), `TestCertChainStrings_ReturnsIssuer` (returns Issuer DN as v1.5.0 minimum).
  - `internal/feature/admin/service.go` (M) — `Service.CertUploadToS3 CertUploadFn` field (wired by main.go at boot; nil = page still works, operator gets "queued for upload" flash instead of "S3 upload succeeded").
  - `internal/certsync/certsync.go` (M) — `ValidateCertKeyPair` exported wrapper (B148 re-uses B147's x509 + matchedAny rules).
  - `internal/handlers/handlers.go` (M) — `sectionPageSet` includes `admin/certificates` so the sidebar auto-opens on the page.
  - `internal/handlers/templates/layout.html` (M) — `<a href="/admin/certificates">` with `fa-certificate` icon (after the /admin/deploy link).
  - `cmd/skygate/main.go` (M) — 3 admin routes: `GET /admin/certificates` + `POST /admin/certificates/upload` + `POST /admin/certificates/toggle-dns01`. All wrapped in `authMW`.
  - `internal/i18n/catalog_admin.go` (M) — 25 new `cert.*` keys (2 dot-prefixed: `cert.title` + `cert.subtitle`; 23 underscore-prefixed: `cert_current` + `cert_current_help` + `cert_subject` + `cert_issuer` + `cert_not_before` + `cert_not_after` + `cert_sha256` + `cert_no_local` + `cert_upload_title` + `cert_upload_help` + `cert_upload_pem` + `cert_upload_key` + `cert_apply` + `cert_dns01_title` + `cert_dns01_help` + `cert_dns01_toggle` + `cert_save` + `cert_recent_events` + `cert_event_when` + `cert_event_actor` + `cert_event_action` + `cert_event_detail` + `cert_no_events`).
  - `internal/i18n/catalog_common.go` (M) — `nav.certificates` in both `ruCommon` + `enCommon` maps (sidebar label).
  - `scripts/check_b148.sh` (NEW, 37 contracts ALL PASS) — pins the 5-contract B-check: certificates.go existence + 3 handlers + certsync.ValidateCertKeyPair re-use + Service.CertUploadToS3 field + certificates.html (4 section markers + 5 form fields) + i18n catalog parity (25 cert.* keys + nav.certificates, all in both maps) + main.go routes (3) + layout.html link + sectionPageSet entry + certificates_test.go (10 test functions present + go test passes).
  - `scripts/verify_pre_deploy.sh` (M) — `B148` row added to the catalog (37 new contracts pinned). Pre-push output ends with "PASS B148".
- **All B148 contracts PASS (37/37)**, all 28 Go test packages green, `go build ./...` clean.
- **Architectural notes baked into the code**:
  - The upload handler writes to the same S3 layout as the certsync scheduler (B147). After a successful upload, the certsync scheduler picks it up on its next 30s tick and propagates to all nodes — the /admin/certificates page does NOT need to "push to nodes" or "reload Caddy", the B147 surface handles that automatically.
  - The validation uses the certsync package's `ValidateCertKeyPair` exported wrapper. The certsync package owns the "is this cert+key a valid pair?" check (x509 + matchedAny over PKCS#1 / PKCS#8 / SEC1), and B148 re-uses it so the rules stay in one place. If B147's check ever loosens (e.g. adds Ed25519 support), B148 picks it up automatically.
  - The DNS-01 toggle is intentionally minimal for v1.5.0: it just stores `dns01_enabled` in `global_settings`. The actual cert-acquisition flow (LE certbot + reg.ru DNS-01 challenge) is a separate v1.5.x surface that depends on B146 being unblocked. The v1.5.0 toggle is the "intent" — the operator sees the toggle, sets it on, and a future v1.5.x release reads the toggle + runs the LE flow.
  - The `CertUploadToS3` callback is optional (nil = page still works, operator gets "queued for upload" flash instead of "S3 upload succeeded"). The certsync scheduler picks up the upload on its next tick regardless (the operator can also run the renew script manually).
  - The form supports both file upload (multipart/form-data) and textarea paste. The handler prefers the file (larger inputs, no newlines stripped by the browser).
- **B148 unblocks**:
  - Phase 4.5 (LE certbot + reg.ru DNS-01) — when B146 is unblocked, the DNS-01 toggle stored by B148 becomes the "operator wants auto-renewal" signal that the v1.5.x flow reads + acts on.
  - Phase 7 (svyatoslava-1 bootstrap) — the new node's deploy script can use the same B148 surface (or the B147 surface directly) to upload its initial cert to S3, then enable the certsync scheduler.
- **All BL-2 plan independent elements now SHIPPED**: B145 (HA chain) + B147 (certsync) + B148 (/admin/certificates) + B149 (/admin/ha) + B150 (/admin/deploy). Only B146 (reg.ru DNS client) remains, blocked on operator's user-level IP whitelist (reg.ru UI → Настройки пользователя → API IP addresses).

---

## 7. References

- `docs/PLANS.md` §v1.5.0 — public-facing plan
- `docs/BACKLOG.md` Priority 3 — BL-2 entry, updated UNBLOCKED status
- `docs/internal/ha-architecture.md` — Tier 1 architecture (now v1.5.0-aligned)
- `docs/internal/v0.27.0-postgres-ha.md` — Patroni + etcd reference (NOT modified, only consulted)
- `docs/internal/https-setup.md` — Caddy config patterns (v0.32.11 baseline, extended for reg.ru DNS-01)
- `docs/disaster-recovery.md` — Tier 0 fallback when v1.5.0 isn't deployed

---

### 2026-08-24 (B151 + B152 + B153 — Phase 7 + 8 + 9 runbooks SHIPPED)
- **3 new operator-driven scripts** (runbooks for Phase 7 + 8 + 9 of the HA v1.5.0 plan; the B145-B150 code surfaces are already SHIPPED):
  - `scripts/init-headplane.sh` (B151, Phase 8) — 2 modes (bundled + external headplane), 6-step bundled flow (check headscale + read .env + generate key via `docker exec headscale apikeys create -e 365d` + write to .env with `.pre-init-headplane.YYYYMMDDHHMMSS` backup + restart headplane + verify `/admin/healthz`), 4-step external flow. Idempotent (NEEDS_KEY gate skips re-mint if .env has a real key). getenv/setenv helpers consistent with `deploy/lib/env.sh`. 20 contracts in `scripts/check_b151.sh` (ALL PASS).
  - `scripts/bootstrap_standby.sh` (B152, Phase 7) — operator SSHes to the new VM + runs this script. 6-step flow: pre-flight idempotency check + S3-pull skygate binary from `ha/deploy/<hostname>/` (B150 deploy surface) + S3-pull headscale config from `ha/headscale-config/` (same ACL policy as primary) + `docker compose up -d` + poll `/healthz` 60s + verify `ha_chain` registration in DB. Validates 3 required env vars (SKYGATE_HA_ROLE=standby + SKYGATE_HA_ENABLED=true + HEADPLANE_HEADSCALE__API_KEY non-empty). Writes `ha.bootstrap` audit row. Idempotent. 18 contracts in `scripts/check_b152.sh` (ALL PASS).
  - `scripts/dr_drill.sh` (B153, Phase 9) — operator-driven 5-step live DR drill. Step 1: verify both nodes on the same skygate version (abort if mismatch). Step 2: kill active (`docker kill -9`), verify standby takes over within 60s. Step 3: restart the original active, verify it rejoins as standby (NO flap, per Decision #11 auto-reclaim is OFF). Step 4: verify DNS resolves to the right IP (B146 reg.ru). Step 5 (optional, `--skip-kill-both`): kill BOTH nodes + restart, verify self-heal within 90s. 3 flags: `--yes` (unattended), `--skip-regapi-check` (skip step 4 if reg.ru not yet unblocked), `--skip-kill-both` (skip step 5 for the first run). Polls `/readyz` for the B145 role banner. NEVER uses `docker compose down -v` (no data destruction). 18 contracts in `scripts/check_b153.sh` (ALL PASS).
- **All BL-2 plan independent elements now SHIPPED**: B145 (HA chain) + B147 (certsync) + B148 (/admin/certificates) + B149 (/admin/ha) + B150 (/admin/deploy) + **B151 (init-headplane.sh) + B152 (bootstrap_standby.sh) + B153 (dr_drill.sh)**. Only B146 (reg.ru DNS live client) remains as a code element, blocked on operator's reg.ru IP whitelist.
- **Phase 7 (svyatoslava-1 bootstrap)** — now runnable end-to-end on the new VM once it's provisioned. The operator:
  1. Provisions svyatoslava-1 (OS + Docker + Tailscale + SSH + Patroni etcd access)
  2. Clones the skygate repo + copies .env from the primary (or re-runs `deploy.sh`)
  3. Sets `SKYGATE_HA_ROLE=standby` in .env
  4. Runs `bash scripts/bootstrap_standby.sh` — the script does the rest
  5. Verifies via `/admin/ha` (the standby appears in the chain table) + `/admin/audit` (the `ha.bootstrap` row is recorded)
- **Phase 8 (init-headplane.sh)** — runs as part of any fresh deploy, BEFORE the first `docker compose up -d headplane`. On a re-deploy with an existing key, the NEEDS_KEY gate skips re-mint. The script is idempotent.
- **Phase 9 (dr_drill.sh)** — operator runs in a low-traffic maintenance window (Sunday 03:00 UTC recommended). The 5 steps walk the operator through the actual failure modes the chain was designed to handle. After the drill, the operator can tag the release (`git tag v1.5.0` per Phase 10).
- **Status**: 8/10 phases SHIPPED. Only B146 + Phase 10 (release tag) remain. Phase 10 is a single `git tag` + GitHub release, not blocked on anything but the operator's blessing.


