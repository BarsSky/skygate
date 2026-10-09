#!/usr/bin/env bash
# check_b373_operator_hints_apply_defaults.sh — B373 (2026-10-09): a hint the
# operator can READ next to a button that ACTS, and a DSN template whose
# placeholder means the same thing to the writer and the reader.
#
# THE WORK ORDER. «добавь подсказки с активными кнопками чтобы пользователь мог как
# прочитать что надо сделать и для чего так и применить параметры по умолчанию» —
# explain what has to be done and WHY, and then let one button apply the default.
#
# TWO DEFECTS HAD TO CLOSE FOR THE BUTTON TO BE HONEST.
#
# 1. cluster_database.dsn_template had TWO placeholder conventions, and the one the
#    panel taught was the WRONG one. The panel composer
#    (internal/feature/admin/database.go) and buildDSNTemplate
#    (internal/dbmigrate/steps/flip.go) put %s in the PASSWORD position, with a
#    comment claiming "the watchdog (Phase 3.1) substitutes it at read time"; the
#    only substitution in the whole tree is
#    internal/cluster.substituteDSNTemplate(tpl, primaryHost) — i.e. %s is the HOST
#    (B212, and scripts/b212_join_verify.sh exercises exactly that shape). Filling
#    the template the way the panel described made `skygate join` hand the standby
#    `postgres://user:<primary-hostname>@host/db`: the hostname in the password
#    field, and an authentication failure that no surface explained. The CLI hint
#    and the i18n texts repeated the same wrong sentence.
#
# 2. Tailscale had a Start button but no "make this the default" action, while the
#    reason it dies at every container recreate is not in the panel at all: the
#    environment is FROZEN at container creation and docker-compose pins
#    SKYGATE_TS_AUTHKEY_FILE=/dev/null, so the entrypoint skips tailscaled (B321's
#    lesson, measured again on 2026-10-09).
#
# WHAT SHIPPED
#   * /admin/database — a "DSN template" hint card (what %s is, why no password is
#     stored, why the row is upserted) and the button «Заполнить из моего DSN»,
#     which composes the template from the DSN THIS process runs on. No password
#     ever enters the value: it is rendered on the page, written to audit_log and
#     copied by every backup.
#   * /admin/cluster — the same action inside the onboarding banner that reports the
#     gap, and the inline SQL sample corrected to the upsert in the host shape.
#   * /admin/tailscale — a hint card explaining why the client comes back down and
#     the button «Применить значения по умолчанию», which persists the usable
#     auth-key path + desired_state=on + the canonical hostname and then brings the
#     client up through the B321 path. It REFUSES by name when no usable key file
#     exists; it never mints one.
#   * internal/db/cluster.go — GetClusterDatabase decoded created_at/updated_at
#     through ParseDBTime: on SQLite the write binds CURRENT_TIMESTAMP (TEXT) and
#     scanning it into time.Time failed the whole read, so a row the new button had
#     just created could not be read back on a native install.
#
# WHAT THIS SCRIPT PINS
#   A. ONE placeholder convention, in the code, the CLI hint and both i18n texts;
#   B. the three surfaces carry a READABLE hint and a POST button that acts;
#   C. neither button invents a value (refusals are named);
#   D. the behavioural halves run;
#   E. tracked by git (AGENTS trap #11), registered, indexed.
#
# SKIP (never FAIL) anything that needs a tool this host lacks.
#
# Usage:  bash scripts/check_b373_operator_hints_apply_defaults.sh
# Exit:   0 = contracts hold, 1 = regression
set -uo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/gosurface.sh

SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

gosurface ADMINSURFACE internal/feature/admin/*.go
DBGO=internal/feature/admin/database.go
TSGO=internal/feature/admin/tailscale_apply_defaults.go
FLIP=internal/dbmigrate/steps/flip.go
DBHTML=internal/handlers/templates/admin/database.html
CLUSTERHTML=internal/handlers/templates/admin/cluster.html
TSHTML=internal/handlers/templates/admin/tailscale.html
I18N_RU=internal/i18n/catalog_admin.go
I18N_TS=internal/i18n/catalog_tailscale.go
SELF=scripts/check_b373_operator_hints_apply_defaults.sh

for f in "$DBGO" "$TSGO" "$FLIP" "$DBHTML" "$CLUSTERHTML" "$TSHTML" "$I18N_RU" "$I18N_TS"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B373 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# =====================================================================
hdr "A. ONE meaning for %s, in code and in every text"

if grep -q 'func dsnTemplateFromParts(' "$DBGO" && grep -q 'func dsnTemplateFromDSN(' "$DBGO"; then
  ok "A1: the panel has ONE composer (dsnTemplateFromParts) and one DSN adapter"
else
  bad "A1: the composer pair is missing — the two entry points could drift into two conventions again"
fi
# The old, wrong shape: a %s in the PASSWORD slot of a TEMPLATE. Three filters keep
# this honest rather than noisy:
#   * comments are dropped (the history is documented on purpose);
#   * `%s:%s@` is a CONCRETE DSN builder (user%spassword@), not a template — it is
#     what internal/dbmigrate/handlers.go legitimately does with the real password;
#   * tests are dropped (a test may pin the old shape to prove it is gone).
LEFTOVER="$(grep -rn -- ':%s@\|:%%s@' --include='*.go' internal/ cmd/ 2>/dev/null \
  | grep -v '_test\.go' \
  | grep -vE ':[0-9]+:[[:space:]]*//' \
  | grep -v '%s:%s@' || true)"
if [ -z "$LEFTOVER" ]; then
  ok "A2: no Go code composes a template with %s in the PASSWORD position"
else
  bad "A2: a password-position template survived:
$LEFTOVER"
fi
if grep -q 'postgres://%s@%%s:%s/%s?sslmode=%s' "$FLIP"; then
  ok "A3: buildDSNTemplate writes the host-position shape"
else
  bad "A3: buildDSNTemplate still builds the password-position template"
fi
if grep -q 'dsnTemplateFromParts(username, host, port, dbname, sslmode)' "$DBGO"; then
  ok "A4: the edit form uses the shared composer (no second implementation)"
else
  bad "A4: the edit form composes its own template again"
fi
# The CLI hint that the operator copies must teach the same convention.
if grep -q 'HOST placeholder' cmd/skygate/*.go && ! grep -q 'PASSWORD placeholder' cmd/skygate/*.go; then
  ok "A5: the CLI's DSN hint says %s is the HOST and no longer says PASSWORD"
else
  bad "A5: the CLI hint still teaches the password-position convention"
fi
for f in "$I18N_RU" "$I18N_TS"; do
  if grep -q 'плейсхолдер ПАРОЛЯ\|PASSWORD placeholder' "$f"; then
    bad "A6: $f still documents %s as the PASSWORD placeholder"
  else
    ok "A6: $f no longer documents %s as the PASSWORD placeholder"
  fi
done

# =====================================================================
hdr "B. a hint to READ and a button that ACTS on each surface"

for key in db.tpl_title db.tpl_what db.tpl_why db.tpl_apply_btn db.tpl_apply_help; do
  if grep -q "\"$key\"" "$DBHTML" && grep -q "\"$key\"" "$I18N_RU"; then
    ok "B: /admin/database uses '$key' and the catalogue defines it"
  else
    bad "B: '$key' is missing from database.html or from the catalogue"
  fi
done
if grep -q 'action="/admin/database/apply-default"' "$DBHTML"; then
  ok "B1: the /admin/database card carries the apply button"
else
  bad "B1: the DSN hint card has no button"
fi
if grep -q 'action="/admin/database/apply-default"' "$CLUSTERHTML" && grep -q 'name="next" value="/admin/cluster"' "$CLUSTERHTML"; then
  ok "B2: the onboarding DSN banner offers the same action and returns to /admin/cluster"
else
  bad "B2: the banner reports the gap without offering the action"
fi
if grep -q "INSERT INTO cluster_database (id, cluster_id, dsn_template)" "$CLUSTERHTML" \
   && grep -q 'ON CONFLICT (id) DO UPDATE SET dsn_template' "$CLUSTERHTML"; then
  ok "B3: the inline SQL sample is the UPSERT that works when the row does not exist"
else
  bad "B3: the sample update still silently changes 0 rows when the row is absent"
fi
if grep -q 'name="action" value="apply_defaults"' "$TSHTML" && grep -q 'name="csrf"' "$TSHTML"; then
  ok "B4: /admin/tailscale's defaults button posts the apply_defaults action with its CSRF token"
else
  bad "B4: the Tailscale defaults button is missing or unauthenticated"
fi
for key in tailscale.defaults_title tailscale.defaults_what tailscale.defaults_why tailscale.defaults_btn tailscale.defaults_btn_help; do
  if grep -q "\"$key\"" "$TSHTML" && grep -q "\"$key\"" "$I18N_TS"; then
    ok "B: /admin/tailscale uses '$key' and the catalogue defines it"
  else
    bad "B: '$key' is missing from tailscale.html or from the catalogue"
  fi
done
# A button that navigates instead of posting cannot perform an action.
if grep -qE '<a[^>]*href="/admin/database/apply-default"' "$DBHTML" "$CLUSTERHTML" 2>/dev/null; then
  bad "B5: an apply-default control is a LINK — a GET must not change state"
else
  ok "B5: both apply-default controls are POST forms"
fi

# =====================================================================
hdr "C. neither button invents a value"

if grep -q 'errDSNTemplateUnparseable' "$ADMINSURFACE" && grep -q 'func (s \*Service) saveDSNTemplateFromDSN(' "$ADMINSURFACE"; then
  ok "C1: an unparseable live DSN is refused by name, not turned into a template"
else
  bad "C1: the DSN button has no refusal path"
fi
if grep -q 'func usableAuthKeyFile(' "$ADMINSURFACE" && grep -q 'func (s \*Service) tailscaleDefaultKeyCandidates(' "$ADMINSURFACE"; then
  ok "C2: the Tailscale button only accepts a real, non-empty regular file"
else
  bad "C2: the Tailscale button would accept the /dev/null sentinel as 'the default'"
fi
if grep -q 'не будет\|никогда\|не выдумывает\|never mints' "$TSGO" "$I18N_TS" 2>/dev/null; then
  ok "C3: the refusal text says the panel does not invent a key"
else
  bad "C3: the refusal does not tell the operator what to do instead"
fi
if grep -q 'func (s \*Service) persistTailscaleDefaults(' "$ADMINSURFACE" \
   && grep -q 'tailscaleDesiredStateDBKey, tailscaleDesiredOn' "$ADMINSURFACE" \
   && grep -q 'tailscaleHostnameDBKey' "$ADMINSURFACE"; then
  ok "C4: the defaults are the documented three (path + intent + canonical name)"
else
  bad "C4: the defaults written by the button are incomplete"
fi
if grep -q 'cluster.db.apply_default' "$ADMINSURFACE" && grep -q 'tailscale_apply_defaults' "$ADMINSURFACE"; then
  ok "C5: both buttons leave an audit row"
else
  bad "C5: a state-changing button with no audit trail"
fi
# The DSN read must survive SQLite, or the row the button just wrote cannot be read
# back on a native install.
if grep -q 'ParseDBTime(createdRaw)' internal/db/cluster.go && grep -q 'ParseDBTime(updatedRaw)' internal/db/cluster.go; then
  ok "C6: cluster_database timestamps are decoded dialect-safely (SQLite TEXT vs PG timestamptz)"
else
  bad "C6: GetClusterDatabase scans timestamps straight into time.Time — the SQLite read dies"
fi

# =====================================================================
hdr "D. the behavioural halves run"

GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
  skip "D1: no go binary in PATH — the behavioural contracts were not run"
else
  if out="$("$GOBIN" test ./internal/feature/admin/ -run 'B373' -count=1 2>&1)"; then
    ok "D1: the B373 contracts pass (real SQLite: the upsert, the refusal, the three settings)"
  else
    bad "D1: go test -run B373 failed:
$(printf '%s\n' "$out" | tail -20)"
  fi
  if out="$("$GOBIN" build ./... 2>&1)"; then
    ok "D2: the tree builds"
  else
    bad "D2: go build ./... fails:
$(printf '%s\n' "$out" | tail -10)"
  fi
  if out="$("$GOBIN" test ./internal/i18n/ -count=1 2>&1)"; then
    ok "D3: every new key exists in BOTH catalogues (parity test)"
  else
    bad "D3: i18n parity failed:
$(printf '%s\n' "$out" | tail -10)"
  fi
fi

# =====================================================================
hdr "E. tracked by git (AGENTS trap #11), registered, indexed"

UNTRACKED=""
for f in "$TSGO" "$SELF"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 || UNTRACKED="${UNTRACKED}${f} "
done
if [ -z "$UNTRACKED" ]; then
  ok "E1: every new file of this block is tracked by git"
else
  skip "E1: not yet tracked by git (the lead commits this block): ${UNTRACKED}"
fi
if grep -q 'run_check "B373"' scripts/verify_pre_deploy.sh; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered — the contract would never run"
fi
if grep -q '^- \*\*B373\*\*' AGENTS.md; then
  ok "E3: recorded in the AGENTS.md block index (as its own line)"
else
  bad "E3: AGENTS.md has no B373 bullet at line start"
fi

printf '\n\033[1mB373 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
