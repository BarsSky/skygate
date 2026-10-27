#!/usr/bin/env bash
# check_b432_manual_update_target.sh — B432 (2026-10-27)
#
# «ЧЕРЕЗ ЗАГРУЗИТЬ ОБРАЗ НЕ ПОЛУЧИЛОСЬ» — the operator's screenshot showed the
# image-pull path refusing:
#
#   pre-flight: running container image "skygate-skygate" is not from a registry
#   (locally-built). Switch to docker-compose.ghcr.yml and set SKYGATE_IMAGE in .env
#   first. See internal/update/image.go header comment for the migration path
#
# The refusal is CORRECT (a source-built container cannot be replaced by a pulled image
# in place — the next `docker compose up` would rebuild it from source again), but it
# named only the registry migration, i.e. "change your install kind", and never the route
# that works for that install: the git+build updater behind «Обновить». And «Обновить»
# itself only renders when a NEWER release is known — from the cached GitHub check — so
# on a source-built install whose check was stale (B431) there was NO control on the page
# that could install a named version at all.
#
# B432 adds that control (a always-rendered "install a specific version" card posting the
# same /admin/update/apply endpoint) and rewrites the refusal so the working route comes
# FIRST and the migration is named as the alternative it is.
#
# CONTRACTS (11)

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

TPL=internal/handlers/templates/admin/update.html
IMG=internal/update/image.go
IMG_TEST=internal/update/image_b432_test.go
CAT=internal/i18n/catalog_update.go
GATE=scripts/verify_pre_deploy.sh
for f in "$TPL" "$IMG" "$IMG_TEST" "$CAT" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

hdr "A. a version can be named without the GitHub check"
if grep -qF '<input type="text" name="target" value="{{.Target}}"' "$TPL"; then
  ok "A1: the template has an EDITABLE target field pre-filled with the resolved version"
else
  bad "A1: no editable target input — an operator whose check is stale still cannot name a version"
fi
if grep -qF 'action="/admin/update/apply"' "$TPL"; then
  ok "A2: the manual control posts the same /admin/update/apply endpoint the CTA does"
else
  bad "A2: the manual control does not post to /admin/update/apply"
fi
# The card must NOT sit behind the IsNewer/PinDrift gate that hid the CTA: walk the card
# from its title to its input and assert neither gate appears in between.
GATED="$(awk '
  /update\.apply_manual_title/ { inside = 1 }
  inside && /name="target"/ { printf "%s\n%s", buf, $0; exit }
  inside { buf = buf "\n" $0 }
  END { printf "%s", buf }
' "$TPL")"
case "$GATED" in
  *IsNewer*|*PinDrift*)
    bad "A3: the manual control is still gated on IsNewer/PinDrift — a stale check would hide it again" ;;
  *)
    ok "A3: the manual control is not gated by the cached check (no IsNewer/PinDrift in its block)" ;;
esac
if grep -qF '{{if not .DevBuild}}' "$TPL"; then
  ok "A4: a dev build still does not get the control"
else
  bad "A4: the card is not guarded by 'not .DevBuild' — a dev build would offer to overwrite itself"
fi

hdr "B. the refusal names the route that works for this install"
if grep -qF 'func imagePullPreflightRefusal(runningImage string) error' "$IMG"; then
  ok "B1: the refusal is a named helper (testable, not inlined into the strategy)"
else
  bad "B1: the pre-flight refusal is still an inline fmt.Errorf — nothing can pin its wording"
fi
if grep -qF 'return imagePullPreflightRefusal(runningImage)' "$IMG"; then
  ok "B2: the pre-flight calls the helper"
else
  bad "B2: the pre-flight does not call the helper (the old text may still be in place)"
fi
if grep -qF '«Обновить»' "$IMG" && grep -qF 'docker-compose.ghcr.yml' "$IMG"; then
  ok "B3: both routes are named — the git+build updater and the registry migration"
else
  bad "B3: the refusal still names only one route (the operator is told to change install kind again)"
fi
if OUT="$(go test ./internal/update/ -run B432 -count=1 2>&1)"; then
  ok "B4: the wording test passes (route order, image name, pre-flight prefix, ST1005)"
else
  bad "B4: go test -run B432 failed: $(printf '%s' "$OUT" | tail -4 | tr '\n' ' ')"
fi

hdr "C. the new strings exist in both catalogues"
missing=""
for k in update.apply_manual_title update.apply_manual_hint update.apply_manual_button; do
  n=$(grep -cF "\"$k\":" "$CAT" 2>/dev/null || true)
  [ "$n" = "2" ] || missing="$missing $k($n)"
done
if [ -z "$missing" ]; then
  ok "C1: all three manual-target keys are defined in RU and EN"
else
  bad "C1: key(s) missing from one catalogue:$missing"
fi

hdr "D. registration"
if grep -q 'run_check "B432"' "$GATE"; then
  ok "D1: B432 is registered in the catalog"
else
  bad "D1: B432 is not registered — this contract would never run"
fi
if grep -q '\*\*B432\*\*' AGENTS.md; then
  ok "D2: AGENTS.md block index carries B432"
else
  bad "D2: AGENTS.md has no B432 entry"
fi
if git ls-files --error-unmatch scripts/check_b432_manual_update_target.sh >/dev/null 2>&1; then
  ok "D3: this script is tracked by git (trap 11)"
else
  bad "D3: this script is not tracked by git"
fi

printf '\n\033[1mB432 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
