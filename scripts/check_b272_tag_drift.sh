#!/usr/bin/env bash
# check_b272_tag_drift.sh
#
# 2026-09-19 (B272) — tags must actually reach headscale, and the operator must
# hear about it when they cannot.
#
# Live case (host `aro`, AFTER upgrading to v1.5.12):
#
#   node_owner_map : 2 | | user=daniil | tag=tag:dev-daniil-workpc
#   headscale      : node 2  user=daniil  dev=-  all=-          ← no tags at all
#   audit          : device_adopt_addtag_failed
#                    err=tag: exec: "docker": executable file not found in $PATH
#                    device_adopt_ensure_tag_owner_failed
#                    headscale PUT /api/v1/policy: 500
#                    {"code":2,"message":"update is disabled for modes ot…"}
#   metric         : skygate_tag_autoupdate_failures_total had NEVER been bumped
#   telegram       : not configured → no notification at all
#
# Three defects, all silent:
#   1. headscale ran with `policy.mode: file`, so PUT /api/v1/policy answered
#      500 "update is disabled for modes other than database". SetPolicy only
#      fell back to the file path on 404/405, so the fallback never ran — and
#      even if it had, it wrote through a hardcoded docker volume
#      (/home/admin/headscale/config) that cannot exist on a native install.
#   2. TagNode/UntagNode went straight to `docker exec … headscale nodes tag`,
#      so on a native install EVERY tag write failed.
#   3. The autoupdater walked headscale and only considered nodes that ALREADY
#      carried a dev-tag. A node whose tag never landed was therefore skipped
#      forever, and the divergence between the database and headscale produced
#      no audit row, no metric and no alert.
#
# CONTRACTS
#   A  file-mode policy: the 500 is recognised and the real policy file is
#      written (native path configurable, docker path not hardcoded)
#   B  tag writes: REST API first, install-kind-aware CLI second
#   C  the reconciler exists, runs every tick, and reports drift
#   D  unattributed nodes get their own metric
#   E  the admin adopt path reports through the same alert sink
#   F  Go contracts (behavioural, incl. a real policy-file write)
#   G  live state on this host (SKIPs when headscale/skygate are unreachable)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B272: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

ACL=internal/headscale/acl.go
TAGS=internal/headscale/tags.go
HS=internal/headscale/headscale.go
AUTO=internal/nodeownership/auto.go
ALERT=internal/nodeownership/auto_alert.go
ADOPT=internal/feature/admin/adopt_devices.go
COLL=internal/metrics/collector.go

hdr "B272 — tags reach headscale (file-mode policy, REST tag writes, drift reconciliation)"

# --- A: file-mode policy -----------------------------------------------------
if grep -q 'func isFileModePolicyError(err error) bool' "$ACL"; then
  ok "A: file-mode policy failures are classified explicitly"
else
  bad "A: no file-mode classifier — a 500 'update is disabled' stays an opaque failure"
fi
if grep -q 'strings.Contains(body, "update is disabled")' "$ACL"; then
  ok "A2: the headscale 0.29.2 500 body ('update is disabled') triggers the file fallback"
else
  bad "A2: the 500 body is not recognised, so the live case would still not fall back"
fi
if grep -q 'func (c \*Client) setPolicyViaFileNative(' "$ACL" && grep -q 'func (c \*Client) setPolicyViaFileDocker(' "$ACL"; then
  ok "A3: both write paths exist (native filesystem + containerised volume)"
else
  bad "A3: one of the write paths is missing"
fi
if grep -q 'os.Rename(tmp, path)' "$ACL"; then
  ok "A4: the native write is atomic (temp + rename)"
else
  bad "A4: the native write is not atomic — a reader can see a half-written policy"
fi
if grep -q 'func DiscoverPolicyPath() (string, error)' "$ACL" && grep -q 'func parseHeadscalePolicyConfig(cfg string) (path, mode string)' "$ACL"; then
  ok "A5: the policy path can be discovered from headscale's config (no hardcoded layout)"
else
  bad "A5: the policy path is not discovered — a native install cannot be supported"
fi
if grep -q 'SKYGATE_HEADSCALE_POLICY_PATH' "$ACL" && grep -q 'SKYGATE_HEADSCALE_CONFIG_VOLUME' "$HS"; then
  ok "A6: the policy path and the container volume are both configurable"
else
  bad "A6: the hardcoded volume/layout is still in place"
fi
if grep -q 'if readErr == nil {' "$ACL" && grep -q 'restarting %s failed' "$ACL"; then
  ok "A7: a failed reload rolls the policy file back"
else
  bad "A7: a failed reload leaves the new policy on disk without headscale knowing"
fi

# --- B: tag writes over the API ---------------------------------------------
if grep -q 'func (c \*Client) setNodeTagsAPI(' "$TAGS" && grep -q '/api/v1/node/%d/tags' "$TAGS"; then
  ok "B: tag writes have a REST implementation"
else
  bad "B: tag writes are still CLI-only (impossible on a native install)"
fi
if grep -q 'if err := c.setNodeTagsAPI(nodeID, tags); err == nil {' "$TAGS"; then
  ok "B2: the REST path is tried FIRST"
else
  bad "B2: the REST path is not first — a host without docker still fails"
fi
if grep -q 'c.runHeadscaleCLI("nodes", "tag"' "$TAGS"; then
  ok "B3: the CLI fallback is install-kind aware (runHeadscaleCLI, from B267)"
else
  bad "B3: the CLI fallback does not use runHeadscaleCLI"
fi
if grep -v '^[[:space:]]*//' "$TAGS" | grep -q 'exec.Command("docker", args...)\|exec.Command("docker", fullArgs...)'; then
  bad "B4: a raw docker exec is still present in tags.go"
else
  ok "B4: no raw docker exec remains in the tag path"
fi

# --- C: the reconciler -------------------------------------------------------
if grep -q 'func ReconcileTags(' "$AUTO" && grep -q 'TagReconcileResult' "$AUTO"; then
  ok "C: the database→headscale reconciler exists"
else
  bad "C: no reconciler — a tag that never landed stays missing forever"
fi
if grep -q 'ReconcileTags(dbConn, hs, nodes, rows, os.Getenv("SKYGATE_BASE_DOMAIN"), alertSink)' "$AUTO"; then
  ok "C2: it runs on every autoupdater tick"
else
  bad "C2: the reconciler is not wired into the tick"
fi
if grep -q 'var ErrNoStrategyMatch' "$AUTO" && grep -q 'var ErrTagNotInHeadscale' "$ALERT"; then
  ok "C3: both drift kinds (tag_missing, no_strategy) have explicit sentinels"
else
  bad "C3: the drift kinds are not classifiable"
fi
if grep -q 'ReasonTagMissing FailureReason = "tag_missing"' "$ALERT" && grep -q 'ReasonNoStrategy FailureReason = "no_strategy"' "$ALERT"; then
  ok "C4: the reasons are part of the B227 alert vocabulary"
else
  bad "C4: the new reasons are missing from the alert vocabulary"
fi
# CONTRACT RENEGOTIATION (2026-09-20, B272.7): the call site moved behind the
# retry wrapper in v1.5.32 and behind the batch pre-pass in v1.5.35, but the
# property this contract protects is unchanged — the reconciler must make a tag
# PERMITTED before it asks headscale to apply it (the live
# `400 requested tags [...] are invalid or not permitted`). Asserting one exact
# call spelling made the check report a FAIL for a correct refactor; it now
# accepts the direct call, the retry wrapper and the batch pre-pass, and requires
# at least one of them to be present.
if grep -qE 'ensureTagIsPermitted(Retry)?\(hs, r, baseDomain, ensured\)' "$AUTO" \
   || grep -q 'ensureTagOwnersBatch(hs, rows, byID, baseDomain, ensured)' "$AUTO"; then
  ok "C6: the reconciler ensures tagOwners BEFORE applying a tag (direct call, retry wrapper or the B272.7 batch)"
else
  bad "C6: the reconciler applies tags without ensuring their owner — headscale answers 400 for an unknown tag"
fi
if grep -q 'ReconcileTags(dbConn, hs, nodes, rows, os.Getenv("SKYGATE_BASE_DOMAIN"), alertSink)' "$AUTO"; then
  ok "C7: the policy owner expression uses the configured base domain"
else
  bad "C7: the base domain does not reach the reconciler, so owners cannot be expressed"
fi
if grep -q 'func ListNodeOwnersAll(' internal/db/node_owner_map.go; then
  ok "C5: the database side of the comparison is queryable"
else
  bad "C5: no ListNodeOwnersAll helper"
fi

# --- D: unattributed metric --------------------------------------------------
if grep -q 'skygate_tag_unmatched_total' "$COLL"; then
  ok "D: unattributed nodes have their own metric"
else
  bad "D: no skygate_tag_unmatched_total — unattributed devices stay invisible"
fi
if grep -q 'metrics.TagUnmatchedCounter' "$ALERT"; then
  ok "D2: the metric is incremented on the no_strategy path"
else
  bad "D2: the metric is declared but never incremented"
fi

# --- E: the admin adopt path -------------------------------------------------
if grep -q 'adminTagAlertSink(s).ReportFailure' "$ADOPT"; then
  ok "E: adopt/tag failures go through the same alert sink (metric + audit + alert)"
else
  bad "E: the admin path still reports only an audit row (the live case never reached /metrics)"
fi
if grep -q 'func adminTagAlertSink(s \*Service) \*nodeownership.TagAlertSink' "$ADOPT"; then
  ok "E2: the sink is constructed for admin callers"
else
  bad "E2: adminTagAlertSink is missing"
fi

# --- F: Go contracts --------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  OUT="$(go test -count=1 -run 'B272' ./internal/headscale/ ./internal/nodeownership/ 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q '^FAIL' <<< "$OUT"; then
    ok "F: the B272 Go contracts pass (policy-file write incl. rollback, REST-first tag writes, reconciler)"
  else
    bad "F: the B272 Go contracts failed: $OUT"
  fi
  OUT2="$(go test -count=1 -run 'B227' ./internal/nodeownership/ 2>&1)"
  if grep -q '^ok' <<< "$OUT2"; then
    ok "F2: the B227 alert-sink contracts still pass (no regression in the metric/audit/alert path)"
  else
    bad "F2: the B227 contracts regressed: $OUT2"
  fi
else
  skip "F: go not on PATH"
fi

# --- G: live state ----------------------------------------------------------
HS_CLI=""
if command -v headscale >/dev/null 2>&1; then
  HS_CLI="headscale"
elif command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -q headscale; then
  HS_CLI="docker exec $(docker ps --format '{{.Names}}' 2>/dev/null | grep headscale | head -1) headscale"
fi
DB=""
for c in /var/lib/skygate/skygate.db /var/lib/skygate/canary/skygate.db /home/skyadmin/skygate/data/skygate.db; do
  [ -r "$c" ] && DB="$c" && break
done

if [ -z "$HS_CLI" ] || [ -z "$DB" ]; then
  skip "G: live drift check needs the headscale CLI + skygate.db on this host (not the test machine)"
else
  rows="$($HS_CLI nodes list -o json 2>/dev/null | python3 -c '
import json,sys
try:
    ns=json.load(sys.stdin)
except Exception:
    sys.exit(0)
for n in ns:
    tags=[t for t in (n.get("tags") or []) if t.startswith("tag:dev-")]
    if not tags:
        print("%s|%s" % (n.get("id"), n.get("givenName")))
' 2>/dev/null)"
  if [ -z "$rows" ]; then
    ok "G: every headscale node carries a per-device tag"
  else
    # Report, do not fail: the operator may legitimately have not adopted the
    # device yet. What matters is that skygate can SEE it (the B272 metric).
    skip "G: nodes without a per-device tag: $(printf '%s' "$rows" | tr '\n' ' ') — /admin/devices should offer Adopt, and skygate_tag_unmatched_total should be non-zero"
  fi
  unmatched="$(curl -sS --max-time 5 "http://127.0.0.1:${SKYGATE_PORT:-8080}/metrics" 2>/dev/null | grep -c 'skygate_tag_unmatched_total')"
  if [ "${unmatched:-0}" -gt 0 ]; then
    ok "G2: /metrics exposes skygate_tag_unmatched_total (the classifier is live)"
  else
    skip "G2: /metrics did not expose skygate_tag_unmatched_total (no unattributed node yet, or another port)"
  fi
fi

# --- H: the policy-permission audit is surfaced to the admin (B272.2) -------
# Live follow-up: headscale ran as headscale:headscale while policy.hujson was
# root:skygate 0660 — headscale could not read its OWN policy, every policy API
# call answered 500 ("reading policy from path …: permission denied"), and the
# only clue was a nested 500 body in the autoupdater log.
if grep -q 'func AuditHeadscalePolicy() PolicyAudit' "$ACL"; then
  ok "H: the headscale package audits the policy file's accessibility"
else
  bad "H: no policy-permission audit exists"
fi
if grep -q 'PolicyAuditUnreadableByHeadscale' "$ACL" && grep -q 'PolicyAuditUnreadableBySkygate' "$ACL"; then
  ok "H2: both halves are distinguished (headscale cannot read vs skygate cannot write)"
else
  bad "H2: the audit cannot tell the two permission problems apart"
fi
if grep -q 'func policyPermissionFixes(' "$ACL" && grep -q 'chown %s:%s %s' "$ACL"; then
  ok "H3: the audit returns ready-to-paste fix commands"
else
  bad "H3: the audit reports a problem without the commands to fix it"
fi
if grep -q 'func canSudoRead(path, user string) bool' "$ACL" && grep -q 'func modeModelAllows(' "$ACL"; then
  ok "H4: the headscale-user verdict is probed for real (sudo -n -u … test -r) with a documented fallback"
else
  bad "H4: no real probe (a name-based guess would be wrong on any other account)"
fi
if grep -q 'headscale.AuditHeadscalePolicy()' cmd/skygate/main.go; then
  ok "H5: the boot sequence reports a permission problem with its fixes in the journal"
else
  bad "H5: the problem is still invisible until an operator reads a nested 500"
fi
if grep -q 'headscale.AuditHeadscalePolicy()' internal/feature/admin/derp_dashboard.go; then
  ok "H6: /admin/derp renders the audit"
else
  bad "H6: the admin page does not show the policy state"
fi
if grep -q 'policy_audit_title' internal/handlers/templates/admin/derp_dashboard.html \
   && grep -q 'derp_dashboard.policy_audit_title' internal/i18n/catalog_admin.go \
   && grep -q 'derp_dashboard.policy_audit_fixes' internal/i18n/catalog_admin.go; then
  ok "H7: the banner is rendered and both i18n keys exist (RU + EN)"
else
  bad "H7: the banner or its i18n keys are missing"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test -count=1 -run 'B2722' ./internal/headscale/ 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "H8: the audit's Go contracts pass (mode model, probe, fixes, not-applicable, missing file)"
  else
    bad "H8: the audit Go contracts failed: $OUT"
  fi
else
  skip "H8: go not on PATH"
fi

# --- I: the privileged policy handoff (B272.3) ------------------------------
# Live follow-up: even with the file group-writable, the write failed with
# "open /etc/headscale/policy.hujson.skygate.tmp: read-only file system" — the
# skygate unit runs with ProtectSystem=strict + ReadWritePaths=${data_dir}
# ${etc_dir}, so /etc/headscale is read-only for it BY DESIGN. The fix reuses
# the project's privilege split (B261): a data-only request + a root path unit.
if grep -q 'func RequestPolicyApply(path, policy string) error' internal/headscale/policy_helper.go \
   && grep -q 'func PolicyHelperArmed() bool' internal/headscale/policy_helper.go; then
  ok "I: the unprivileged service can hand the policy to a privileged helper"
else
  bad "I: no privileged policy handoff exists — a native install can never write the policy file"
fi
if grep -q 'ErrPolicyHelperUnavailable' internal/headscale/policy_helper.go \
   && grep -q 'errors.Is(helperErr, ErrPolicyHelperUnavailable)' "$ACL"; then
  ok "I2: a missing helper is reported (never a silent success)"
else
  bad "I2: a missing helper would be indistinguishable from success"
fi
if [ -f deploy/skygate-apply-policy.sh ] && bash -n deploy/skygate-apply-policy.sh 2>/dev/null; then
  ok "I3: the root-owned policy applier exists and parses"
else
  bad "I3: deploy/skygate-apply-policy.sh is missing or has a syntax error"
fi
if grep -q 'POLICY_PATH must be absolute' deploy/skygate-apply-policy.sh \
   && grep -q 'policy path mismatch' deploy/skygate-apply-policy.sh \
   && grep -q 'policy.prev' deploy/skygate-apply-policy.sh; then
  ok "I4: the applier validates the path (absolute + must match headscale's own config), backs up the previous policy and restores it on failure"
else
  bad "I4: the applier is missing its validation/rollback guards"
fi
if grep -q 'write_policy_units' deploy/install-common.sh \
   && grep -q 'skygate-policy.path' deploy/install-common.sh \
   && grep -q 'skygate-policy.service' deploy/install-common.sh; then
  ok "I5: the installer writes and enables the skygate-policy.path/.service pair"
else
  bad "I5: the installer does not install the policy units"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test -count=1 -run 'B2723' ./internal/headscale/ 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "I6: the handoff's Go contracts pass (request shape, validation, unavailable helper)"
  else
    bad "I6: the handoff Go contracts failed: $OUT"
  fi
else
  skip "I6: go not on PATH"
fi

# --- J: an untracked file must not lock the operator out (B272.4) -----------
# Live case: the image update aborted with
#   error: The following untracked working tree files would be overwritten by
#   checkout: scripts/skygate-move-to-infra.sh
# and rolled back, although the target revision CONTAINS that file — the
# leftover was skygate's own. The operator could not update at all until they
# deleted it by hand.
DOCKER_GO=internal/update/docker.go
if grep -q 'func (u \*DockerUpgrader) checkoutRef(' "$DOCKER_GO" \
   && grep -q 'u.checkoutRef(ctx, gitRef)' "$DOCKER_GO"; then
  ok "J: the docker upgrader preserves untracked files instead of aborting"
else
  bad "J: checkout still fails on an untracked leftover"
fi
if grep -q 'func (u \*DockerUpgrader) untrackedCheckoutConflicts(ctx context.Context, gitRef string)' "$DOCKER_GO" \
   && grep -q 'ls-tree", "-r", "--name-only"' "$DOCKER_GO"; then
  ok "J2: the conflict set is the intersection of untracked files and the target tree"
else
  bad "J2: the conflict detection is missing (or relies on a non-existent git --dry-run)"
fi
if grep -q 'checkout-stash' "$DOCKER_GO" && grep -q 'func copyFilePreservingMode(' "$DOCKER_GO"; then
  ok "J3: the local copy is backed up (with its mode) before the tracked version is materialised"
else
  bad "J3: the local copy would be destroyed without a backup"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test -count=1 -run 'B2724' ./internal/update/ 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "J4: the checkout contracts pass (real git: preserved+proceeded, modified tracked file still fails)"
  else
    bad "J4: the checkout contracts failed: $OUT"
  fi
else
  skip "J4: go not on PATH"
fi

# --- K: a host without github.com access can still self-update (B272.5) -----
# Live case: the image update aborted with
#   fatal: unable to access 'https://github.com/BarsSky/skygate.git/':
#     Failed to connect to github.com:443 after 132571 ms
# and rolled back — nothing in the product said the fetch source is configurable.
if grep -q 'func (u \*DockerUpgrader) fetchTarget(' "$DOCKER_GO" \
   && grep -q 'func (u \*DockerUpgrader) gitMirrorURL()' "$DOCKER_GO"; then
  ok "K: the updater can fetch from a configured mirror when origin is unreachable"
else
  bad "K: the git source is hardcoded to origin — an offline host can never self-update"
fi
if grep -q 'SKYGATE_UPDATE_GIT_URL' "$DOCKER_GO" && grep -q 'SKYGATE_UPDATE_GIT_MIRROR' "$DOCKER_GO" \
   && grep -q '"update.git_url"' "$DOCKER_GO"; then
  ok "K2: the mirror comes from SKYGATE_UPDATE_GIT_URL (env) with the DB key update.git_url as fallback"
else
  bad "K2: the mirror cannot be configured (env or DB)"
fi
if grep -q 'u.SettingsFn = s.globalSettingFn()' internal/feature/admin/update.go \
   && grep -q 'func (s \*Service) globalSettingFn() func(key string) string' internal/feature/admin/dbsource.go; then
  ok "K3: the admin path wires the DB lookup, so the mirror is editable from the panel"
else
  bad "K3: the DB-configured mirror never reaches the upgrader"
fi
if grep -q 'refs/heads/\*:refs/remotes/origin/\*' "$DOCKER_GO" && grep -q 'refs/tags/\*:refs/tags/\*' "$DOCKER_GO"; then
  ok "K4: the mirror fetch uses an explicit refspec (tag + branch targets both resolve)"
else
  bad "K4: the mirror fetch would not populate the refs the checkout phase needs"
fi
if grep -q 'origin unreachable' "$DOCKER_GO" && grep -q 'SKYGATE_UPDATE_GIT_URL' "$DOCKER_GO"; then
  ok "K5: an unreachable origin with no mirror names the knob to set instead of a bare git error"
else
  bad "K5: an offline host still gets an opaque fetch failure"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test -count=1 -run 'B2725' ./internal/update/ 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "K6: the mirror contracts pass (real git: fallback fetch brings the tag + branches, no-mirror case names the knob)"
  else
    bad "K6: the mirror contracts failed: $OUT"
  fi
else
  skip "K6: go not on PATH"
fi

printf '\n\033[1mB272 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
