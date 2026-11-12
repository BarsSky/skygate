#!/usr/bin/env bash
# ============================================================================
# check_b464_readiness_truth.sh — B464
#
# "ready" MUST MEAN "THIS HOST CAN SERVE TRAFFIC".
#
# Measured live on the operator's standby (2026-11-11, skygate v1.5.108):
#
#   cluster_node:  svyatoslava | state=ready | roles={skygate-standby}
#                  skygate_version=v1.5.108+59fbad7 | last_seen_at = 24 s ago
#   on that host:  no skygate web server (nothing on 80/443/8080/8082), no
#                  headscale, no database, no docker, /var/lib/skygate = 16 KB
#   cluster_database: 0 rows  → /etc/skygate/dbs.env says
#                  "# SKYGATE_DB_DSN=<unset — primary returned no DSN …>"
#
# The state was decided by the only signal that existed: "a process on that host
# reached POST /api/cluster/heartbeat". That answers "can the agent reach the hub?"
# and says nothing about serving — so the HA chain offered a non-serving, data-less
# host as a READY standby, and flipping DNS to it would have been an outage.
#
# Contracts:
#   A. the cluster package has the `degraded` state and the verdict, and the verdict
#      is EMPTY for an agent that reports nothing (compatibility)
#   B. the heartbeat applies the verdict, audits it, and `draining` outranks it
#   C. the elector treats `degraded` as alive-and-staleness-checked
#   D. `degraded` cannot be promoted (failover query) and CAN be upgraded
#   E. the agent probes /healthz + /readyz, falls back for an older hub, and the
#      join refuses to call "no database" a success
#   F. /admin/cluster names the state, the reason and the credential's expiry — in
#      BOTH catalogues (RU + EN)
#   G. the detector fails on the pre-B464 shape
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b464.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

RD=internal/cluster/readiness_b464.go
NODE=internal/cluster/node.go
JOIN=internal/cluster/join.go
ELECTOR=internal/elector/elector.go
UPGRADE=internal/cluster/upgrade.go
CANDIDATES=internal/db/cluster_sql_b291.go
DAEMON=cmd/skygate/cluster.go
PROBE=cmd/skygate/heartbeat_readiness_b464.go
HANDLERS=internal/feature/cluster/handlers.go
ADMIN=internal/feature/admin/cluster.go
TMPL=internal/handlers/templates/admin/cluster.html
EN_RU=internal/i18n/catalog_admin.go
H446=internal/feature/admin/backup_stale_cause_b446.go
H446T=internal/feature/admin/backup_stale_cause_b446_test.go
ONBOARD=internal/feature/admin/cluster_onboard_b342.go
B365C=scripts/check_b365_cluster_onboard_gaps.sh

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }

for f in "$RD" "$NODE" "$JOIN" "$ELECTOR" "$UPGRADE" "$CANDIDATES" "$DAEMON" "$PROBE" \
         "$HANDLERS" "$ADMIN" "$TMPL" "$EN_RU" "$H446" "$H446T" "$ONBOARD" "$B365C"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

echo "=== B464: ready means the host can serve ==="

# --- A: the state and the verdict --------------------------------------------
if has "$NODE" 'NodeStateDegraded = "degraded"'; then
  ok "A1: NodeStateDegraded is declared with the other states"
else
  bad "A1: there is no degraded state"
fi
if has "$RD" 'func readinessVerdict(r *Readiness) (state, reason string)'; then
  ok "A2: the verdict is a pure function (testable without a hub)"
else
  bad "A2: no readinessVerdict function"
fi
if has "$RD" 'if r == nil {'; then
  ok "A3: an absent payload yields NO verdict (an old agent is never demoted)"
else
  bad "A3: the nil payload is not handled as 'not measured'"
fi
if has "$RD" 'if !r.Serving' && has "$RD" 'if !r.Ready'; then
  ok "A4: both halves of 'can serve' are checked (web server AND database)"
else
  bad "A4: only one of the two readiness facts is checked"
fi

# --- B: the heartbeat applies it ---------------------------------------------
if has "$JOIN" 'verdictState, verdictReason := readinessVerdict(req.Readiness)'; then
  ok "B1: the heartbeat uses the verdict to decide the state"
else
  bad "B1: the heartbeat ignores the readiness payload"
fi
if has "$JOIN" 'case prevState == NodeStateDraining:'; then
  ok "B2: draining outranks the verdict (the operator's decision)"
else
  bad "B2: a verdict can undo a drain"
fi
if has "$JOIN" 'if state != prevState {' && has "$JOIN" '"readiness":%s'; then
  ok "B3: every transition is audited, with the measured block in the detail"
else
  bad "B3: the transition (or its measured block) is not audited"
fi
if has "$JOIN" 'Degraded:             state == NodeStateDegraded'; then
  ok "B4: the hub tells the agent its own verdict (the host's journal stays silent otherwise)"
else
  bad "B4: the response carries no verdict"
fi
if has "$JOIN" 'THE INVITE TOKEN IS ALSO THE NODE CREDENTIAL'; then
  ok "B5: the deliberate non-enforcement of the invite expiry on heartbeats is documented"
else
  bad "B5: the invite-expiry contract on the heartbeat is undocumented (the next reader will 'fix' it and 401 every standby)"
fi

# --- C: the elector -----------------------------------------------------------
if has "$ELECTOR" 'case "ready", "degraded":'; then
  ok "C1: a degraded node obeys the staleness rule (silence still means failed)"
else
  bad "C1: a silent degraded node would stay degraded forever"
fi

# --- D: promotion vs upgrade --------------------------------------------------
if has "$CANDIDATES" "FROM cluster_node WHERE state = 'ready' ORDER BY id ASC"; then
  ok "D1: failover candidates are state='ready' only, so a degraded node can never be promoted"
else
  bad "D1: the candidate query changed — a degraded node might be promoted"
fi
if has "$UPGRADE" "state IN ('ready', 'failed', 'degraded')"; then
  ok "D2: a degraded node IS upgradeable (an upgrade is the usual fix)"
else
  bad "D2: a degraded node is excluded from the only procedure that repairs it"
fi

# --- E: the agent side --------------------------------------------------------
if has "$PROBE" '"/healthz"' && has "$PROBE" '"/readyz"'; then
  ok "E1: the probe uses skygate's own two endpoints (no second DB configuration path)"
else
  bad "E1: the probe invents its own health question"
fi
if has "$PROBE" 'if strings.HasSuffix(u, "/healthz")'; then
  ok "E2: /readyz is derived from the same URL (one thing to configure)"
else
  bad "E2: the readyz URL is not derived from the healthz URL"
fi
if has "$PROBE" 'func redactProbeDetail' && has "$PROBE" 'dsnUserinfo'; then
  ok "E3: the measured string is redacted before it leaves the host"
else
  bad "E3: a driver message could carry a DSN into another database"
fi
if has "$DAEMON" 'strings.Contains(string(body), "unknown field")' && has "$DAEMON" 'legacyHubSeen'; then
  ok "E4: a NEWER agent talking to an OLDER hub retries once with the legacy body (3 rejected beats = failed)"
else
  bad "E4: a newer agent would be declared failed by an older hub"
fi
if has "$HANDLERS" 'decodeJSONForwardCompatible(w, r, &req)'; then
  ok "E5: the heartbeat endpoint tolerates unknown fields (join stays strict)"
else
  bad "E5: the heartbeat endpoint still rejects unknown fields"
fi
if has "$DAEMON" 'if jr.DSN == "" && !opts.AllowNoDSN {'; then
  ok "E6: a join with no database FAILS instead of exiting 0 (the live silent failure)"
else
  bad "E6: a join with no database still exits 0"
fi
if has "$DAEMON" '"allow-no-dsn"'; then
  ok "E7: the standalone-local-DB topology has an explicit flag"
else
  bad "E7: there is no way to say 'this node keeps its own database'"
fi

# --- F: the page (both catalogues) -------------------------------------------
for key in cluster.state_degraded cluster.reason_degraded cluster.reason_degraded_unknown \
           cluster.action_degraded cluster.cred_expired cluster.cred_expired_help; do
  n=$(grep -c "\"$key\":" "$EN_RU" || true)
  if [ "${n:-0}" -ge 2 ]; then
    ok "F1: $key is defined in BOTH catalogues (RU + EN)"
  else
    bad "F1: $key has ${n:-0} definition(s), want 2 (RU + EN parity)"
  fi
done
if has "$TMPL" '{{tf "cluster.reason_degraded" .HealthReason}}'; then
  ok "F2: the template renders the MEASURED sentence, not a generic label"
else
  bad "F2: the degraded row's reason is not rendered"
fi
if has "$ADMIN" 'case "degraded":'; then
  ok "F3: the page's rule table has a degraded branch (no row falls through to 'unknown')"
else
  bad "F3: degraded rows have no named reason"
fi
if has "$ADMIN" 'func loadClusterCredentials' && has "$TMPL" 'cluster.cred_expired'; then
  ok "F4: an expired node credential is DISCLOSED on the page (not silently accepted, not fatally enforced)"
else
  bad "F4: the expired-credential fact is invisible"
fi

# --- G: the detector must discriminate the pre-B464 shape --------------------
cat > "$SKY_TMP/old_node.go" <<'EOF'
const (
	NodeStatePending  = "pending"
	NodeStateReady    = "ready"
	NodeStateDraining = "draining"
	NodeStateFailed   = "failed"
)
EOF
cat > "$SKY_TMP/old_join.go" <<'EOF'
	state = CASE
	    WHEN state = 'pending' THEN 'ready'
	    WHEN state = 'failed'  THEN 'ready'
	    ELSE state
	END
EOF
cat > "$SKY_TMP/old_daemon.go" <<'EOF'
	body, err := json.Marshal(cluster.HeartbeatRequest{NodeID: st.NodeID, Token: st.Token})
EOF
discriminates=1
for _pair in "$RD:readinessVerdict" "$NODE:NodeStateDegraded" "$JOIN:readinessVerdict" \
             "$ELECTOR:case \"ready\", \"degraded\":" "$DAEMON:allow-no-dsn" "$PROBE:redactProbeDetail"; do
  _file="${_pair%%:*}"; _marker="${_pair#*:}"
  has "$_file" "$_marker" || discriminates=0
done
for _marker in 'NodeStateDegraded' 'readinessVerdict' 'allow-no-dsn' 'redactProbeDetail'; do
  has "$SKY_TMP/old_node.go" "$_marker" && discriminates=0
  has "$SKY_TMP/old_join.go" "$_marker" && discriminates=0
  has "$SKY_TMP/old_daemon.go" "$_marker" && discriminates=0
done
if [ "$discriminates" -eq 1 ]; then
  ok "G1: every predicate is present in the fixed files and absent from the pre-B464 shape"
else
  bad "G1: the predicates do not discriminate the pre-B464 shape"
fi

# --- H: the clock seam (the gate failed for two hours a day, on THREE checks) --
# Measured on the reference host 2026-11-12 05:37 UTC: go test ./... failed in
# internal/feature/admin with «a fresh run produced an excuse: … the next tick will
# catch up the missed slot …». Nothing was wrong with the product — the test passed
# lastRun = now−2 h and asked "is a slot due?", which is TRUE for two hours after the
# 04:00 slot — so the gate failed on B1, B132 and B176 at once, twice a day, and the
# verdict read like a product regression (trap #15).
if has "$H446" 'func backupStaleCauseAt(s *Service, lastRun int64, now time.Time) string'; then
  ok "H1: the stale-backup diagnostic takes an injected clock (a test can pin the moment)"
else
  bad "H1: the diagnostic reads time.Now() internally, so its test is a fact about the hour"
fi
if has "$H446" 'return backupStaleCauseAt(s, lastRun, time.Now())'; then
  ok "H2: production still uses the real clock (the seam did not change behaviour)"
else
  bad "H2: the production wrapper no longer delegates to the injectable form"
fi
if has "$H446T" 'backupStaleCauseAt(getTestService()'; then
  ok "H3: the B446 test PINS a moment instead of asking the wall clock"
else
  bad "H3: the B446 test still depends on the hour the gate happens to run"
fi
if has "$H446T" 'next tick will catch up'; then
  ok "H4: the missed-slot branch is still asserted, so pinning 'no excuse' did not disable it"
else
  bad "H4: nothing proves the catch-up sentence can still fire"
fi

# --- I: the credential's age is reported on the HOST too -----------------------
# The panel is another machine. The daemon's journal is the log an on-host operator
# reads, so "your credential expired 33 days ago" must appear there as well — and the
# sentence has to name the rotation path, or it is just alarming noise.
if has "$DAEMON" 'warnIfCredentialExpired(st.Token)'; then
  ok "I1: the daemon reports its own expired credential at startup"
else
  bad "I1: an expired credential is invisible on the host itself"
fi
if has "$DAEMON" 'func tokenExpiryUnix(token string) (int64, bool)'; then
  ok "I2: the expiry is read from the node's OWN token (no secret, no verification)"
else
  bad "I2: no expiry claim reader"
fi
if has "$DAEMON" 'To rotate it: issue a fresh invite on the primary'; then
  ok "I3: the warning names the rotation procedure, not just the problem"
else
  bad "I3: the warning states the fact without the action"
fi

# --- J: the panel's own onboarding block teaches the new verdict ---------------
# B464 changed what `ready` MEANS, so the block the panel hands the operator for a
# new standby had to change with it — otherwise he follows the instructions, gets
# `degraded`, and concludes the product is broken. Two facts must survive:
# the unit must source the SERVICE's env (or the probe guesses the port — B270's
# 8080/8082 trap), and the block must ASK the hub's questions while saying that a
# failure means `degraded`, not "broken".
if has "$ONBOARD" 'EnvironmentFile=-/etc/skygate/skygate.env'; then
  ok "J1: the generated heartbeat unit takes SKYGATE_PORT from the service env (no port guessing)"
else
  bad "J1: the unit passes no environment, so the probe would measure the wrong port on a :8082 host"
fi
if has "$ONBOARD" 'curl -fsS -m 3 \"http://127.0.0.1:$PORT/healthz\"' \
   && has "$ONBOARD" '/readyz'; then
  ok "J2: the block ends by asking /healthz AND /readyz — the two inputs of the verdict"
else
  bad "J2: the block does not show what the verdict is made of"
fi
if has "$EN_RU" 'состояние будет <code>degraded</code>' && has "$EN_RU" 'the state will be <code>degraded</code>'; then
  ok "J3: both notes say that a host with no web service is degraded BY DESIGN (RU + EN)"
else
  bad "J3: the onboarding note still promises a standby that needs no web service"
fi
if has "$B365C" 'A5b:' && has "$B365C" 'A5c:'; then
  ok "J4: the B365 contract was renegotiated, not deleted (the claim is still forbidden, the question is required)"
else
  bad "J4: the B365 contract still forbids the readiness probes outright"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB464: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB464: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
