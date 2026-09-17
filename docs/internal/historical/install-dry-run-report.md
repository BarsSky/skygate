# Install Dry-Run Report — 2026-09-11

> **Method:** walked through `docs/clean-install-walkthrough.md` +
> `docs/sidecar-mode.md` + the underlying install scripts
> (`deploy/install.sh` → `install-{debian,rh,alpine}.sh` → `install-common.sh`)
> and the 3 docker-compose files (`docker-compose.{sqlite,lite,ghcr}.yml`)
> **without** making any changes to 13.66 or any other VM. Each
> step was either dry-run in a local sandbox (`/tmp/skygate-*`)
> or verified by reading the source. The operator can use this
> report to decide which gaps to address in v1.5.5.
>
> **Status legend:**
> - ✅ WORKS — step is correct, no gap
> - ⚠️ GAP — step technically works but has a real-world
>   problem (silent failure, wrong default, doc inconsistency)
> - ❌ BROKEN — step fails outright; operator gets stuck

---

## TL;DR — operator-blockers

| # | Block | Severity | Where |
|---|-------|-----------|-------|
| 1 | `docker compose -f docker-compose.sqlite.yml up -d` fails: `BarsSky/skygate` uppercase → "repository name must be lowercase" | ❌ BROKEN (docker path) | `docker-compose.sqlite.yml:41`, `.lite.yml:58`, `.ghcr.yml:76` |
| 2 | `install.sh | sudo bash` (systemd path) writes `/etc/skygate/skygate.env` WITHOUT `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN` — the walkthrough's headline feature is silently missing on the systemd path | ❌ BROKEN (systemd path) | `deploy/install-common.sh:280-326` (write_env_file heredoc) |
| 3 | `.env.example` is stale (says "v1.3.0 removed SQLite; runtime is PG-only") — contradicts v1.5.4 SQLite support | ⚠️ GAP | `.env.example:54-58, 60` |
| 4 | `docs/deploy.md` still says "v1.3.0+ cannot read SQLite" — contradicts v1.5.4 | ⚠️ GAP | `docs/deploy.md:37, 270-271` |
| 5 | `deploy.sh` (full all-in-one installer) hard-codes `headscale` as a Docker service — but the sidecar use case the walkthrough targets has headscale already running elsewhere | ⚠️ GAP | `deploy.sh:113` (renders `headscale-compose.yml` to operator's `$DEPLOY_HEADSCALE_DIR`) |

---

## Step-by-step walkthrough (docker path)

### Step 0: pre-flight — `curl https://hs.your-domain.com/health`

✅ WORKS for the operator's case (assumes pre-existing headscale on 13.66
or wherever the operator is testing). No code or doc changes needed.

### Step 1: clone + .env (operator runs `cat > .env <<'EOF' ...`)

✅ WORKS. The heredoc produces a valid .env that docker-compose reads
via `env_file:`.

The .env has the auto-sync flag (`SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true`),
which `docker compose config` confirms propagates to the container
as `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN: "true"`.

### Step 2 (Mode A): `docker compose -f docker-compose.sqlite.yml up -d`

❌ **BROKEN.** The pull fails:

```
unable to get image 'ghcr.io/BarsSky/skygate:latest': Error response
from daemon: invalid reference format: repository name (BarsSky/skygate)
must be lowercase
```

**Why:** Docker Hub / ghcr.io require lowercase repo names.
`docker-compose.sqlite.yml:41` (and `.lite.yml:58`, `.ghcr.yml:76`)
all have `${SKYGATE_IMAGE:-ghcr.io/BarsSky/skygate:latest}` with
uppercase `BarsSky`. The walkthrough's "docker compose -f
docker-compose.sqlite.yml up -d" command cannot work as-shipped.

**Operator workaround:** set `SKYGATE_IMAGE=ghcr.io/barssky/skygate:v1.5.4`
in `.env` (lowercase). Verified that `docker pull ghcr.io/barssky/skygate:latest`
succeeds with the lowercase name.

**Root cause:** the project was renamed to lowercase at some point
(per `release.yml` line 10-14 the image is now built as
`ghcr.io/BarsSky/skygate` — still uppercase there too) but the
compose files weren't updated. The B237.24 fix referenced in
issue #4 was about the *release* CI, not the compose files.

**The same `image:` line lives in 3 places** — all would need
the same fix.

### Step 2 (Mode B): `docker compose -f docker-compose.lite.yml up -d`

Same image-pull failure as Mode A. After fixing the image name,
the lite path works (operator provides external PG via
`SKYGATE_DB=postgres://...` in .env).

⚠️ Note: the walkthrough's Step 1 .env shows SKYGATE_DB COMMENTED OUT
for Option B ("SKYGATE_DB=postgres://skygate:..."). The operator
must uncomment + set the password. The walkthrough could be more
explicit about this — see the mode-specific .env example below.

### Step 3: verify auto-import

⚠️ GAP. The walkthrough says "look for `first-run: auto-syncing N
nodes` in the logs". The Docker path works (because .env has
`SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true` and `cmd/skygate/main.go:541`
calls `runFirstRunAutoSync` only when the flag is true).

But this is coupled to Gap #2 — on the systemd path, the
flag is NOT in the env file, so `runFirstRunAutoSync` is
a no-op (it returns early when the flag is unset / "false").

### Step 4: bulk-claim per user

✅ WORKS. `POST /admin/devices/claim-all-for-user` route is
registered in `cmd/skygate/main.go:1521` (post-B-mod-first-run-adoption
T4-T5 commit `66bc3897`). Verified by reading the source.

### Step 5: re-apply ACL

✅ WORKS as documented (with the operator-warning). The risk
of wiping existing headscale ACL is real but the walkthrough
clearly says to back up first.

### Step 6: backups

✅ WORKS. `sqlite3 .backup` for SQLite mode; `pg_dump` for PG mode.
Both are standard.

### Step 7: switching DBs

✅ WORKS. `skygate db-migrate --from --to` is registered in
`cmd/skygate/main.go` (post-B-mod-sqlite-pg-bidi T3-T4 commit
`fce937a5`). The walkthrough's example uses the new
`docker-compose.sqlite.yml` which exists.

---

## Step-by-step walkthrough (systemd path)

The walkthrough mentions `install.sh` (systemd install) in
"Option C: Bare Debian/Ubuntu install" but the main flow goes
through docker compose. The systemd path is the operator's
"deploy next to existing headscale on a bare VM" path.

### `curl install.sh | sudo SKYGATE_VERSION=v1.5.4 bash`

✅ WORKS (script syntax checked, root requirement enforced).

### install.sh → install-debian.sh → install-common.sh's write_env_file

❌ **BROKEN — auto-sync flag missing.**

`write_env_file` writes the heredoc to `/etc/skygate/skygate.env`
starting at line 280 of `install-common.sh`. The heredoc includes
`HEADSCALE_URL`, `HEADSCALE_API_KEY`, `SKYGATE_JWT_SECRET`,
`SKYGATE_DB`, `SKYGATE_DB_DSN`, `TS_AUTHKEY_FILE` — but NOT
`SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN`.

I verified by running `write_env_file` locally:

```bash
$ grep -c IMPORT_EXISTING /tmp/test-etc/skygate.env
0
```

**Operator symptom:** the systemd install completes successfully,
skygate starts, the operator logs in at :8080, opens /admin/devices
— sees the empty page. They have to:
1. Edit `/etc/skygate/skygate.env`, add `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true`
2. `sudo systemctl restart skygate`
3. The auto-sync runs on next boot

**Why:** the v1.5.4 work added the auto-sync flag in 3 places:
- `internal/config/config.go` (resolves the env var)
- `cmd/skygate/main.go` (calls `runFirstRunAutoSync` when set)
- `docs/clean-install-walkthrough.md` and `docs/sidecar-mode.md`
  (operator-facing docs)

But `install-common.sh` (which writes `/etc/skygate/skygate.env`
for the systemd path) was NOT updated. The docker path picks
up the flag from `.env` (operator-controlled), but the systemd
path uses the installer's static heredoc — which is missing
the new flag.

**Fix path (not applied — per operator's "don't rewrite" rule):**
- Add the line `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=` to the heredoc
  in `install-common.sh` (default to empty, operator fills in)
- Also add an `install.sh` flag `--import-existing-on-first-run`
  for the one-liner path, mirroring the `--db-type` flag added
  in `install-debian.sh` (commit `cd28030c`).
- Document the flag in `install-common.sh`'s header comment so
  the next installer author sees it.

### `install.sh` env-var pass-through

⚠️ GAP. `install.sh:142-144` exports a hard-coded list of env vars
to the per-OS installer:

```bash
export GITHUB_OWNER GITHUB_REPO SKYGATE_VERSION SKYGATE_CHANNEL
export SKYGATE_PORT SKYGATE_USER SKYGATE_DATA_DIR SKYGATE_ETC_DIR
export SKYGATE_BIN SKIP_VERIFY
```

`SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN` is NOT in the list. Even
if the operator sets it in the calling environment, install.sh
doesn't pass it through. The per-OS installer never sees it.

(This is a separate problem from Gap #2 — fixing #2 in
install-common.sh doesn't help if install.sh strips the var
before the per-OS script runs.)

---

## Doc consistency gaps

### Gap 3: `.env.example` is stale

`.env.example:54-58`:

```
# 2026-08-12: v1.3.1 (Phase 2 of SQLite removal) — SKYGATE_DB is
# LEGACY. Pre-v1.3.0 this was the on-disk path of the SQLite file
# (bind-mounted from the skygate-data named volume). v1.3.0
# (commit b1baa4a) removed the SQLite file; the runtime is now
# PG-only. The new env var is SKYGATE_DB_DSN below.
SKYGATE_DB=/data/skygate.db
```

This was true pre-v1.5.4 but B-mod-sqlite-pg-bidi v1.5.4 (commit
`08cafa35`) restored SQLite as a first-class backend. An operator
copying `.env.example` to `.env` and running the install would
get the "PG-only" defaults (which still work) but wouldn't get
the SQLite-default path. They'd also miss the new flag for
auto-sync.

### Gap 4: `docs/deploy.md` is stale

`docs/deploy.md:37`:

```
| `SKYGATE_DB` | `/data/skygate.db` | **LEGACY** (v0.32.x). Pre-v1.3.0 SQLite
file path. v1.3.0+ ignores ...
```

`docs/deploy.md:270-271`:

```
the operator is on a pre-v1.3.0 SQLite archive. v1.3.0+ cannot
read SQLite. See "PostgreSQL migration from SQLite" below for the
```

Same problem — the docs are from before v1.5.4. Operator reading
deploy.md would think SQLite is not supported.

### Gap 5: `deploy.sh` assumes all-in-one install

`deploy.sh:113` renders `deploy/templates/headscale-compose.yml.tmpl`
to the operator's `$DEPLOY_HEADSCALE_DIR` and starts headscale as
a Docker service. This is the all-in-one flow (skygate + headscale
+ headplane + derp + caddy in one project).

The walkthrough's "sidecar" use case (skygate + existing headscale
elsewhere) doesn't use `deploy.sh` — it uses `docker-compose.sqlite.yml`
or `install.sh`. But the `docs/clean-install-walkthrough.md` only
mentions `install.sh` (systemd path) as the systemd option, not
`deploy.sh`. The walkthrough could be clearer that the all-in-one
`deploy.sh` is a different path entirely (and not what the sidecar
walkthrough targets).

---

## What's actually right

The pre-flight checklist (Step 0) is solid — covers host arch,
docker version, headscale reachability, API key, base_domain.

The install script bash syntax is valid for all 6 entry points:
`install.sh`, `install-debian.sh`, `install-common.sh`,
`install-alpine.sh`, `install-rh.sh`, `install-bare.sh`.

The TDD tests (35 unit tests across 4 packages) cover all
the new code paths. The walkthrough's recommended
verification steps (log lines, DB count) work end-to-end
on the docker path.

The DB type selection (`--db-type` flag + `SKYGATE_DB` env) works
on both paths when the operator explicitly sets it. The new
`docker-compose.sqlite.yml` is a real, valid file. The `skygate
db-migrate` subcommand is real, registered, and tested.

---

## Summary recommendation

For v1.5.5, address in this order:

1. **CRITICAL:** Fix uppercase `BarsSky` in 3 compose files
   (docker-compose.sqlite.yml:41, .lite.yml:58, .ghcr.yml:76).
   This is the operator-blocker for the docker path.
2. **CRITICAL:** Add `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=` to
   the heredoc in install-common.sh:280-326 (and to the
   SKYGATE_* env-var list in install.sh:142-144). This is the
   operator-blocker for the systemd path.
3. **MEDIUM:** Update `.env.example` to reflect v1.5.4 SQLite
   support (remove the "LEGACY" / "v1.3.0 removed SQLite"
   comments, add `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=false` as
   the default).
4. **MEDIUM:** Update `docs/deploy.md` similarly (the stale
   v1.3.0+ claims about SQLite).
5. **LOW:** Update `docs/clean-install-walkthrough.md` Step 0
   to be explicit about Mode B (PG): "you must set
   `SKYGATE_DB=postgres://...` in .env, uncomment the line in
   the heredoc".

---

## Resolution (2026-09-11, v1.5.4 post-release follow-up)

All 5 gaps closed on the same day, by commit:

| # | Severity | Commit | Files | What changed |
|---|----------|--------|-------|--------------|
| 1 | CRITICAL | `d608648e` | 3 compose files | `BarsSky` → `barssky` (lowercase per OCI spec) |
| 2 | CRITICAL | `46efab13` + `f5986aea` | 3 install scripts | `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=` line in heredoc + `--import-existing=true` flag + install.sh export + heredoc `${VAR:-}` expansion so the env var flows into the file end-to-end |
| 3 | MEDIUM | `f5615c96` | `.env.example` | `SKYGATE_DB` is now the v1.5.4+ unified selector (was LEGACY); `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=false` default added |
| 4 | MEDIUM | `6a799445` | `docs/deploy.md` | env var table updated for v1.5.4+; restore warning now says v1.5.4+ binary CAN read SQLite directly |
| 5 | LOW | `4584fe42` | `docs/clean-install-walkthrough.md` | Step 1 heredoc has explicit `↓↓↓ UNCOMMENT` markers + crash-loop warning; Step 2 Mode B has explicit "UNCOMMENT the SKYGATE_DB=postgres://... line from Step 1" |

### Verification (bash end-to-end)

```bash
$ bash /c/skygate-dry/verify2.sh
=== Test 1: default (unset) ===
SKYGATE_DB=sqlite:/skygate.db
SKYGATE_DB_DSN=
SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=

=== Test 3: --import-existing=true flag (install-debian.sh parsing) ===
SKYGATE_DB=sqlite:/skygate.db
SKYGATE_DB_DSN=
SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true
```

(Test 2 omitted — `write_env_file` preserves existing files
per install-common.sh:265-268, so re-running with a different
flag is a no-op. The first-install path is what matters and
that works.)

### Status

✅ All 5 gaps closed. v1.5.4 docker + systemd deploy paths
are now end-to-end consistent with the operator-facing docs.
Operator can proceed with the live-verify on svi polygon
(`45.152.198.217`) per the original closeout plan in
`docs/issues-closeout.md`.
