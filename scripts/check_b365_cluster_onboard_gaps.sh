#!/usr/bin/env bash
# check_b365_cluster_onboard_gaps.sh — B365 (2026-10-08): the last three gaps in
# the panel-only cluster onboarding.
#
# THE MEASUREMENT
# ---------------
# A real second host was onboarded entirely from the admin panel: the panel
# minted the invite + an `infra`-user preauth key, the host joined the tailnet,
# ran `skygate join`, and the row was Approved. Three things were still wrong,
# and all three were found by RUNNING the block, not by reading it:
#
#   gap 1  step 4 rendered
#            sudo systemctl enable --now skygate && systemctl is-active skygate
#            && curl -fsS http://127.0.0.1:8080/healthz
#          A bare host has NO skygate.service — the standby is a single binary
#          with no unit — so the step could not work as written, and the operator
#          was told to run something that does not exist. What has to run is the
#          heartbeat daemon (which `skygate join` itself prints, pointing at a
#          `deploy/heartbeat-daemon.service` THIS REPO DOES NOT CONTAIN). The
#          operator started it by hand with `setsid`.
#
#   gap 2  `skygate join` reported, live:
#            cluster join: no DSN bootstrap from primary
#            (cluster_database.dsn_template is empty); use the standby's own
#            .env SKYGATE_DB_DSN
#          — the symptom, never the one action that fixes it. The standby became
#          a cluster member with no DSN, i.e. it can never serve as a mirror
#          without a hand edit, which is exactly the promise this flow exists to
#          keep.
#
#   gap 3  after the join AND after Approve the row read
#            id=node-disc-svyatoslava  hostname=svyatoslava  state=ready
#            skygate_version=(discovered via Tailscale)
#          The B223 discovery pass created that row with its placeholder version
#          and `ON CONFLICT (id) DO NOTHING`; the join's own upsert carries
#          `ON CONFLICT (id)` too, but `cluster_node` has TWO unique keys — the
#          `id` PRIMARY KEY and `UNIQUE (cluster_id, hostname)`
#          (`idx_cluster_node_cluster_hostname`, B211 migration v0.66) — and the
#          row is keyed by hostname, not by an id the invite derives. Measured
#          root cause: the join returned the EXISTING row untouched, so the
#          version never changed, the join wrote no node_join audit row, and the
#          fresh last_seen_at came from the heartbeat daemon.
#
# WHAT THIS SCRIPT PINS (properties, not strings)
#   A. step 4 names the heartbeat unit it enables, writes that unit itself, is
#      idempotent, verifies the DAEMON — and does NOT promise a `skygate` web
#      service (`healthz` / `curl` / a bare `skygate` unit are ABSENT).
#   B. an empty `cluster_database.dsn_template` produces a message that names
#      the exact setting on BOTH surfaces (the join's output and the panel's
#      warning), and the pass-through case is unchanged.
#   C. a real join ADOPTS the row that already exists for its hostname (state
#      pending + the joining build's version), the code targets the unique key
#      that actually fires, and one upsert helper serves both branches.
#   D. both dialects: the adoption path and the join upsert go through the
#      DialectKind helpers, never a PostgreSQL-only literal.
#   E. the behavioural contracts actually RUN: internal/cluster/join_b365_test.go
#      drives the REAL Join on a migrated SQLite DB (the native install's
#      backend), and the admin/CLI unit tests pin the rendered block + message.
#   F. tracked by git (AGENTS trap #11) — reported as SKIP, not FAIL, because the
#      lead commits this block.
#
# SKIP (never FAIL) anything that needs live state, a network, or a tool this
# host does not have (AGENTS rule 1).
#
# Usage:  bash scripts/check_b365_cluster_onboard_gaps.sh
# Exit:   0 = contracts hold (a SKIP is not a failure), 1 = regression
set -uo pipefail
cd "$(dirname "$0")/.."

SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

ONBOARD=internal/feature/admin/cluster_onboard_b342.go
GOTEST=internal/feature/admin/cluster_onboard_b365_test.go
JOIN=internal/cluster/join.go
JOINTEST=internal/cluster/join_b365_test.go
CLI=cmd/skygate/cluster.go
CLITEST=cmd/skygate/join_b365_test.go
TPL=internal/handlers/templates/admin/cluster.html
I18N=internal/i18n/catalog_admin.go
SELF=scripts/check_b365_cluster_onboard_gaps.sh

for f in "$ONBOARD" "$GOTEST" "$JOIN" "$JOINTEST" "$CLI" "$CLITEST" "$TPL" "$I18N"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B365 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# fn_body <file> <func-name-prefix> — print a Go function body. Used so the
# assertions below are made against the MESSAGE a surface emits, not against a
# file that merely contains the word somewhere.
fn_body() {
  awk -v fn="$2" 'index($0, fn) == 1 && /func / {p=1} p {print} p && /^}/ {exit}' "$1"
}

# =====================================================================
hdr "A. the standby step names the heartbeat unit (and no skygate service)"

if grep -q 'Num: 4, TitleKey: "cluster.onboard_step_heartbeat"' "$ONBOARD"; then
  ok "A1: step 4 is the heartbeat step, not a 'service' step on a host with no unit"
else
  bad "A1: step 4 does not use the heartbeat title key — a standby has no skygate unit"
fi
if grep -q 'heartbeatDaemonStepCommand(' "$ONBOARD"; then
  ok "A2: the step's command is built by the shared heartbeatDaemonStepCommand helper (unit name + command in ONE place)"
else
  bad "A2: the heartbeat step's command is not built by heartbeatDaemonStepCommand — the unit name would drift"
fi
# The rendered command is a pure Go function, so the exact text is pinned
# behaviourally in E1; the source contracts here are that the unit name, the
# daemon subcommand and the verification are all present.
if grep -q 'clusterOnboardHeartbeatUnit = "skygate-heartbeat"' "$ONBOARD" \
   && grep -q 'clusterOnboardHeartbeatStateFile = "/etc/skygate/cluster-state.json"' "$ONBOARD"; then
  ok "A3: the heartbeat unit and state file have ONE named source each"
else
  bad "A3: the heartbeat unit / state file name is not a single named constant"
fi
if grep -q 'cluster heartbeat-daemon --state-file=' "$ONBOARD" \
   && grep -q 'systemctl daemon-reload' "$ONBOARD" \
   && grep -q 'systemctl enable --now' "$ONBOARD" \
   && grep -q 'systemctl is-active --quiet' "$ONBOARD" \
   && grep -q 'journalctl -u' "$ONBOARD"; then
  ok "A4: the step renders the unit, reloads, enables --now and verifies the daemon (its state + first heartbeats)"
else
  bad "A4: the heartbeat step no longer renders/reloads/enables/verifies the daemon"
fi
# THE NEGATIVE PROPERTY, checked against the COMMAND the step renders (the body
# of the builder). The fix's own explanatory comment quotes the old command, so a
# whole-file grep would fire on the documentation — measure the emitted text.
STEP_BODY="$(fn_body "$ONBOARD" "func heartbeatDaemonStepCommand(")"
if [ -z "$STEP_BODY" ]; then
  bad "A5: heartbeatDaemonStepCommand() is gone — the step no longer names what it enables"
elif [ -z "$(printf '%s\n' "$STEP_BODY" | grep -E 'healthz|curl |enable --now skygate |is-active skygate')" ]; then
  ok "A5: the rendered step no longer promises a skygate web service (no healthz probe, no bare skygate unit)"
else
  bad "A5: the rendered step still tells the operator to enable/verify a skygate web service a standby does not have"
fi
# The CLI's own next-steps message pointed at deploy/heartbeat-daemon.service,
# which the repo does not contain — and DOES NOT EXIST anywhere in the tree.
if [ -e deploy/heartbeat-daemon.service ]; then
  skip "A6: deploy/heartbeat-daemon.service exists now — the CLI may reference it again"
elif ! sed 's://.*::' "$CLI" | grep -q 'deploy/heartbeat-daemon.service'; then
  ok "A6: nothing in the CLI points at deploy/heartbeat-daemon.service (a file the repo does not contain)"
else
  bad "A6: the join's next-steps message still names a systemd file that does not exist"
fi

# =====================================================================
hdr "B. an empty dsn_template is reported, and names the exact setting"

# B1/B2 read the Message the join actually prints (the helper's body), so a
# mention in a comment cannot satisfy them.
CLI_HINT="$(fn_body "$CLI" "func missingDSNBootstrapHint(")"
if [ -z "$CLI_HINT" ]; then
  bad "B1: missingDSNBootstrapHint() is gone — the join would print nothing about the missing DSN"
elif [ -n "$(printf '%s\n' "$CLI_HINT" | grep -F 'cluster_database.dsn_template')" ]; then
  ok "B1: the join's message names cluster_database.dsn_template"
else
  bad "B1: the join's message does not name cluster_database.dsn_template"
fi
if [ -z "$CLI_HINT" ]; then
  bad "B2: no message to check for the action"
elif [ -n "$(printf '%s\n' "$CLI_HINT" | grep -E '/admin/database|UPDATE cluster_database')" ]; then
  ok "B2: the join's message names the page (or the exact statement) that fills the setting"
else
  bad "B2: the join's message names the symptom but not the action"
fi
if [ -n "$(printf '%s\n' "$CLI_HINT" | grep -F '%s')" ] \
   && [ -n "$(printf '%s\n' "$CLI_HINT" | grep -i 'password')" ]; then
  ok "B3: it says what the template's %s placeholder IS (the password, not the host)"
else
  bad "B3: the message does not say what %s is — an operator would substitute the host and get an unusable DSN"
fi
if grep -q 'if jr.DSN != ""' "$CLI" && grep -q 'missingDSNBootstrapHint()' "$CLI"; then
  ok "B4: the hint is the ELSE branch — a join that DID receive a DSN prints the DSN, not the warning"
else
  bad "B4: the DSN hint is not gated on an empty jr.DSN"
fi
# THE PANEL SIDE. Both i18n keys must exist in RU and EN (the parity test only
# compares key SETS, so presence in one language proves nothing).
RU_HITS="$(grep -c '"cluster.onboard_dsn_missing_title"' "$I18N" || true)"
EN_HITS="$(grep -c '"cluster.onboard_dsn_missing_help"' "$I18N" || true)"
if [ "$RU_HITS" -eq 2 ] && [ "$EN_HITS" -eq 2 ]; then
  ok "B5: cluster.onboard_dsn_missing_title/_help are defined in BOTH catalogues"
else
  bad "B5: the panel's DSN warning keys are not defined twice (RU+EN): title=$RU_HITS help=$EN_HITS"
fi
DSN_WARN_KEYS="$(grep -h '"cluster.onboard_dsn_missing' "$I18N")"
if [ -n "$(printf '%s\n' "$DSN_WARN_KEYS" | grep -F 'dsn_template')" ]; then
  ok "B6: the panel warning names the setting (dsn_template)"
else
  bad "B6: the panel warning does not name cluster_database.dsn_template"
fi
if grep -q 'SELECT COALESCE(dsn_template' "$ONBOARD" && grep -q 'onboardDSNTemplateReady' "$ONBOARD"; then
  ok "B7: the panel reads cluster_database.dsn_template and reports absence (it never invents a DSN)"
else
  bad "B7: the panel does not read dsn_template — the warning cannot be truthful"
fi
if grep -q 'cluster.onboard_dsn_missing_help' "$TPL" && grep -q 'Onboard.DSNReady' "$TPL"; then
  ok "B8: the template renders the warning for that one render"
else
  bad "B8: the template does not render the DSN warning"
fi
# B9/B10 — the recipe must WORK on the deployment it is printed on. Measured live
# on the reference primary (2026-10-08): `cluster_database` had ZERO rows, because
# nothing creates one until /admin/database is opened. The pre-fix recipe was a
# bare `UPDATE ... WHERE id='skygate-staging'`, which changes 0 rows there and
# reports success — a step that cannot succeed is worse than no step (see B365's
# own step-4 defect). The statement must therefore CREATE the row.
if [ -n "$(printf '%s\n' "$CLI_HINT" | grep -F 'INSERT INTO cluster_database')" ] \
   && [ -n "$(printf '%s\n' "$CLI_HINT" | grep -F 'ON CONFLICT (id) DO UPDATE')" ]; then
  ok "B9: the join's SQL CREATES the cluster_database row (upsert), so it works when the row is absent"
else
  bad "B9: the join's SQL is UPDATE-only — on a deployment whose cluster_database table is empty (measured live: 0 rows) it silently changes nothing"
fi
if [ -n "$(printf '%s\n' "$DSN_WARN_KEYS" | grep -F 'ON CONFLICT (id) DO UPDATE')" ]; then
  ok "B10: the panel warning carries the same upsert, so the page and the CLI cannot drift"
else
  bad "B10: the panel warning still prescribes a statement that cannot create the missing row"
fi

# =====================================================================
hdr "C. a join adopts the row that already exists for its hostname"

if grep -q 'adoptNodeOnJoin' "$JOIN" && grep -q "state = 'pending'" "$JOIN"; then
  ok "C1: the existing row is ADOPTED (refreshed in place), and adoption forces state=pending"
else
  bad "C1: an existing row for the hostname is still returned untouched"
fi
if grep -q 'ON CONFLICT (cluster_id, hostname) DO UPDATE' "$JOIN"; then
  ok "C2: the insert targets the unique key the row actually collides on (cluster_id, hostname), not (id)"
else
  bad "C2: the insert still names a conflict target the hostname row cannot collide on"
fi
if grep -q 'markInviteUsed(' "$JOIN" && grep -q 'auditNodeJoin(' "$JOIN" && grep -q 'joinResponse(' "$JOIN"; then
  ok "C3: both join branches share markInviteUsed / auditNodeJoin / joinResponse (they cannot drift)"
else
  bad "C3: one of the shared join helpers is missing — the adopt path would differ from the fresh-insert path"
fi
if grep -q 'RETURNING id' "$JOIN"; then
  ok "C4: the join binds itself to the row that owns (cluster_id, hostname)"
else
  bad "C4: the join does not read back the id that owns the row"
fi
# The schema evidence the fix rests on: the natural key is UNIQUE on BOTH chains.
if grep -q 'UNIQUE (cluster_id, hostname)' internal/db/migrations_v0_66_b211.go \
   && grep -q 'idx_cluster_node_cluster_hostname' internal/db/migrations_sqlite.go; then
  ok "C5: the (cluster_id, hostname) unique key exists on both dialect chains"
else
  bad "C5: the (cluster_id, hostname) unique key is not declared on both chains — the adopt assumption is unproven"
fi

# =====================================================================
hdr "D. both dialects (SQLite is the native install's backend)"

if grep -q 'dialect.CastTextArray' "$JOIN" && grep -q 'db.TextArrayLiteral' "$JOIN"; then
  ok "D1: roles are written through the dialect array helpers, never a PG-only ARRAY[...] literal"
else
  bad "D1: the join writes roles without the dialect helpers — it would be dead on SQLite (the B291 class)"
fi
if grep -q 'dialect.TimeValue' "$JOIN"; then
  ok "D2: timestamps are bound through DialectKind.TimeValue (both backends can read them back)"
else
  bad "D2: timestamps are bound as raw time.Time — SQLite stores an undecodable shape (the B291 class)"
fi
# The negative: no PG-only literal may be EXECUTED (a comment explaining the old
# `EXTRACT(epoch FROM now())` bug is fine — comments are stripped first).
if ! sed 's://.*::' "$JOIN" | grep -qE '::text\[\]|EXTRACT\(epoch'; then
  ok "D3: no PostgreSQL-only SQL literal is executed in the join path"
else
  bad "D3: a PostgreSQL-only literal is back in the join SQL — it would be dead on SQLite"
fi

# =====================================================================
hdr "E. the behavioural contracts actually run"

if command -v go >/dev/null 2>&1; then
  # Capture first, match second: piping a test run into grep -q kills it with
  # SIGPIPE and pipefail turns that into a false FAIL (AGENTS trap #9).
  OUT="$(go test ./internal/cluster/ ./internal/feature/admin/ ./cmd/skygate/ \
           -run 'B365' -count=1 -v 2>&1)"
  printf '%s\n' "$OUT" > "$SKY_TMP/b365-go-test.log"
  if printf '%s\n' "$OUT" | grep -qE '^(FAIL|--- FAIL)'; then
    bad "E1: a B365 Go test FAILED (log: $SKY_TMP/b365-go-test.log)"
    printf '%s\n' "$OUT" | grep -E '^(--- FAIL|FAIL|    )' | head -20 | sed 's/^/        /'
  elif printf '%s\n' "$OUT" | grep -q '^ok'; then
    ok "E1: the B365 Go contracts pass (join adoption on migrated SQLite, the rendered step, the messages)"
  else
    bad "E1: the B365 Go tests did not report ok (log: $SKY_TMP/b365-go-test.log)"
  fi
  # The B342 block's own tests must keep passing — B365 renamed one of its steps.
  OUT2="$(go test ./internal/feature/admin/ -run 'B342' -count=1 2>&1)"
  if printf '%s\n' "$OUT2" | grep -q '^ok'; then
    ok "E2: the B342 onboarding-block contracts still pass after the step rename"
  else
    bad "E2: the B342 block contracts broke: $(printf '%s' "$OUT2" | tail -3)"
  fi
else
  skip "E1: no go binary in PATH — the B365 behavioural contracts were not run"
  skip "E2: no go binary in PATH — the B342 block contracts were not run"
fi

# =====================================================================
hdr "F. tracked by git (AGENTS trap #11)"

if git rev-parse --git-dir >/dev/null 2>&1; then
  UNTRACKED=""
  for f in "$SELF" "$GOTEST" "$JOINTEST" "$CLITEST"; do
    git ls-files --error-unmatch "$f" >/dev/null 2>&1 || UNTRACKED="$UNTRACKED $f"
  done
  if [ -z "$UNTRACKED" ]; then
    ok "F1: every new file of this block is tracked by git"
  else
    # NOT a failure: the lead commits this block. A .gitignore rule eating a
    # check script is the trap being watched for, so it is reported loudly.
    skip "F1: not yet tracked by git (the lead commits this block; verify .gitignore does not eat them):$UNTRACKED"
  fi
else
  skip "F1: not a git work tree — trackedness cannot be checked here"
fi
if grep -q 'check_b365_cluster_onboard_gaps.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "F2: registered in scripts/verify_pre_deploy.sh"
else
  skip "F2: not yet registered in scripts/verify_pre_deploy.sh (the lead registers the check)"
fi

hdr "B365 summary: $PASS passed, $FAIL failed, $SKIP skipped"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
