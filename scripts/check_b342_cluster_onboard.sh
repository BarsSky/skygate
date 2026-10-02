#!/usr/bin/env bash
# check_b342_cluster_onboard.sh — B342: onboard a SECOND host from the panel alone.
#
# WHY THIS FILE EXISTS (RR-15 option (a), 2026-10-02)
# ---------------------------------------------------
# `/admin/cluster` could add a node row and mint an invite, but it never told
# the operator what to run ON THE NEW HOST. docs/ha.md §2.4 took five steps in
# three places, four of them outside the panel — and §2.4 states the criterion
# itself: *"If admin must SSH to do X, then X is a gap, not a feature."*
#
# B342 is the panel-side artifact: one admin action mints the invite (sgn1) and
# a tailnet preauth key for the `infra` user, parks them behind an OPAQUE
# one-time token in global_settings, and `GET /admin/cluster?boot=<token>`
# consumes that token ONCE to render the ready-to-run block (tailnet → install
# the primary's version → join → service → approve). It is the B266 pattern
# (exit-node registration) applied to host onboarding; nothing is pushed to the
# target and no third-party credential is stored anywhere.
#
# What this script verifies:
#   A. the route + handler exist, behind authMW
#   B. the handler's guards: admin, isSafeNodeName, ttl bounds, URL validation
#   C. one-time parking: opaque token, consume DELETES, 15-minute sweep, and a
#      charset guard on the token before it becomes a settings key
#   D. the two credentials never reach a URL or the audit log (lengths only)
#   E. the flow uses the SHARED primitives (cluster.IssueInvite, the infra user,
#      CreatePreauthKeyWithTags) rather than re-implementing them
#   F. the template renders the card, and every cluster.onboard_* key used by
#      the Go code or the template is defined in BOTH catalogues
#   G. the pure unit tests run (order, credential placement, version fallback,
#      shell quoting)
#   H. tracked, registered, indexed (AGENTS §2 trap #11)
#
# Usage:  bash scripts/check_b342_cluster_onboard.sh
# Exit:   0 = all contracts hold, 1 = regression

set -uo pipefail
cd "$(dirname "$0")/.."

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

GO=internal/feature/admin/cluster_onboard_b342.go
TPL=internal/handlers/templates/admin/cluster.html
I18N=internal/i18n/catalog_admin.go
ROUTES=cmd/skygate/routes.go
CAT=scripts/verify_pre_deploy.sh

for f in "$GO" "$TPL" "$I18N" "$ROUTES" "$CAT"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B342 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

hdr "A. the route and the handler exist"

if grep -q 'mux.Handle("POST /admin/cluster/onboard", authMW(http.HandlerFunc(adminSvc.PostAdminClusterOnboard)))' "$ROUTES"; then
  ok "A1: POST /admin/cluster/onboard is registered behind authMW"
else
  bad "A1: POST /admin/cluster/onboard is not registered with authMW in cmd/skygate/routes.go"
fi
if grep -q '^func (s \*Service) PostAdminClusterOnboard(' "$GO"; then
  ok "A2: PostAdminClusterOnboard is defined"
else
  bad "A2: PostAdminClusterOnboard is missing"
fi
if grep -q '^func (s \*Service) consumeClusterOnboard(' "$GO"; then
  ok "A3: consumeClusterOnboard renders the parked payload once"
else
  bad "A3: consumeClusterOnboard is missing — the page could not render the block"
fi
if grep -q 'data.Onboard = s.consumeClusterOnboard(boot)' internal/feature/admin/cluster.go; then
  ok "A4: GET /admin/cluster consumes ?boot= into the page data"
else
  bad "A4: the cluster page does not consume ?boot= — the block would never be rendered"
fi

hdr "B. the handler's guards"

if grep -q 'c == nil || !c.IsAdmin' "$GO"; then
  ok "B1: admin-only guard"
else
  bad "B1: the handler has no admin guard"
fi
if grep -q 'isSafeNodeName(hostname)' "$GO"; then
  ok "B2: the hostname is validated with B266's isSafeNodeName (it is rendered into a shell command)"
else
  bad "B2: the hostname goes into a shell command without isSafeNodeName validation"
fi
if grep -q 'clusterOnboardMaxTTLHours' "$GO" && grep -q 'n < 1 || n > clusterOnboardMaxTTLHours' "$GO"; then
  ok "B3: ttl_hours is bounded on both sides"
else
  bad "B3: ttl_hours is not bounded"
fi
if grep -q 'url.Parse(apiURL)' "$GO"; then
  ok "B4: api_url is validated before it is rendered"
else
  bad "B4: api_url is rendered without validation"
fi
if grep -q 'if s.ClusterInviteSecret == ""' "$GO"; then
  ok "B5: a missing invite secret is reported instead of minting nothing"
else
  bad "B5: no ClusterInviteSecret guard"
fi

hdr "C. one-time parking (the B266 pattern)"

if grep -q 'db.RandomConfirmationToken(16)' "$GO"; then
  ok "C1: the render token is opaque and random (16 bytes)"
else
  bad "C1: the render token is not a random opaque token"
fi
if grep -q 'clusterOnboardSettingPrefix + token' "$GO"; then
  ok "C2: the payload is parked in global_settings under the token"
else
  bad "C2: the payload is not parked under the token"
fi
if grep -q '_ = db.SetGlobalSetting(s.dbc(), key, "")' "$GO"; then
  ok "C3: consuming the token DELETES the payload (shown exactly once)"
else
  bad "C3: the payload is not deleted on consume — it would be re-renderable"
fi
if grep -q 'clusterOnboardSweepAfter = 15 \* time.Minute' "$GO" && grep -q 'scheduleClusterOnboardSweep' "$GO"; then
  ok "C4: an unrendered payload (and its credentials) is swept after 15 minutes"
else
  bad "C4: no 15-minute sweep for an unrendered payload"
fi
if grep -q "r == '-' || r == '_'" "$GO"; then
  ok "C5: the token charset is restricted before it becomes a settings key"
else
  bad "C5: the token is used as a settings-key suffix without a charset guard"
fi

hdr "D. the credentials never reach a URL or the audit log"

REDIRECT="$(grep -n 'admin/cluster?boot=' "$GO" || true)"
if [ -n "$REDIRECT" ] && ! printf '%s' "$REDIRECT" | grep -q 'inviteToken\|tsKey'; then
  ok "D1: the redirect carries only the opaque token (never the invite or the preauth key)"
else
  bad "D1: the redirect URL carries a credential: $REDIRECT"
fi
if grep -q 'ts_key_len=%d' "$GO" && grep -q 'invite_len=%d' "$GO"; then
  ok "D2: the audit row records credential LENGTHS, not values"
else
  bad "D2: the audit row does not record lengths — a token in an audit detail is a token in every backup"
fi
if ! grep -q 'AppendAuditLog.*inviteToken' "$GO"; then
  ok "D3: no audit call passes the invite token"
else
  bad "D3: an audit call passes the invite token"
fi

hdr "E. shared primitives, not a second implementation"

if grep -q 'cluster.IssueInvite(' "$GO"; then
  ok "E1: the invite is minted by the shared cluster.IssueInvite"
else
  bad "E1: the invite token is minted locally instead of via cluster.IssueInvite"
fi
if grep -q 's.infraHeadscaleUserID(r.Context())' "$GO" && grep -q 'CreatePreauthKeyWithTags(infraID' "$GO"; then
  ok "E2: the tailnet key belongs to the infra user (not the synthetic tagged-devices)"
else
  bad "E2: the tailnet preauth key is not minted for the infra user"
fi
if grep -q 'cluster.LookupNode(' "$GO" && grep -q 'cluster.AddNode(' "$GO"; then
  ok "E3: the node row is created through the shared cluster helpers (idempotently)"
else
  bad "E3: the node row is written with a local query"
fi
if grep -q 'cluster.NodeRoleStandby' "$GO"; then
  ok "E4: the standby role comes from the shared constant"
else
  bad "E4: the role string is hardcoded"
fi

hdr "F. the template and the i18n catalogues"

if grep -q 'action="/admin/cluster/onboard"' "$TPL"; then
  ok "F1: the cluster page posts to the onboard action"
else
  bad "F1: no form posts to /admin/cluster/onboard"
fi
if grep -q '{{range .Data.Onboard.Steps}}' "$TPL"; then
  ok "F2: the page renders the numbered steps"
else
  bad "F2: the page does not render the onboarding steps"
fi
USED_KEYS="$( { grep -o 'cluster\.onboard_[a-z_]*' "$GO"; grep -o 'cluster\.onboard_[a-z_]*' "$TPL"; } | sort -u )"
MISSING=0
while IFS= read -r key; do
  [ -n "$key" ] || continue
  n="$(grep -c "\"$key\"" "$I18N" || true)"
  if [ "$n" -ne 2 ]; then
    bad "F3: $key appears $n time(s) in $I18N — every user-visible string needs ONE key in RU and ONE in EN (AGENTS rule 10)"
    MISSING=$((MISSING+1))
  fi
done <<< "$USED_KEYS"
if [ "$MISSING" -eq 0 ]; then
  ok "F3: every cluster.onboard_* key used by the Go code and the template is defined in RU and EN ($(printf '%s\n' "$USED_KEYS" | grep -c . ) keys)"
fi

hdr "G. the pure unit tests run"

if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/admin/ -run 'B342' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "G1: the B342 unit tests pass (order, credential placement, version fallback, quoting)"
  else
    bad "G1: the B342 unit tests failed: $(printf '%s' "$OUT" | tail -3)"
  fi
else
  skip "G1: no go binary in PATH — the unit tests were not run"
fi

hdr "H. tracked, registered, indexed"

for f in scripts/check_b342_cluster_onboard.sh "$GO" internal/feature/admin/cluster_onboard_b342_test.go; do
  if git ls-files --error-unmatch "$f" >/dev/null 2>&1; then
    ok "H: tracked by git: $f"
  else
    bad "H: NOT tracked by git (AGENTS §2 trap #11): $f"
  fi
done
if grep -q 'check_b342_cluster_onboard.sh' "$CAT"; then
  ok "H: registered in scripts/verify_pre_deploy.sh"
else
  bad "H: not registered in scripts/verify_pre_deploy.sh — it would never run"
fi
if grep -q '\*\*B342\*\*' AGENTS.md; then
  ok "H: recorded in the AGENTS.md block index"
else
  bad "H: no B342 line in the AGENTS.md block index"
fi

hdr "B342 summary: $PASS passed, $FAIL failed, $SKIP skipped"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
