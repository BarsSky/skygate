#!/usr/bin/env bash
# B491 (TD-39) — the update check must not depend on ONE endpoint.
#
# Measured on the production host (2026-11-17): /admin/update printed
#   checker: GET https://api.github.com/…: context deadline exceeded
# while a plain `curl` from the same service user fetched the same URL in 0.2 s — intermittent DPI on
# the egress, not a broken panel. B486 made that failure HONEST (classification + layout) and
# deliberately left reachability open as TD-39: the endpoint itself has to be the operator's choice,
# exactly like the git remote has been since B272.5.
#
# Contracts:
#   A. the endpoint layer exists and is git-TRACKED (trap #11), with the parser, the resolver, the
#      default template and the mirror source label
#   B. the checker really tries them IN ORDER and keeps the git-tags door as the last resort
#   C. a GitHub token is NEVER sent to a mirror — that would hand the credential to a third party
#   D. every endpoint error NAMES its endpoint (the B486 lesson: a message that says "some error"
#      tells nobody where to look)
#   E. both check paths (the page and the scheduled job) read the same setting, and the page checker's
#      cache key includes it, so saving a mirror takes effect without a restart
#   F. the operator can reach it from the UI: form, route, audit, and the keys in BOTH catalogues
#   G. the behaviour is pinned by Go tests that actually run here
#   H. discoverable: registered in the catalog, indexed in AGENTS.md, recorded in the roadmap

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

LIB="internal/update/check_endpoint_b491.go"
CHK="internal/update/checker.go"
ADM="internal/feature/admin/update.go"
SET="internal/feature/admin/update_settings.go"
TPL="internal/handlers/templates/admin/update.html"
ROUTES="cmd/skygate/routes.go"
SCHED="cmd/skygate/main_update_scheduler.go"
I18N="internal/i18n/catalog_update.go"
TEST="internal/update/check_endpoint_b491_test.go"

# ═══════════════ A. the endpoint layer ═══════════════
if [ -f "$LIB" ]; then ok "A1: internal/update/check_endpoint_b491.go exists"
else bad "A1: the endpoint layer is missing"; fi

if git ls-files --error-unmatch "$LIB" >/dev/null 2>&1; then
    ok "A2: git tracks it (trap #11)"
else
    bad "A2: the endpoint layer is NOT tracked by git — the released tag would not carry it"
fi

missing=""
for sym in 'DefaultCheckEndpoint' 'func ParseCheckEndpoints' 'func CheckEndpointsFor' 'SourceMirror' 'func endpointSource' 'func isGitHubAPIHost'; do
    case "$(cat "$LIB")" in
        *"$sym"*) : ;;
        *) missing="$missing $sym" ;;
    esac
done
case "$missing" in
    '') ok "A3: the default template, the parser, the resolver and the mirror label are all present" ;;
    *)  bad "A3: missing from the endpoint layer:$missing" ;;
esac

case "$(cat "$LIB")" in
    *'{owner}'*'{repo}'*) ok "A4: the default endpoint is a TEMPLATE, so a fork or a rename follows" ;;
    *) bad "A4: the default endpoint is not templated" ;;
esac

case "$(cat "$LIB")" in
    *'http://'*'https://'*|*'HasPrefix(lower, "http://")'*)
        ok "A5: the parser accepts http(s) only (a file:// endpoint would be a local file read)" ;;
    *) bad "A5: the parser does not restrict the scheme" ;;
esac

# ═══════════════ B. order + the last resort ═══════════════
case "$(cat "$CHK")" in
    *'Endpoints []string'*) ok "B1: Checker carries the ordered endpoint list" ;;
    *) bad "B1: Checker has no Endpoints field" ;;
esac
case "$(cat "$CHK")" in
    *'for i, u := range urls'*) ok "B2: fetch tries the endpoints in the operator's order" ;;
    *) bad "B2: fetch does not iterate the endpoint list" ;;
esac
case "$(cat "$CHK")" in
    *'func (c *Checker) fetchFromEndpoint(ctx context.Context, apiURL string)'*)
        ok "B3: one endpoint = one request (the URL is a parameter, not a constant)" ;;
    *) bad "B3: fetchFromAPI still hardcodes the URL instead of taking it" ;;
esac
case "$(cat "$CHK")" in
    *'fetchFromTags'*) ok "B4: the git-tags door remains the last resort (B445 preserved)" ;;
    *) bad "B4: the git-tags fallback was removed" ;;
esac

# ═══════════════ C. the token never leaves GitHub ═══════════════
auth_ctx="$(grep -A4 'isGitHubAPIHost(apiURL)' "$CHK" 2>/dev/null)"
case "$auth_ctx" in
    *'Authorization'*) ok "C1: the bearer token is set INSIDE the isGitHubAPIHost branch" ;;
    *) bad "C1: the Authorization header is outside the GitHub-only branch — a mirror would receive the token" ;;
esac

# ═══════════════ D. errors name the endpoint ═══════════════
case "$(cat "$CHK")" in
    *'returned %d'*) ok "D1: a bad status names the endpoint and the code" ;;
    *) bad "D1: the status error still says only 'GitHub API returned N'" ;;
esac
case "$(cat "$CHK")" in
    *'GET %s returned %d'*) ok "D2: the message carries the URL that failed" ;;
    *) bad "D2: the failure message does not carry the URL" ;;
esac

# ═══════════════ E. both paths read the same setting ═══════════════
case "$(cat "$ADM")" in
    *'update.check_url'*) ok "E1: the page checker reads the update.check_url setting" ;;
    *) bad "E1: the page checker ignores the setting" ;;
esac
case "$(cat "$ADM")" in
    *'strings.Join(updateCheckEndpointsFor(s), ",")'*)
        ok "E2: the page checker's cache key includes the endpoints (a saved mirror applies at once)" ;;
    *) bad "E2: the cache key ignores the endpoints — a saved mirror would need a restart" ;;
esac
case "$(cat "$SCHED")" in
    *'update.check_url'*) ok "E3: the SCHEDULED check reads the same setting" ;;
    *) bad "E3: the scheduler does not read the setting — the page could be green while the nightly check keeps failing" ;;
esac
case "$(cat "$ADM")" in
    *'SKYGATE_UPDATE_CHECK_URL'*) ok "E4: the environment is the break-glass knob (read before the DB)" ;;
    *) bad "E4: no environment override" ;;
esac

# ═══════════════ F. reachable from the UI ═══════════════
case "$(cat "$ROUTES")" in
    *'POST /admin/update/check-url'*) ok "F1: the route is registered" ;;
    *) bad "F1: no route for the new form" ;;
esac
case "$(cat "$SET")" in
    *'func (s *Service) PostAdminUpdateCheckEndpoint'*) ok "F2: the handler exists" ;;
    *) bad "F2: the handler is missing" ;;
esac
case "$(cat "$SET")" in
    *'update_check_url'*) ok "F3: the change is audited" ;;
    *) bad "F3: saving the endpoint is not audited" ;;
esac
case "$(cat "$TPL")" in
    *'action="/admin/update/check-url"'*) ok "F4: the page has the form" ;;
    *) bad "F4: the page has no form for the endpoint" ;;
esac
case "$(cat "$TPL")" in
    *'UpdateCheckEnvOverride'*) ok "F5: the page says when the environment is overriding the field" ;;
    *) bad "F5: the page does not surface the env override — the operator would edit a field that has no effect" ;;
esac

i18n_keys="update.check_endpoint_title update.check_endpoint_label update.check_endpoint_save update.check_endpoint_saved update.check_endpoint_invalid update.check_endpoint_effective update.check_endpoint_env_override"
i18n_missing=""
for k in $i18n_keys; do
    n="$(grep -c "\"$k\":" "$I18N" 2>/dev/null)"
    [ "${n:-0}" -ge 2 ] || i18n_missing="$i18n_missing $k(${n:-0})"
done
case "$i18n_missing" in
    '') ok "F6: every new key exists in BOTH catalogues (RU + EN)" ;;
    *)  bad "F6: these keys are not in both catalogues:$i18n_missing" ;;
esac

# ═══════════════ G. the behaviour is actually tested ═══════════════
if [ -f "$TEST" ]; then ok "G1: the tests exist"; else bad "G1: no test file"; fi
tests_missing=""
for t in TestParseCheckEndpoints_B491 TestCheckEndpointsForPrecedence_B491 TestFetchFallsThroughEndpoints_B491 \
         TestFetchSendsTokenOnlyToGitHub_B491 TestFetchFallsBackToTagsWhenAllEndpointsFail_B491; do
    grep -q "func $t" "$TEST" 2>/dev/null || tests_missing="$tests_missing $t"
done
case "$tests_missing" in
    '') ok "G2: the five behavioural tests are present" ;;
    *)  bad "G2: missing tests:$tests_missing" ;;
esac
tout="$(go test ./internal/update/ -run 'B491' -count=1 2>&1)"; trc=$?
if [ "$trc" -eq 0 ]; then
    ok "G3: the endpoint tests pass here (($(printf '%s' "$tout" | grep -c '^--- PASS') PASS rows))"
else
    bad "G3: the endpoint tests fail: $(printf '%s' "$tout" | tail -3 | tr '\n' ' ')"
fi

# ═══════════════ H. discoverability ═══════════════
if grep -q 'check_b491_update_check_endpoint.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "H1: registered in scripts/verify_pre_deploy.sh"
else
    bad "H1: not registered in the catalog"
fi
if grep -q '^\- \*\*B491\*\*' AGENTS.md 2>/dev/null; then
    ok "H2: the AGENTS index carries its line"
else
    bad "H2: no AGENTS index line"
fi
if grep -q 'B491' docs/ROADMAP.md 2>/dev/null; then
    ok "H3: the roadmap records it (TD-39)"
else
    bad "H3: the roadmap does not mention B491"
fi

echo
echo "  B491: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
