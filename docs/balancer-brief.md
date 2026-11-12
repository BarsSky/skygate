# Brief for the balancer — how to work on this repository without inventing facts

**Audience:** the model/agent that will pick up the open items below.
**Written:** 2026-10-29, after an audit of an agent answer that was ~52 % accurate
and whose two headline conclusions were both wrong.
**Status of this file:** a handoff document. It is not a plan of record — the
plan of record is [`docs/ROADMAP.md`](ROADMAP.md).

---

## 1. Why this document exists

An agent was asked: *"проверить, как работает миграция данных (экспорт и импорт
настроек) и работоспособность подъёма кластера HA; что осталось проверить и
реализовать"*. It answered with two confident tables and a numbered plan.

Both headline conclusions were false:

| Its conclusion | Reality |
|---|---|
| "HA check scripts **do not exist** for B204/B205/B218/B225/B226 — create all five" | All five exist **and are registered** in `scripts/verify_pre_deploy.sh` (lines 4480, 4477, 3772, 3793, 3802). They run green: `B204 30/30`, `B205 35/35`, `B218 20 passed`, `B225 13 passed`, `B226 25 passed` |
| "`skygate oidc-export` is the settings export" | It prints the **OIDC block only** (5 fields). Settings live in the database; the documented path is `/admin/backup` + `scripts/restore.sh` |

It also invented `skygate ha init`, `skygate standby join`, `skygate ha status`,
and the tables `ha_cluster_nodes` / `ha_heartbeat`. None exist. The real
interface is `skygate cluster invite|join|nodes|dbs|audit|failover|failover-drill|upgrade|heartbeat-daemon`,
and the tables are `cluster`, `cluster_node`, `cluster_database`,
`cluster_migration`, `cluster_invite`, `cluster_audit`.

### How the failure happened (this is the part worth copying into your process)

1. It printed shell commands (`grep -r "skygate ha init"`, `SELECT * FROM
   ha_heartbeat`) **without running them**, then treated "0 matches" as a fact
   about the product. The pattern it searched for was itself invented, so 0 was
   guaranteed.
2. It read the truncated block index in `AGENTS.md` as if those lines were
   descriptions. They are not — `AGENTS.md` says so about itself: *"Where the
   line reads as a cross-reference rather than a description, treat it as a
   pointer."* The index's B218/B225/B226 lines are mangled cross-references, and
   their real subjects are: B218 = `skygate init` role presets, B225 = Telegram
   alerts on Patroni failover, B226 = the Prometheus `/metrics` exporter.
3. It never opened `cmd/skygate/cluster.go`, `cmd/skygate/init.go` or
   `docs/ha.md` — three files that would have contradicted it in one minute.

---

## 2. Rules this repository expects you to follow

These are non-negotiable and are already enforced by contracts:

1. **"It is absent" requires a command you actually ran, with its output
   quoted.** Name the file you looked for and the search you performed. Search
   for **verbs in the dispatcher** (`case "..."` in `cmd/skygate/*.go`) and for
   **table names in `internal/db/migrations_*.go`** — never for the phrasing of
   a doc line.
2. **The block index in `AGENTS.md` is a pointer index, not a specification.**
   The authority for any `BNNN` is `scripts/check_bNNN_*.sh` plus its
   `run_check "BNNN"` entry in `scripts/verify_pre_deploy.sh`.
3. **One change, one contract.** New behaviour ships with
   `scripts/check_bNNN_*.sh`, registered in the catalog, plus one line in the
   block index. Take the next free block number — `B459` is now used.
4. **Never reuse a block number.** `B203` is the skygate-watchdog
   (`ResettableDB` + `DBSwap`). The Qwen answer proposed
   `check_b203_export_import.sh`, which would have collided.
5. **A check that matches nothing must never report green.** If a selection or
   a search yields zero, that is a broken invocation, not a pass.
6. **Fixed `/tmp` paths are forbidden** in checks (`mktemp -d` + `EXIT` trap).
7. **Verify on the host, not on Windows.** Local Windows is for editing. `bash`
   on this workstation is the WSL launcher, not Git Bash — use
   `C:\Program Files\Git\usr\bin\bash.exe` or `/usr/bin/bash`.
8. **The gate is the contract:** `bash scripts/verify_pre_deploy.sh` must end
   with `0 FAIL`. Live-state checks must `SKIP`, never `FAIL`, when their
   dependency is absent.

---

## 3. What has already been done (do not redo it)

Block **B459** — *the backup must speak the database's dialect*. Measured live on
the standby host (native install, no docker, SQLite = the v1.5.4+ default):

| Defect found | Fix |
|---|---|
| `scripts/backup.sh` — the script `internal/backup/runner.go` runs for the in-app «Создать сейчас» — had **no SQLite path at all**. `load_dsn()` answered `SKYGATE_DB_DSN is not set` and the run failed; v1.3.1 had deleted the SQLite branch in the era when PostgreSQL was the only backend, and v1.5.4 made SQLite the default again | Step 3 dispatches on `SKYGATE_DB` (`sqlite:` / `file:` / bare path → the FILE; `postgres://` or `SKYGATE_DB_DSN` → `pg_dump`). WAL-checkpoints via `sqlite3` when present, carries `-wal`/`-shm` otherwise |
| An archive could be produced with the database missing (the pre-B459 SQLite branch of `deploy/backup.sh` copied a docker **volume** and only `warn`ed on a native host) | An empty copy, a non-SQLite copy, or a missing file now **fails the run**; `deploy/backup.sh` uses `die` instead of `warn` |
| `scripts/verify_backup.sh` classified every SQLite archive as a "v0.33.x SQLite-era" relic and **failed** it — every SQLite install's backups would have been marked unhealthy forever | SQLite archives are verified: header magic, `PRAGMA integrity_check`, table count, critical tables |
| `verify_backup.sh` ignored its positional argument; `verify_backup.sh /path` printed *"nothing to verify"*, **exit 0** — green about an archive it never opened | The explicit destination is honoured |
| The integrity check was gated on `docker info` alone, so a native SQLite install reported *"integrity skipped"* | Dialect-first: `PRAGMA integrity_check` for SQLite, docker-gated PG checks unchanged |
| `skygate init status` called `db.OpenDSN` (the PostgreSQL-only opener) and died with `cannot parse 'sqlite:/…': failed to parse as keyword/value`, while `migrate-only` opened the same file fine | Uses `db.OpenWithDialect`. The leak was invisible on PostgreSQL installs, which is why it survived |
| **The same leak affected ALL 16 CLI subcommands** — `cluster invite\|join\|nodes\|dbs\|audit\|failover\|…`, `acl-apply`, `regapi-credentials`, `init bootstrap`, `init status`, `migrate`, `derp-probe`, `oidc-export`. `db.OpenDSN` is `sql.Open("pgx", dsn)` and its own doc comment still said *"v1.3.0: skygate is PG-only"* — stale since v1.5.4 made SQLite the default | New `cmd/skygate/cli_db_b459.go` → `openCLIDB(cfg)` on `db.OpenWithDialect` (a strict superset: same migration chain per dialect). All 16 sites swapped, zero remain |
| **`skygate oidc-export` exported the WRONG configuration on every SQLite install**: it treated the open failure as a soft warning, printed the `SKYGATE_OIDC_*` env values and exited **0**, so `deploy/skygate-apply-oidc.sh` would write those into headscale. The same "warning + env values + exit 0" happened whenever the `oidc_settings` row was unreadable (`no such table` on a blank DB) | An unreadable database **and** an unreadable settings row are errors with a named reason; `--from-env` is the explicit opt-out for a genuinely env-only install |
| `skygate oidc-export --env` (documented in `RELEASE-NOTES.md:10338`) was rejected with `unknown flag`, and the help text did not mention it | `--env` and `--from-env` are parsed and listed in `--help` |
| `docker run` steps in `scripts/backup.sh` printed `docker: command not found` into the operator's log on a native host | Guarded on `command -v docker`, with a named reason for the skip |
| `validateImportedACL` checked four top-level keys and **nothing else**, so a policy with a wildcard in `tagOwners` (`"tag:server": ["tag:dev-alice-*"]`) passed the dry-run and failed at apply with headscale's `src=tag not found` — a green preview and a red apply with no named cause | `wildcardTagOwnerProblem` refuses a wildcard owner and a wildcard tag key, naming the tag; keys are visited in sorted order so the message is deterministic. 7 behavioural tests (refuse owner, refuse key, refuse bare `*`, accept concrete, accept empty, refuse non-list, determinism over 50 runs) |
| Three deploy scripts wrote their `audit_log` row through `psql` (PostgreSQL) using SQLite's `strftime()`: `scripts/bootstrap_standby.sh`, `deploy/scripts/setup-skygate-public.sh`, `deploy/scripts/create-standby-preauth.sh` | The row DID land — the PG chain installs a `strftime` shim — but that shim **ignores its format argument** and always returns the epoch, so the ISO-shaped `initiated_at` received a number, and the INSERT depended on a shim the project is retiring (B420). Now `EXTRACT(EPOCH FROM now())::bigint` + `to_char(now() AT TIME ZONE 'UTC', …)` |
| `check_b429_timezone.sh` contract A4 reported **`FAIL: internal/localtime tests failed:` with an EMPTY detail** on any shell without `go` on `PATH` (AGENTS trap #8) — the failure grep looks for `--- FAIL`/`FAIL`, while the real output was `go: command not found`. It would have reddened the gate on a machine where nothing is wrong | A `skip()` helper and a `command -v go` guard: `34 passed / 0 failed / 1 skipped` without Go, `35 / 0 / 0` with it, `rc=0` both ways |
| Catalog hygiene: `run_check "B202.5"` was registered **twice** (second copy mangled), and the `B202` description was truncated mid-sentence | Duplicate removed; `B202` described from its 24 actual contracts, and marked explicitly as the **database**-migration tool, not a settings export |

Contract: `scripts/check_b459_sqlite_backup_native.sh` — **35 contracts**, of
which 7 are behavioural on the extracted backup helpers, 6 pin the CLI dialect
opener and the `oidc-export` contract, and 5 pin the script-side SQL plus the
imported-ACL wildcard rule. 7 Go tests live in
`internal/feature/admin/acl_import_b459_test.go`.

### Live evidence (standby host, SQLite, native, no docker)

```
EXPORT   exit=0   PRAGMA integrity_check = ok, tables = 47
ARCHIVE  skygate.db, skygate.env, skygate-repo.bundle, skygate-schema.txt, inventory.txt
VERIFY   OK — healthy (SQLite integrity_check=ok, 47 tables, critical tables present)  exit=0
NEGATIVE missing database  -> exit=1, NO archive produced            (correct)
CORRUPT  appended garbage  -> exit=1                                 (the verifier can fail)
IMPORT   marker=exported-ok, portal_users=1, integrity ok
APP      init status on the imported SQLite DB: exit=0, cluster/node/database rendered
CLI      cluster nodes|dbs|audit on SQLite: exit=0 (every verb used db.OpenDSN before)
OIDC     --env exit=0; unreadable settings row exit=1; --from-env exit=0; healthy DB exit=0
```

**The host stays isolated** (`/srv/b459`, port 18080, its own database). The
project was **not** switched to this host and must not be until the operator says
so — that move is a joint decision (see §6).

---

## 4. Open items — in priority order

### 4.1 Operator-gated verification (highest value, cannot be done by an agent alone)

| # | Item | Why it matters | Acceptance |
|---|---|---|---|
| 1 | **Live DR drill on the real pair.** The drill scripts are done (`scripts/ha-phase9.sh`, `scripts/dr_drill.sh`); the drill itself has **never been run** (`docs/ha.md` §7.1 **Q9**) | The difference between "the failover path is code-complete" and "the failover path is proven" | Drill log with timestamps, the promoted node named, and the reclaim verified |
| 2 | **Live end-to-end onboarding of a second host from the panel** (`ROADMAP.md` §5 **RR-15**, B342) | The panel mints invite + tailnet preauth and renders a five-step block; unit-tested only, never run against a second VM | `POST /admin/cluster/onboard` → `GET /admin/cluster?boot=<token>` → node `ready` in `/admin/cluster` |
| 3 | **SQLite → PostgreSQL conversion on a real database** (`skygate db-migrate`, `/admin/database`, B329) | The dialect-pair guarantee is the project's own compatibility promise; it is unit-tested but not exercised on a live install | Row counts per table equal before/after; `schema_parity` green |

### 4.2 Agent-workable defects (measured, not guessed)

| # | Item | Evidence | Acceptance |
|---|---|---|---|
| 4 | ~~`skygate oidc-export` accepts no `--env` flag while `RELEASE-NOTES.md:10338` advertises one~~ **DONE in B459** | parser + help text now carry `--env` / `--from-env`; contracts I3–I5 | live: `oidc-export --env` exit 0, `--help` lists both |
| 5 | ~~`skygate oidc-export` exits 0 when the database is unreachable~~ **DONE in B459** — and the measured defect was larger: on SQLite it could never open the DB at all, so it *always* exported env values with exit 0 | `oidc_export_b304.go`, `cmd/skygate/cli_db_b459.go`; contract I6 | live: unreadable settings row → exit 1 with a named reason; `--from-env` → exit 0 |
| 6 | ~~`validateImportedACL` checks four keys and nothing else~~ **DONE in B459** — a wildcard in `tagOwners` is now refused by name; 7 tests | `wildcardTagOwnerProblem` in `acl_import.go` | live-equivalent: `TestValidateImportedACL_RejectsWildcardOwner_B459` fails without the fix |
| 7 | ~~The headscale **version detector exists but is not wired to ACL import**~~ **DONE in B460 — and deliberately NOT as a gate.** The dry-run now names every plane the apply will push to, with its version (read from the update monitor's **cache**, so rendering a page probes nothing), the declared pin as the labelled fallback, and a **named reason** when neither answers; a mismatch between planes raises a banner | `acl_import_planes_b460.go`, `Service.PlaneVersionFn`, `cmd/skygate/main.go`; contracts B1/B5–B7 | `TestB460_PlaneTableRenders` in RU+EN. Refusing on a version number was rejected on purpose: every 0.29.x difference is handled by a capability ladder, so a gate would block applies that work |
| 8 | ~~Three deploy scripts write their audit row with SQLite's `strftime()`~~ **DONE in B459** — and the real defect was narrower than documented: the row **did** land (PG shim), but the shim ignores its format argument, so `initiated_at` held an epoch | `bootstrap_standby.sh`, `setup-skygate-public.sh`, `create-standby-preauth.sh`; contracts J1 | native PostgreSQL time expressions; the J1 detector is proved on a planted `strftime()` copy |
| 9 | ~~`docs/ha.md` §7.2 **G11** claims that INSERT "fails on a PG cluster"~~ **DONE** — the row now records what was measured (the shim, and that it ignores its format argument), marks the code fixed in B459, and keeps the genuinely separate open nit about step 6's comment | `docs/ha.md` §7.2 | text matches the measurement |
| 10 | ~~`scripts/restore.sh`'s SQLite branch is docker-only and labelled "v0.32.x legacy"~~ **DONE in B461** — the target file is now resolved (env → `TARGET_DIR/.env` → the archive's own `skygate.env`), the previous database is kept as `<db>.pre-restore-<ts>`, a stale `-wal`/`-shm` is removed, an empty or non-SQLite archive file is refused **before** anything is touched, and the docker volume is only a fallback whose failure is a named error | `scripts/restore.sh`; 21 contracts in `check_b461_restore_sqlite_native.sh`, 9 of them behavioural. The behavioural leg found a **real bug in the first version**: the resolver guessed "value vs file path" from the shape of the string, so the non-existent `${TARGET_DIR}/.env` was taken as the database path and the restore would have overwritten that `.env`. E7/E8 pin both halves | native SQLite archive restored by the documented command, end to end — still wants a run on the stand, which is the one thing left in this row |
| 11 | ~~`docs/ROADMAP.md` §5 **BL-2** still says HA Tier 1 is "blocked on DNS credentials"~~ **DONE** — the row is marked shipped (v1.5.0, B200–B226) and now points at what is actually open: Q9, G2, G10. The RU mirror `docs/ru/ROADMAP.md` was corrected in the same pass | EN + RU rows | both files agree with `docs/ha.md` §7.1 |

Also closed in the same pass, outside this table: `docs/backup-restore-and-migration.md`
§1 (the archive-contents table now lists `skygate.db` and the
`SKYGATE_DB_DIALECT` line) and `docs/oidc.md` §2.4 (the `skygate oidc-export` flag
table, including `--from-env`).

### 4.3 Deliberately deferred (do not start without an operator decision)

* `G2` etcd has no quorum (single node) — needs a third VM.
* `G1` the Phase 0 runner needs `python3`; a `jq` fallback is future work.
* `G10` the S3 IAM scope is wider than `ha/*`.
* `G3`/`G4` extra DNS providers and Let's Encrypt DNS-01.
* A single "export all settings to a file" command. **Ask first:** settings
  already travel inside the database dump, and `/admin/backup` is the documented
  path. If a first-class command is still wanted, it needs an explicit allowlist
  of fields (never secrets), a backup before import, RU+EN keys and its own
  block.

---

## 5. How to prove a change is done

```bash
# 1. the contract you added, alone
bash scripts/check_bNNN_your_slug.sh

# 2. the neighbours you touched
bash scripts/check_b202.sh            # 37/37
bash scripts/check_b459_sqlite_backup_native.sh   # 24/24
bash scripts/verify_backup.sh --help  # if you touched it

# 3. the gate, as the user who OWNS the checkout (AGENTS trap #13)
bash scripts/verify_pre_deploy.sh     # must end with 0 FAIL

# 4. on a native SQLite host, the whole loop
scripts/backup.sh /tmp/bk && scripts/verify_backup.sh /tmp/bk
```

A change is not done because the check is green. It is done when a **negative**
case fails for the right reason (a corrupt archive, a missing database, a
wildcard tagOwner) and you have that failure in the transcript.

---

## 6. Open questions for the operator

1. Should `svyatoslava` stay a SQLite-only test stand until the PostgreSQL
   conversion has been exercised somewhere with enough CPU/RAM for PG?
2. When is the maintenance window for the live DR drill (Q9)? Sunday 03:00 UTC
   is the standing recommendation.
3. Should the settings-transfer command be built at all, given that settings
   already travel inside the database dump?
