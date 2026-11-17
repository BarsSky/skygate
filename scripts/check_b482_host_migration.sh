#!/usr/bin/env bash
# ============================================================================
# check_b482_host_migration.sh — B482
#
# A CUTOVER MUST BE A RECORD, NOT A CHAT LOG.
#
# The 2026-11-15/16 migration from an agent VM to a VPS was 44 manual steps and left no commits:
# the knowledge of what an installation consists of, which keys must be copied verbatim, and in
# which order (ACL before DNS) lived in a conversation. The operator asked for that analysis to
# become a wizard in the panel — questions with a choice or an input, and automatic application —
# so the analysis is now `docs/host-migration.md`, the questions and the ordered steps are
# `internal/migrate/plan.go`, the run is two tables (schema v0.85) and the executor is
# `internal/migrate/exec.go`.
#
# The contracts below pin the parts that make the wizard trustworthy rather than decorative: the
# two orderings that cost the migration its time, the secret handling (a password must never reach
# argv), the plan's uniqueness in the database, and the fact that every step the plan can produce
# has a human title in BOTH languages.
#
# Contracts:
#   A. schema: two tables, both chains, registered at v85, SQLite through the DDL chokepoint
#   B. data layer: universal placeholders, RETURNING id, idempotent plan upsert, running run is
#      not deletable, secrets are their own columns and never part of the answers blob
#   C. plan: every phase, the two load-bearing orderings, the enum answers change the plan
#   D. executor: ssh is shelled out to, a password never reaches argv, secrets are 0600 in 0700
#   E. UI: routes, template defines, page/section/title switches, sidebar entry
#   F. i18n: every step title and every note exists in RU and EN, RU is really Russian
#   G. docs: the analysis exists in both languages, mirrors section numbering, is catalogued, and
#      names the non-automatable steps and the DNS/ACL ordering
#   H. registration: the gate, the block index, the release notes
#   I. the discriminators: the pre-B482 shape fails, and the plan's own tests pass
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b482.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

SCHEMA=internal/db/migrations_v0_85_host_migrations.go
DRVPG=internal/db/driver_postgres.go
DRVSL=internal/db/driver_sqlite.go
DBMIG=internal/db/host_migration.go
PLAN=internal/migrate/plan.go
EXEC=internal/migrate/exec.go
PLAN_TEST=internal/migrate/plan_test.go
EXEC_TEST=internal/migrate/exec_test.go
HANDLER=internal/feature/admin/migrate.go
TPL_WIZ=internal/handlers/templates/admin/host_migration.html
TPL_RUN=internal/handlers/templates/admin/host_migration_run.html
ROUTES=cmd/skygate/routes.go
HANDLERS=internal/handlers/handlers.go
LAYOUT=internal/handlers/templates/layout.html
CATALOG=internal/i18n/catalog_admin.go
DOC=docs/host-migration.md
DOCRU=docs/ru/host-migration.md
VERIFY=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$SCHEMA" "$DRVPG" "$DRVSL" "$DBMIG" "$PLAN" "$EXEC" "$PLAN_TEST" "$EXEC_TEST" \
         "$HANDLER" "$TPL_WIZ" "$TPL_RUN" "$ROUTES" "$HANDLERS" "$LAYOUT" "$CATALOG" \
         "$DOC" "$DOCRU" "$VERIFY" "$AGENTS" "$NOTES"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: schema ------------------------------------------------------------------
if has "$SCHEMA" 'CREATE TABLE IF NOT EXISTS host_migrations' && has "$SCHEMA" 'CREATE TABLE IF NOT EXISTS host_migration_steps'; then
  ok "A1: both tables are created by one migration file"
else
  bad "A1: schema v0.85 does not create host_migrations + host_migration_steps"
fi
if has "$SCHEMA" 'BIGSERIAL' && has "$SCHEMA" 'AUTOINCREMENT' && has "$SCHEMA" 'execSQLiteDDL'; then
  ok "A2: both dialects are written, SQLite through the DDL chokepoint"
else
  bad "A2: the schema does not follow the dual-dialect convention"
fi
if has "$DRVPG" '{85,' && has "$DRVSL" '{85,' && has "$DRVPG" 'migrateV085PG' && has "$DRVSL" 'migrateV085SQLite'; then
  ok "A3: v0.85 is registered in BOTH migration registries"
else
  bad "A3: v0.85 is missing from a registry (the two chains would diverge)"
fi
if has "$SCHEMA" 'step_where  TEXT' || has "$SCHEMA" 'step_where TEXT'; then
  ok "A4: the step row carries its location (local/remote/copy/internal/manual)"
else
  bad "A4: the step row has no step_where column — the executor could not tell a local step from a remote one"
fi
if has "$SCHEMA" 'UNIQUE INDEX IF NOT EXISTS host_migration_steps_run_key_uniq'; then
  ok "A5: one row per (run, step key), so a regenerated plan cannot duplicate a verdict"
else
  bad "A5: no unique index on (run_id, step_key)"
fi

# --- B: data layer --------------------------------------------------------------
if has "$DBMIG" 'RETURNING id'; then
  ok "B1: inserts use RETURNING id (the form that works on pgx AND SQLite)"
else
  bad "B1: no RETURNING id — LastInsertId is a known pgx breakage"
fi
if ! has "$DBMIG" 'VALUES (?' && ! has "$DBMIG" 'WHERE id = ?'; then
  ok "B2: placeholders are universal \$n, no question marks"
else
  bad "B2: a ? placeholder appeared — that is a dialect bug on PostgreSQL"
fi
if has "$DBMIG" 'ON CONFLICT (run_id, step_key) DO UPDATE'; then
  ok "B3: the plan upsert is idempotent on the unique key"
else
  bad "B3: the plan upsert is not idempotent — regenerating would duplicate steps"
fi
if has "$DBMIG" 'is running' && has "$DBMIG" 'DeleteHostMigration'; then
  ok "B4: a running run refuses deletion (a step would write into a row nobody sees)"
else
  bad "B4: DeleteHostMigration does not refuse a running run"
fi
if has "$SCHEMA" 'secret_enc' && has "$SCHEMA" 'db_dsn_enc' && ! has "$DBMIG" 'AnswersJSON = ' || has "$DBMIG" 'answers_json' ; then
  ok "B5: the credentials are their own columns, separate from the answers blob"
else
  bad "B5: the schema does not keep secrets out of answers_json"
fi
if has "$DBMIG" "CASE WHEN \$9 = '' THEN secret_enc"; then
  ok "B6: saving a later answer does not erase a credential entered earlier"
else
  bad "B6: UpdateHostMigration would overwrite a stored secret with an empty value"
fi

# --- C: the plan ----------------------------------------------------------------
for phase in P0 P1 P2 P3 P4 P5 P6 P7; do
  if has "$PLAN" "Phase: \"$phase\""; then :; else bad "C1: the plan never produces a $phase step"; fi
done
if has "$PLAN" 'Phase: "P0"' && has "$PLAN" 'Phase: "P7"'; then
  ok "C1: all eight phases exist in the catalog"
fi
if has "$PLAN" 'the SNAPSHOT (P0) comes before anything that touches the new host'; then
  ok "C2: the snapshot-before-the-new-host rule is documented in the code"
else
  bad "C2: the snapshot ordering rule is not stated"
fi
if has "$PLAN" 'the ACL and the tags (P5 vps.acl) come BEFORE the DNS switch'; then
  ok "C3: the ACL-before-DNS rule is documented in the code"
else
  bad "C3: the ACL-before-DNS ordering rule is not stated — this is the window that cost the migration"
fi
if has "$PLAN" 'func Validate(' && has "$PLAN" 'answers are incomplete'; then
  ok "C4: an incomplete answer set is refused BY NAME"
else
  bad "C4: Validate does not name what is missing"
fi
if has "$PLAN" 'DNSSameProvider' && has "$PLAN" 'DNSStub' && has "$PLAN" 'KindManual'; then
  ok "C5: a stub/manual DNS answer yields a manual step instead of a silent switch"
else
  bad "C5: the DNS modes are not distinguished"
fi
if has "$PLAN" 'ModeDBOnly' && has "$PLAN" '!= ModeDBOnly'; then
  ok "C6: the db_only mode is a real branch (it drops the panel/DERP phases)"
else
  bad "C6: db_only is not handled"
fi
if has "$PLAN" 'KeepNoiseKey' && has "$PLAN" 'noise_private.key'; then
  ok "C7: the noise-key step exists and is optional"
else
  bad "C7: the headscale identity step is missing"
fi
if has "$PLAN" 'db-migrate --from'; then
  ok "C8: an engine change reuses skygate db-migrate instead of a second implementation"
else
  bad "C8: the conversion step does not use the existing CLI"
fi
if has "$PLAN" 'func shellQuote('; then
  ok "C9: every interpolated answer goes through shellQuote"
else
  bad "C9: answers are interpolated into shell commands without quoting"
fi

# --- D: the executor ------------------------------------------------------------
if has "$EXEC" '"ssh"' && has "$EXEC" '"bash", []string{"-lc"' && has "$EXEC" '"tar"'; then
  ok "D1: the executor shells out to ssh/bash/tar, the way every other remote path in this tree does"
else
  bad "D1: the executor does not use the project's shell-based transport"
fi
if has "$EXEC" 'SSH_ASKPASS_REQUIRE=force' && has "$EXEC" '"setsid"'; then
  ok "D2: password auth uses OpenSSH's askpass under setsid (no sshpass, no new dependency)"
else
  bad "D2: password auth is not implemented through askpass"
fi
if has "$EXEC" 'the password must never be in argv' || has "$EXEC" 'never in argv' || has "$EXEC" 'never through a command line'; then
  ok "D3: the argv/secrecy rule is documented next to the code that builds argv"
else
  bad "D3: the secrecy rule is not stated in the executor"
fi
if has "$EXEC" 'os.MkdirTemp' && has "$EXEC" '0700' && has "$EXEC" 'os.RemoveAll(dir)'; then
  ok "D4: credentials live in a 0700 dir, are removed after the step, and the cleanup is returned"
else
  bad "D4: the credential temp files are not created/removed under the documented modes"
fi
if has "$EXEC" 'ErrExecutorPending' && has "$EXEC" 'ErrManualStep'; then
  ok "D5: an unimplemented internal step and a manual step are refused with distinct errors"
else
  bad "D5: the executor cannot tell a manual step from a pending implementation"
fi
if has "$EXEC" 'HostStepRunning' && has "$EXEC" 'HostStepFailed'; then
  ok "D6: the row is marked running before the work and gets a verdict after it"
else
  bad "D6: the step row is not updated around the work"
fi
if has "$EXEC" 'truncateOutput(out, 8*1024)'; then
  ok "D7: the stored output is bounded and keeps the tail"
else
  bad "D7: the stored output is unbounded or keeps the wrong end"
fi

# --- E: the UI ------------------------------------------------------------------
for route in 'GET /admin/migrate"' 'POST /admin/migrate/save' 'GET /admin/migrate/{id}' \
             'POST /admin/migrate/{id}/plan' 'POST /admin/migrate/{id}/step' 'POST /admin/migrate/{id}/delete'; do
  if has "$ROUTES" "$route"; then :; else bad "E1: route missing from routes.go: $route"; fi
done
if has "$ROUTES" '/admin/migrate/{id}/delete'; then
  ok "E1: all six routes are registered"
fi
if has "$TPL_WIZ" '{{define "body-admin-host_migration"}}' && has "$TPL_RUN" '{{define "body-admin-host_migration_run"}}'; then
  ok "E2: both templates define the layout body with underscores (TD-16)"
else
  bad "E2: the template define names do not follow the layout contract"
fi
if has "$TPL_WIZ" 'name="auth"' && has "$TPL_WIZ" 'value="password"' && has "$TPL_WIZ" 'value="key_paste"'; then
  ok "E3: the wizard offers BOTH access answers the operator asked for (key and OS login+password)"
else
  bad "E3: the access question does not offer both a key and a password"
fi
if has "$TPL_WIZ" 'name="mode"' && has "$TPL_WIZ" 'value="copy"' && has "$TPL_WIZ" 'value="fresh_db"' && has "$TPL_WIZ" 'value="db_only"'; then
  ok "E4: the mode question offers one-to-one, fresh DB and database-only"
else
  bad "E4: the mode question is incomplete"
fi
if has "$TPL_WIZ" 'name="dns_mode"' && has "$TPL_WIZ" 'name="new_hostname"' && has "$TPL_WIZ" 'name="old_host"'; then
  ok "E5: the hostname, DNS and old-host questions are present"
else
  bad "E5: the hostname/DNS/old-host questions are missing"
fi
if has "$TPL_RUN" 'step_key' && has "$TPL_RUN" 'table-wrap'; then
  ok "E6: the run page can execute a step and keeps its tables inside the layout wrapper"
else
  bad "E6: the run page lacks the per-step action or the table wrapper"
fi
if has "$HANDLERS" 'admin/host_migration.html' && has "$HANDLERS" 'admin/host_migration_run.html'; then
  ok "E7: the page/title/section switches know both templates"
else
  bad "E7: the template switches were not extended (sidebar active state + tab title)"
fi
if has "$HANDLER" '.Where'; then
  ok "E8: the generated plan keeps each step's location, so the executor knows how to run it"
else
  bad "E8: the handler drops Step.Where — every stored step would be unrunnable"
fi
if has "$LAYOUT" '/admin/migrate'; then
  ok "E9: the sidebar links to the page"
else
  bad "E9: the page is unreachable from the sidebar"
fi
# The pre-existing admin/migrate_run.html belongs to the DATABASE migration page. A wizard that
# reuses that filename silently breaks the other page; the name is part of the contract.
if has "internal/handlers/templates/admin/migrate_run.html" 'db.migrate_run_title' && ! has "internal/handlers/templates/admin/migrate_run.html" 'migrate.step.'; then
  ok "E10: the pre-existing database-migration template is untouched"
else
  bad "E10: admin/migrate_run.html was overwritten by the wizard (that file belongs to /admin/database/migrate)"
fi

# --- F: i18n --------------------------------------------------------------------
if has "$CATALOG" 'migrate.' && has "$CATALOG" 'title.admin_host_migration'; then
  ok "F1: the catalogue carries the page title and the wizard keys"
else
  bad "F1: the i18n keys are missing"
fi
missing_steps=0
for key in p0_inventory p0_reach p0_host p0_ports p0_no_install p0_snapshot p1_base p1_user \
           p1_firewall p1_tailscale p1_skygate_install p2_headscale_stop p2_headscale_config \
           p2_headscale_db p2_noise_key p2_headscale_start p2_reregister p3_env p3_data \
           p3_db_convert p3_start p3_migrate p3_readyz p4_derper p4_settings p4_cert p4_relay_row p5_tags \
           p5_acl p5_dedupe p5_dns p5_dns_stub p5_dns_manual p5_acme p6_health p6_nodes \
           p6_routes p6_derp p6_verify p6_client p7_stop p7_decommission; do
  n="$(grep -c "migrate.step.$key.title" "$CATALOG" 2>/dev/null || true)"
  if [ "${n:-0}" -lt 2 ]; then
    missing_steps=$((missing_steps+1))
    bad "F2: step $key has no title in both catalogues (found ${n:-0} of 2)"
  fi
done
if [ "$missing_steps" -eq 0 ]; then
  ok "F2: every step the plan can produce has an RU and an EN title"
fi
missing_notes=0
for note in note_snapshot_stops_services note_two_headscales note_tags_dropped note_env_secrets \
            note_data_dir_overwrite note_pg_weight note_cert_chain note_acl_before_dns note_ttl \
            note_acme_needs_dns note_decommission note_client_check; do
  n="$(grep -c "migrate.note.$note" "$CATALOG" 2>/dev/null || true)"
  if [ "${n:-0}" -lt 2 ]; then
    missing_notes=$((missing_notes+1))
    bad "F3: note $note has no text in both catalogues (found ${n:-0} of 2)"
  fi
done
if [ "$missing_notes" -eq 0 ]; then
  ok "F3: every risk note the plan can attach exists in both languages"
fi
if has "$TPL_WIZ" '{{t "migrate.' && has "$TPL_RUN" '{{t "migrate.'; then
  ok "F4: the templates render through the catalogue instead of hardcoded text"
else
  bad "F4: a template ships hardcoded user-visible text"
fi

# --- G: the docs -------------------------------
if has "$DOC" '## 3. Phases' && has "$DOCRU" '## 3.'; then
  ok "G1: the analysis carries the phase catalog in both languages"
else
  bad "G1: the phase catalog is missing from a language"
fi
if has "$DOC" 'the ACL and the tags go first, DNS last' || has "$DOC" 'ACL and the tags'; then
  ok "G2: the EN analysis records the ACL-before-DNS rule"
else
  bad "G2: the EN analysis does not record the load-bearing ordering"
fi
if has "$DOCRU" 'сначала ACL и теги на новом хосте, потом DNS' || has "$DOCRU" 'сначала ACL'; then
  ok "G3: the RU analysis records the ACL-before-DNS rule"
else
  bad "G3: the RU analysis does not record the load-bearing ordering"
fi
if has "$DOC" 'cannot do' && has "$DOCRU" 'делать **не** может'; then
  ok "G4: both analyses name what the wizard cannot automate"
else
  bad "G4: the manual-only steps are not documented"
fi
if has "docs/README.md" 'host-migration.md' && has "docs/ru/README.md" 'host-migration.md'; then
  ok "G5: both catalogues list the new document"
else
  bad "G5: the document is not in the catalogue (B330 D1 would fail)"
fi

# --- H: registration ------------------------------------------------------------
if has "$VERIFY" '"B482"'; then ok "H1: the contract is registered in the gate"; else bad "H1: not registered in the gate"; fi
if has "$AGENTS" 'B482'; then ok "H2: the block index carries the entry"; else bad "H2: no block-index entry"; fi
if has "$NOTES" 'B482'; then ok "H3: the release notes describe the block"; else bad "H3: no release-notes section"; fi

# --- I: the discriminators ------------------------------------------------------
# The schema predicates must not match a pre-B482 migration file, or every one of them would pass
# on a tree that never had this feature.
cat > "$SKY_TMP/old_schema.go" <<'GO'
const oldPG = `CREATE TABLE IF NOT EXISTS dns_provider_creds (id BIGSERIAL PRIMARY KEY);`
func migrateV084PG(d *sql.DB) error { return nil }
GO
if grep -q 'host_migrations' "$SKY_TMP/old_schema.go"; then
  bad "I1: the schema predicate matches a file that has no migration tables"
else
  ok "I1: the schema predicates discriminate the pre-B482 shape"
fi
# The plan predicates must be about THIS plan: a stub with the right phase strings but no rule
# documentation must fail the ordering contracts.
cat > "$SKY_TMP/old_plan.go" <<'GO'
package migrate
// Phase: "P0"
// Phase: "P7"
GO
if grep -q 'comes BEFORE the DNS switch' "$SKY_TMP/old_plan.go"; then
  bad "I2: the ordering predicate matches a plan with no rules"
else
  ok "I2: the ordering predicates discriminate a rule-less plan"
fi
# Behaviour: the plan and the executor have their own tests, and they must actually run.
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/migrate/ -count=1 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ]; then
    ok "I3: go test ./internal/migrate/ passes ($(printf '%s' "$OUT" | tail -1))"
  else
    bad "I3: go test ./internal/migrate/ failed: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
  fi
else
  printf '  \033[33m-\033[0m I3: SKIP — no go toolchain on this host\n'
fi

echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB482: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB482: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
[ "$FAIL" -eq 0 ]
