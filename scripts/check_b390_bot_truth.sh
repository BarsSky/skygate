#!/usr/bin/env bash
# check_b390_bot_truth.sh — B390 (2026-10-25, TD-33)
#
# THE BOT MUST NOT STATE A VALUE IT DOES NOT HAVE.
#
# The audit in TD-33 found six Telegram surfaces that answered with a value nobody
# had measured. This contract pins the fixes in the SOURCE (so a rule cannot be
# quietly deleted) and then runs the BEHAVIOURAL tests that fail when a value is
# fabricated again — the roadmap's own requirement: "each item needs a test that
# fails when the value becomes fabricated again, not a grep that pins the current
# call".
#
#   H1  /exit_nodes + /myexitnodes labelled EVERY relay "offline": they read
#       `SELECT … FROM devices`, and that table has no writer anywhere in the
#       repository (its only INSERT was a fixture removed in v1.3.0). The status now
#       comes from `exit_node_health` — the monitor snapshot /admin/exit-nodes
#       renders — and a relay the snapshot does not describe says "no data".
#   H2  /my_status printed "devices: 0" when the read FAILED (the error was
#       discarded in both the legacy and the rich path).
#   H3  /version printed a hand-maintained constant `dbSchemaVersion = "v0.32"`
#       against a live v0.81 schema, and /help recommended it as the post-deploy
#       check. The level now comes from `applied_migrations`.
#   H4  /mysubnet share|revoke answered «ACL пере-применён» while a failed re-apply
#       only existed in the log — and a nil headscale client (read-only deploy,
#       corrupt per-plane key) PANICKED inside acl.ApplyGeneratedPolicy.
#   H5  /admin/telegram "Send test" always reported «отправлено»: SendTelegram
#       returns nothing and the handler counted its own loop iterations.
#   H6  /delrule + /clearrules counted iterations, not deletions.
#   +   /help <command> was a 150-line switch of hardcoded ENGLISH while 23
#       `bot.help_detail.*` translations sat unused in both catalogues.
#
# CONTRACTS (27)
#   A  H1: nothing in the telegram surface reads the writerless `devices` table; both
#      replies take the state from the snapshot and answer "no data" when unmeasured
#   B  H2 + H3: no fabricated count, no hardcoded schema constant
#   C  H4: the subnet replies report a failed ACL, and the pipeline refuses a nil client
#   D  H5: the notifier has error-reporting sends, the no-op refuses, "Send test" counts
#      deliveries
#   E  H6: deletions are counted from rows affected
#   F  the help reference comes from the catalogue, in the caller's language
#   G  the behavioural Go tests pass (both packages)
#   H  registered in the catalog, indexed in AGENTS.md, tracked by git
#
# READS A SURFACE, NOT A FILE (B339). `internal/telegram/commands_user.go` was split
# into nine files by refactor Phase D, and a contract that greps one path turns a pure
# code MOVE into a false FAIL — so the source assertions below read the whole
# `internal/telegram` package surface through scripts/lib/gosurface.sh and locate a
# reply by its FUNCTION NAME (awk on `func <name>(`), which survives any file move.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
# shellcheck source=lib/gosurface.sh
. scripts/lib/gosurface.sh

PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b390.XXXXXX")" || exit 1
trap 'rm -rf "$SKY_TMP"' EXIT

HEALTH=internal/telegram/exit_node_truth_b390.go
DELHELPER=internal/telegram/rule_delete_truth_b390.go
NOTIFY=internal/telegram/notify.go
ADMIN_ACTIONS=internal/feature/admin/telegram_actions.go
ACL_APPLY=internal/acl/acl_apply.go
DB_RULES=internal/db/device_rules.go
DB_TRACK=internal/db/migration_tracking.go
CATALOG=internal/i18n/catalog_bot.go
GATE=scripts/verify_pre_deploy.sh

for f in "$HEALTH" "$DELHELPER" "$NOTIFY" "$ADMIN_ACTIONS" "$ACL_APPLY" \
         "$DB_RULES" "$DB_TRACK" "$CATALOG" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

# stripGoComments removes // and /* */ so an assertion never matches the banner that
# explains it (the B418 lesson: a check must look at the code, not at the sentence
# about the code).
strip_go_comments() {
  awk 'BEGIN{d=0}
       { s=$0; out=""
         while (1) {
           if (d) { p=index(s,"*/"); if (p==0) { s=""; break } s=substr(s,p+2); d=0 }
           else   { p=index(s,"/*"); if (p==0) { out=out s; break }
                    out=out substr(s,1,p-1); s=substr(s,p+2); d=1 }
         }
         sub(/\/\/.*$/, "", out)
         print out }' "$1"
}

# fn_body writes ONE function body (by name) from the surface to $2. A reply that moved
# to another file in the package is still found; a reply that was RENAMED is reported
# as missing rather than silently passing.
fn_body() {
  awk -v name="$1" '
    $0 ~ ("^func " name "\\(") { f=1 }
    f { print }
    f && /^}/ { exit }' "$TG" > "$2"
}

gosurface TG_SURFACE internal/telegram/*.go
TG="$SKY_TMP/telegram_surface.go"
strip_go_comments "$TG_SURFACE" > "$TG"

# --- A: H1 — the writerless `devices` table --------------------------------
hdr "A. exit-node state comes from the health snapshot, not from \`devices\`"

if grep -q 'FROM devices' "$TG"; then
  bad "A1: the telegram surface still queries the \`devices\` table — it has NO WRITER in the repository, so every relay reads as 'offline' (TD-33 H1)"
else
  ok "A1: nothing in the telegram surface reads the writerless \`devices\` table"
fi

fn_body exitNodesReply "$SKY_TMP/fn_admin_exit.go"
fn_body myExitNodesReply "$SKY_TMP/fn_user_exit.go"
if [ -s "$SKY_TMP/fn_admin_exit.go" ] && [ -s "$SKY_TMP/fn_user_exit.go" ]; then
  if grep -qF 'exitNodeHealthIndex(' "$SKY_TMP/fn_admin_exit.go" &&
     grep -qF 'exitNodeHealthIndex(' "$SKY_TMP/fn_user_exit.go"; then
    ok "A2: both replies read the monitor's snapshot (exitNodeHealthIndex)"
  else
    bad "A2: a reply does not use the shared snapshot loader"
  fi
  if grep -qF 'exitNodeStatusFor(' "$SKY_TMP/fn_admin_exit.go" &&
     grep -qF 'exitNodeStatusFor(' "$SKY_TMP/fn_user_exit.go"; then
    ok "A3: both replies map the snapshot state through the shared ladder"
  else
    bad "A3: a reply does not use exitNodeStatusFor — the state could be guessed again"
  fi
  if grep -qF 'bot.exit_nodes.health_unavailable' "$SKY_TMP/fn_admin_exit.go" &&
     grep -qF 'bot.exit_nodes.health_unavailable' "$SKY_TMP/fn_user_exit.go"; then
    ok "A5: an unreadable snapshot is reported as a fact about the READ, not as a per-node status"
  else
    bad "A5: a failed snapshot read is silent — the user cannot tell 'no data' from 'offline'"
  fi
else
  bad "A2: could not locate exitNodesReply / myExitNodesReply in the telegram surface"
fi

ladder_missing=0
for k in status_online status_untagged status_degraded status_offline status_no_data; do
  if ! grep -qF "\"bot.exit_nodes.$k\"" "$HEALTH"; then
    bad "A4: exitNodeStatusLabel does not map \`bot.exit_nodes.$k\` — a rung of B273's ladder is missing"
    ladder_missing=1
  fi
done
[ "$ladder_missing" -eq 0 ] && ok "A4: the ladder maps all four monitor states plus 'no data'"

# --- B: H2 + H3 — no fabricated numbers ------------------------------------
hdr "B. no fabricated count, no hardcoded schema level"

fn_body myStatusReply "$SKY_TMP/fn_status.go"
fn_body myStatusBlocks "$SKY_TMP/fn_status_rich.go"
if grep -qF 'deviceCountForUser' "$SKY_TMP/fn_status.go" &&
   grep -qF 'deviceCountForUser' "$SKY_TMP/fn_status_rich.go"; then
  ok "B1: both /my_status paths share deviceCountForUser (one source for the number)"
else
  bad "B1: a /my_status path does not use the shared device counter"
fi
if grep -qF 'bot.my_status.value_unavailable' "$SKY_TMP/fn_status.go" &&
   grep -qF 'bot.my_status.value_unavailable' "$SKY_TMP/fn_status_rich.go"; then
  ok "B2: a failed device count renders the unavailable marker instead of 0"
else
  bad "B2: a failed device count would still print a number"
fi
if grep -q 'dbSchemaVersion' "$TG"; then
  bad "B3: the hardcoded \`dbSchemaVersion\` constant is back — it disagreed with the database for 49 migrations (TD-33 H3)"
else
  ok "B3: the hardcoded schema constant is gone"
fi
fn_body schemaLevelLabel "$SKY_TMP/fn_schema.go"
if grep -qF 'db.LatestAppliedMigrationVersion(' "$SKY_TMP/fn_schema.go"; then
  ok "B4: /version reads the level from applied_migrations"
else
  bad "B4: /version does not read the schema level from the database"
fi

# --- C: H4 — a failed ACL is reported --------------------------------------
hdr "C. a failed ACL re-apply is reported, and a nil client does not panic"

fn_body mySubnetShareReply "$SKY_TMP/fn_share.go"
fn_body mySubnetRevokeReply "$SKY_TMP/fn_revoke.go"
if grep -qF 'bot.mysubnet.share_ok_acl_failed' "$SKY_TMP/fn_share.go" &&
   grep -qF 'bot.mysubnet.revoke_ok_acl_failed' "$SKY_TMP/fn_revoke.go"; then
  ok "C1: /mysubnet share|revoke have a distinct reply for an unapplied ACL"
else
  bad "C1: the subnet replies have no failure wording — they still answer «ACL пере-применён»"
fi
if grep -qF 'AppendAuditLog' "$SKY_TMP/fn_share.go" && grep -qF 'AppendAuditLog' "$SKY_TMP/fn_revoke.go"; then
  ok "C2: a failed re-apply also writes an audit row"
else
  bad "C2: a failed re-apply leaves no audit trail"
fi
strip_go_comments "$ACL_APPLY" > "$SKY_TMP/c_acl.go"
if grep -q 'if hs == nil' "$SKY_TMP/c_acl.go"; then
  ok "C3: ApplyGeneratedPolicy refuses a nil headscale client (it used to PANIC on the nil receiver)"
else
  bad "C3: ApplyGeneratedPolicy dereferences a nil client — a read-only deploy panics in the bot"
fi

# --- D: H5 — "Send test" counts deliveries ---------------------------------
hdr "D. the test button can fail itself"

strip_go_comments "$NOTIFY" > "$SKY_TMP/d_notify.go"
strip_go_comments "$ADMIN_ACTIONS" > "$SKY_TMP/d_actions.go"
if grep -qF 'SendTelegramErr(text string) error' "$SKY_TMP/d_notify.go" &&
   grep -qF 'SendTelegramToChatErr(text string, chatID int64) error' "$SKY_TMP/d_notify.go"; then
  ok "D1: the Notifier interface has error-reporting send methods"
else
  bad "D1: the Notifier still cannot report a delivery failure"
fi
if grep -qF 'ErrTelegramNotConfigured' "$SKY_TMP/d_notify.go"; then
  ok "D2: the no-op notifier refuses with a reason instead of a silent success"
else
  bad "D2: a no-op send reports success"
fi
if grep -qF 'sendTestMessages(' "$SKY_TMP/d_actions.go" && grep -qF 'SendTelegramErr(' "$SKY_TMP/d_actions.go"; then
  ok "D3: 'Send test' counts deliveries from the reported result"
else
  bad "D3: 'Send test' does not use the delivery-reporting send path"
fi
if grep -qF 'НЕ отправлено' "$SKY_TMP/d_actions.go"; then
  ok "D4: the flash has a failure wording (a total failure can no longer read as success)"
else
  bad "D4: no failure wording in the flash — the button cannot fail itself"
fi

# --- E: H6 — deletions counted from rows affected --------------------------
hdr "E. delete replies count deletions"

if grep -qF 'func DeleteRuleForUserRows(' "$DB_RULES"; then
  ok "E1: db.DeleteRuleForUserRows reports rows affected"
else
  bad "E1: no rows-affected delete helper — a caller can only count its own loop"
fi
fn_body deleteRuleReply "$SKY_TMP/fn_delrule.go"
fn_body confirmClearRules "$SKY_TMP/fn_clear.go"
if grep -qF 'deleteRuleJobs(' "$SKY_TMP/fn_delrule.go" &&
   grep -qF 'deleteRuleJobs(' "$SKY_TMP/fn_clear.go"; then
  ok "E2: both /delrule and /clearrules count through the shared helper"
else
  bad "E2: a delete reply still counts loop iterations"
fi
if grep -q 'deleted++' "$SKY_TMP/fn_delrule.go" "$SKY_TMP/fn_clear.go"; then
  bad "E3: an unconditional \`deleted++\` is back — it counts attempts, not deletions"
else
  ok "E3: no unconditional counter remains in the delete paths"
fi
strip_go_comments "$DELHELPER" > "$SKY_TMP/e_helper.go"
if grep -qF 'RowsAffected' "$DB_RULES" && grep -qF 'n <= 0' "$SKY_TMP/e_helper.go"; then
  ok "E4: a DELETE that matched nothing is reported as a failure, not as a deletion"
else
  bad "E4: the helper does not treat a zero-row DELETE as 'nothing happened'"
fi

# --- F: the help reference comes from the catalogue ------------------------
hdr "F. /help <cmd> answers in the caller's language"

if grep -qF 'No detailed help for' "$TG"; then
  bad "F1: the hardcoded English help switch is back"
else
  ok "F1: no hardcoded English help text remains"
fi
fn_body helpDetailReply "$SKY_TMP/fn_help.go"
if grep -qF '"bot.help_detail." + cmd' "$SKY_TMP/fn_help.go"; then
  ok "F2: helpDetailReply looks the reference up in the catalogue"
else
  bad "F2: helpDetailReply does not use bot.help_detail.<command>"
fi
missing=0
for cmd in ack add_device add_rule audit bind clearrules delete_rule delrule exit_nodes \
           exit_nodes_health help lang my_nodes my_quota my_rules my_status myexitnodes \
           nodes quota restart rules status sync_nodes unbind version; do
  n=$(grep -c "\"bot.help_detail.$cmd\":" "$CATALOG")
  [ "$n" -ge 2 ] || { bad "F3: bot.help_detail.$cmd is not defined in BOTH catalogues (found $n)"; missing=1; }
done
[ "$missing" -eq 0 ] && ok "F3: every command the bot can be asked about has a reference in RU and EN"

# --- G: the behavioural tests ---------------------------------------------
hdr "G. the behavioural tests"

if OUT="$(go test ./internal/telegram/ ./internal/feature/admin/ -run B390 -count=1 2>&1)"; then
  ok "G1: the B390 behavioural tests pass (snapshot vs devices, no-data, count, schema, ACL failure, help language)"
else
  bad "G1: go test -run B390 failed: $(printf '%s' "$OUT" | tail -4 | tr '\n' ' ')"
fi

# --- H: registration -------------------------------------------------------
hdr "H. registration"

if grep -q 'run_check "B390"' "$GATE"; then
  ok "H1: B390 is registered in the catalog"
else
  bad "H1: B390 is not registered — this contract would never run"
fi
if grep -q '\*\*B390\*\*' AGENTS.md; then
  ok "H2: AGENTS.md block index carries B390"
else
  bad "H2: AGENTS.md has no B390 entry"
fi
if git ls-files --error-unmatch scripts/check_b390_bot_truth.sh >/dev/null 2>&1; then
  ok "H3: this script is tracked by git (trap 11)"
else
  bad "H3: this script is not tracked by git"
fi

printf '\n\033[1mB390 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
