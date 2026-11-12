#!/usr/bin/env bash
# ============================================================================
# check_b465_verdict_age.sh — B465
#
# A VERDICT MUST CARRY ITS AGE, AND A COPY MUST NOT ACT ON THE WORLD.
#
# Two screenshots from the same installation on the same day:
#
#   1. /admin/exit-nodes said «Проблему релеев: 2» — and the small print under it said
#      «последняя проверка: 2026-10-30 16:14». Thirteen days earlier. The stored snapshot
#      (`global_settings.exitnode.health`) still held the pre-B449 text:
#      `ssh root@[100.64.0.2:18022]:18022 … Could not resolve hostname [100.64.0.2:18022]`,
#      and the block presented it as the current state of the relays.
#
#   2. The operator's Telegram received
#      `#46 ⚠️ пин на exit-узел устарел … состояние узла: offline`
#      for a relay that was ONLINE at that moment and had never left `online` that day. The
#      `exit_node_health` row behind it was hours old (it came from a restored copy whose
#      tailnet never connects to it) — and the message turned it into a claim about the
#      operator's laptop. #45 had arrived one hour earlier: identical.
#
# Contracts:
#   A. the reconciler refuses to call a pin stale on an expired measurement
#   B. the alert says how old the measurement is, and says «не измерено» when it cannot know
#   C. a standing condition is ONE incident (dedup window by KIND)
#   D. the panel calls an old snapshot history, not the current state
#   E. the ssh target cannot be rendered as `[host:port]:port`
#   F. SKYGATE_READONLY_COPY silences the copy (alerts, bot, every scheduler) —
#      and the log must describe that state truthfully: no promised bot loop, and no
#      scheduler started above the choke point
#   G. /readyz's tailscale probe answers for a NATIVE install too
#   H. the detector discriminates the pre-B465 shape
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b465.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

REC=internal/feature/exit_rules/reconciler.go
ALERT=internal/feature/exit_rules/preferred_exit_alert_b3913.go
HVIEW=internal/feature/admin/exit_nodes_provision.go
HCHECK=internal/feature/admin/exit_nodes_health_b417_1.go
HREC=internal/feature/admin/exit_nodes_reconcile_b417.go
TMPL=internal/handlers/templates/admin/exit_nodes.html
CAT_BOT=internal/i18n/catalog_bot.go
CAT_EX=internal/i18n/catalog_exit_nodes.go
PROV=internal/exitnode/provision_b412.go
SCHED=cmd/skygate/main_schedulers.go
CFG=internal/config/config.go

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }

for f in "$REC" "$ALERT" "$HVIEW" "$HCHECK" "$HREC" "$TMPL" "$CAT_BOT" "$CAT_EX" "$PROV" \
         "$SCHED" "$CFG"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

# B339: a contract must read a SURFACE, never cmd/skygate/main.go as one file — a pure
# code move would turn it red while the product is unchanged. The package surface is the
# operand, expanded inside the helper.
. "$(dirname "$0")/lib/gosurface.sh"
gosurface CMD cmd/skygate/*.go
[ -s "$CMD" ] || { echo "FAIL: the cmd/skygate surface is empty (scripts/lib/gosurface.sh)" >&2; exit 1; }

echo "=== B465: a verdict must carry its age ==="

# --- A: the reconciler's freshness gate --------------------------------------
if has "$REC" 'const relayVerdictFreshFor = 30 * time.Minute'; then
  ok "A1: the relay verdict has an explicit freshness window"
else
  bad "A1: no freshness window — an hours-old row still decides"
fi
if has "$REC" 'CheckedAt time.Time' && has "$REC" 'rv.Fresh = true'; then
  ok "A2: the health verdict carries whether it is fresh"
else
  bad "A2: relayHealth has no freshness fact"
fi
if has "$REC" 'if !s.PrefRelayFresh {' && has "$REC" '"stale-pref-relay-unmeasured"'; then
  ok "A3: an unfresh 'unusable' is reported as UNMEASURED, not as a stale pin"
else
  bad "A3: the planner still calls a pin stale on an expired measurement"
fi
if has "$REC" 'now.Sub(h.LastCheckAt) <= relayVerdictFreshFor'; then
  ok "A4: freshness is computed from the row's own last_check_at"
else
  bad "A4: freshness is not derived from the measurement time"
fi

# --- B: the alert names the age ----------------------------------------------
if has "$ALERT" 'PrefActionUnmeasured = "unmeasured"'; then
  ok "B1: 'not measured' is its own alert kind"
else
  bad "B1: there is no 'not measured' kind — it reuses 'stale'"
fi
if has "$ALERT" 'alert.preferred_exit.title_unmeasured'; then
  ok "B2: the unmeasured alert has its own title (it does not claim the pin is wrong)"
else
  bad "B2: the unmeasured alert reuses the stale title"
fi
if has "$ALERT" 'alert.preferred_exit.relay_state_aged'; then
  ok "B3: the relay fact renders WITH the age of the measurement"
else
  bad "B3: the relay state is presented as a fact about now"
fi
if has "$REC" 'RelayMeasuredAgo: measuredAgo' && has "$REC" 'humanDuration(time.Since(ch.RelayCheckedAt))'; then
  ok "B4: the reconciler hands the age to the message"
else
  bad "B4: the message gets no measurement time"
fi
for key in alert.preferred_exit.title_unmeasured alert.preferred_exit.action_unmeasured \
           alert.preferred_exit.detail_unmeasured alert.preferred_exit.relay_state_aged; do
  n=$(grep -c "\"$key\":" "$CAT_BOT" || true)
  if [ "${n:-0}" -ge 2 ]; then
    ok "B5: $key is defined in BOTH catalogues (RU + EN)"
  else
    bad "B5: $key has ${n:-0} definition(s), want 2 (RU + EN parity)"
  fi
done

# --- C: conditions do not repeat hourly --------------------------------------
if has "$ALERT" 'preferredExitConditionTTL = 12 * time.Hour'; then
  ok "C1: a standing condition dedups for 12 h, not 1 h (the #45/#46 pair)"
else
  bad "C1: a standing condition still repeats every hour"
fi
if has "$ALERT" 'func preferredExitDedupTTL(action string) time.Duration'; then
  ok "C2: the dedup window is chosen by KIND of message"
else
  bad "C2: one window for every message"
fi

# --- D: the panel calls an old snapshot history -------------------------------
if has "$HVIEW" 'HealthStale  bool' && has "$HVIEW" 'HealthAgeSec int64'; then
  ok "D1: the page knows how old the snapshot is"
else
  bad "D1: the page has no idea the snapshot may be ancient"
fi
if has "$HVIEW" 'v.HealthStale = time.Since(st.At) > 2*exitNodeHealthInterval'; then
  ok "D2: staleness is two missed background passes, not a second invented number"
else
  bad "D2: staleness is not derived from the monitor's own interval"
fi
if has "$TMPL" 'exit_nodes.health.stale_title' && has "$TMPL" 'exit_nodes.health.broken_stale'; then
  ok "D3: the template says the list is HISTORY and re-labels the count"
else
  bad "D3: the template still presents an old snapshot as the current state"
fi
for key in exit_nodes.health.stale_title exit_nodes.health.stale_help exit_nodes.health.broken_stale; do
  n=$(grep -c "\"$key\":" "$CAT_EX" || true)
  if [ "${n:-0}" -ge 2 ]; then
    ok "D4: $key is defined in BOTH catalogues"
  else
    bad "D4: $key has ${n:-0} definition(s), want 2"
  fi
done
if has "$HCHECK" 'exit-node-health: pass produced no rows'; then
  ok "D5: the silent path (no rows) is logged instead of returning quietly"
else
  bad "D5: a pass that measured nothing is still invisible"
fi
if has "$HREC" 'go func() {' && [ "$(grep -c 'runHealthCheckIfDue(ctx)' "$HREC")" -ge 1 ]; then
  ok "D6: the relay health pass has its own goroutine/ticker"
else
  bad "D6: the health pass still rides behind the base-route pass"
fi
if [ "$(grep -c 's.runHealthCheckIfDue(ctx)' "$HREC")" -ge 1 ] \
   && ! grep -A3 's.RunBaseRouteReconcile()' "$HREC" | grep -q 'runHealthCheckIfDue'; then
  ok "D7: the health pass is not called from the base-route branch (a hang there cannot starve it)"
else
  bad "D7: the health pass is still chained after RunBaseRouteReconcile"
fi

# --- E: the ssh target cannot be doubled -------------------------------------
if grep -A14 'func (r Request) SSHTarget() string' "$PROV" | grep -q 'ParseSSHTarget(host)'; then
  ok "E1: SSHTarget splits a host that already carries a port"
else
  bad "E1: SSHTarget still brackets host:port and appends the port again"
fi
if [ -f internal/exitnode/target_b465_test.go ] \
   && grep -q 'root@100.64.0.2:18022' internal/exitnode/target_b465_test.go \
   && grep -q 'bugShape' internal/exitnode/target_b465_test.go; then
  ok "E2: the live shape is pinned by a test, with a detector for the malformation"
else
  bad "E2: nothing pins the target shape"
fi

# --- F: the read-only copy ----------------------------------------------------
if has "$CFG" 'func ReadOnlyCopy() bool' && has "$CFG" 'SKYGATE_READONLY_COPY'; then
  ok "F1: SKYGATE_READONLY_COPY exists as a first-class flag"
else
  bad "F1: there is no way to say 'this process is a replica'"
fi
if has "$CMD" 'installNotifierUnlessReadOnly' && has "$CMD" 'Telegram notifier NOT installed'; then
  ok "F2: a read-only copy never installs the notifier (no alerts leave it)"
else
  bad "F2: a copy still gets a live notifier"
fi
if has "$SCHED" 'if config.ReadOnlyCopy() {' && has "$SCHED" 'every scheduler wired HERE is SKIPPED'; then
  ok "F3: a read-only copy starts nothing in the scheduler-wiring function, and the line says only that"
else
  bad "F3: a copy still runs the passes wired there, or the line over-claims again"
fi
# F2b (measured on the live restored copy, 2026-11-12 21:09): the startup message promised
# "starting getUpdates loop" while the guard below it skipped rn.Run, so the log said the fix
# was not running while it was. A read-only branch must come FIRST and name the truth.
if has "$CMD" 'READ-ONLY COPY: Telegram bot loop NOT started'; then
  ok "F2b: a read-only copy says the bot loop is NOT started instead of promising it"
else
  bad "F2b: the startup log still promises a bot loop a read-only copy never starts"
fi
# F3b (same measurement, eleven milliseconds later): «every background scheduler is SKIPPED»
# was printed and then `autoupdater: starting (interval=5m0s)` and
# `preferred-reconciler: starting (interval=1h0m0s, live=true)` followed, because main() started
# both ABOVE wireOptionalSchedulers — whose early return was therefore not the choke point the
# block claimed. They now live in wireEarlySchedulers, whose FIRST statement is the same gate.
EARLY_BODY="$(awk '/^func wireEarlySchedulers\(/,/^}/' "$SCHED")"
_e_guard="$(printf '%s\n' "$EARLY_BODY" | grep -n 'config.ReadOnlyCopy()' | head -1 | cut -d: -f1)"
_e_dns="$(printf '%s\n' "$EARLY_BODY" | grep -n 'RunDomainAutoUpdater(' | head -1 | cut -d: -f1)"
_e_pref="$(printf '%s\n' "$EARLY_BODY" | grep -n 'RunPreferredExitReconciler(' | head -1 | cut -d: -f1)"
if [ -n "$_e_guard" ] && [ -n "$_e_dns" ] && [ -n "$_e_pref" ] \
   && [ "$_e_guard" -lt "$_e_dns" ] && [ "$_e_guard" -lt "$_e_pref" ]; then
  ok "F3b: the two loops that start BEFORE the choke point sit inside the same read-only gate"
else
  bad "F3b: a copy still starts the DNS autoupdater / the preferred-exit reconciler"
fi
# F4 (the same live measurement, one step further): a LIST OF CALL SITES IS NOT A CHOKE POINT.
# After F3b was in place the copy still started, on both sides of the guarded functions,
# `reconcile: cron enabled` (writes portal_users↔headscale links), `update-scheduler: ARMED`
# (follows the COPIED database's schedule and could update this replica) and
# `backup-scheduler: started` (dumps the copy's DB into the operator's bucket). The decision
# therefore moved onto the CONFIGURATION, which every start site already consults:
# Config.ApplyReadOnlyCopy() clears every writer switch, main() calls it right after
# config.Load(), and the two DB-driven schedulers (update, backup) have their own guards
# because they do not consult a cfg switch to decide whether to ACT.
if [ "$(grep -c 'func (c \*Config) ApplyReadOnlyCopy' "$CFG")" -ge 1 ] \
   && [ "$(grep -c 'cfg.ApplyReadOnlyCopy()' "$CMD")" -ge 1 ]; then
  ok "F4: one early decision on the CONFIGURATION covers the start sites scattered across main()"
else
  bad "F4: the read-only gate still depends on remembering every call site"
fi
_f4_switch=0
for _w in DNSAutoUpdateEnabled PrefReconcileInterval NodeDiscoveryInterval ExitNodeCheckInterval \
          HeadscalePollInterval ReconcileHeadscaleUsers ExpireWatchEnabled UpdateScheduleEnabled \
          BackupVerifyInAppEnabled CleanupSmokeMeshInAppEnabled CertSyncEnabled \
          TokenAutoRotateEnabled KeyNotifyEnabled HAEnabled; do
  grep -q "c\.$_w" "$CFG" || _f4_switch=1
done
if [ "$_f4_switch" -eq 0 ]; then
  ok "F4b: every writer switch that gates a background pass is named in the disabled set"
else
  bad "F4b: a writer switch is missing from ApplyReadOnlyCopy"
fi
if has internal/config/readonly_b465_test.go 'func TestB465_ApplyReadOnlyCopyDisablesEveryWriter' \
   && has internal/config/readonly_b465_test.go 'func TestB465_ApplyReadOnlyCopyIsANoOpOnAnActingHost'; then
  ok "F4c: the decision is pinned by tests in both directions (copy = all off, acting host = untouched)"
else
  bad "F4c: nothing pins ApplyReadOnlyCopy"
fi
if has "$CMD" 'update-scheduler NOT armed' && has "$CMD" 'backup-scheduler NOT started'; then
  ok "F4d: the two schedulers that decide from the DATABASE have their own guard"
else
  bad "F4d: a database-driven scheduler still fires on a copy"
fi

# --- G: the tailscale probe speaks for native installs too --------------------
if has "$CMD" 'tailscale is not installed on this host (skipped)'; then
  ok "G1: an absent tailscale is reported as skipped, not as a broken dependency"
else
  bad "G1: an install without tailscale still looks broken"
fi
if ! has "$CMD" 'return false, "tailscaled not running in container'; then
  ok "G2: the container-only guard is gone"
else
  bad "G2: the container-only guard still fails every native install"
fi

# --- H: the detector discriminates the pre-B465 shape ------------------------
cat > "$SKY_TMP/old_reconciler.go" <<'EOF'
	if s.PrefRelayKnown && !s.PrefRelayUsable {
		if s.ExistingPrefSetByUserID != 0 {
			return stalePrefSkip(s, "stale-pref-human-pinned", "")
		}
EOF
cat > "$SKY_TMP/old_alert.go" <<'EOF'
		RelayState: ch.RelayState, Rules: int64(ch.RuleCount),
const preferredExitDedupTTL = time.Hour
EOF
cat > "$SKY_TMP/old_prov.go" <<'EOF'
func (r Request) SSHTarget() string {
	host := strings.TrimSpace(r.Host)
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
EOF
discriminates=1
for _pair in "$REC:relayVerdictFreshFor" "$REC:stale-pref-relay-unmeasured" \
             "$ALERT:PrefActionUnmeasured" "$ALERT:preferredExitConditionTTL" \
             "$HVIEW:HealthStale" "$PROV:ParseSSHTarget(host)" \
             "$CFG:ReadOnlyCopy" "$SCHED:every background scheduler is SKIPPED"; do
  _file="${_pair%%:*}"; _marker="${_pair#*:}"
  has "$_file" "$_marker" || discriminates=0
done
for _marker in 'relayVerdictFreshFor' 'stale-pref-relay-unmeasured' 'PrefActionUnmeasured' \
               'HealthStale' 'ParseSSHTarget(host)' 'ReadOnlyCopy'; do
  has "$SKY_TMP/old_reconciler.go" "$_marker" && discriminates=0
  has "$SKY_TMP/old_alert.go" "$_marker" && discriminates=0
  has "$SKY_TMP/old_prov.go" "$_marker" && discriminates=0
done
if [ "$discriminates" -eq 1 ]; then
  ok "H1: every predicate is present in the fixed files and absent from the pre-B465 shape"
else
  bad "H1: the predicates do not discriminate the pre-B465 shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB465: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB465: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
