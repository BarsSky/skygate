#!/usr/bin/env bash
# check_b311_preauth_key_request_truth.sh
#
# 2026-09-23 (B311) — «ключ не выдаётся на том, где реально запущен headscale».
#
# OPERATOR REPORT (native host `aro`, «Сгенерировать ключ»):
#
#   api: headscale POST /api/v1/preauthkey: 500 {"code":2,
#        "message":"auth-key must be either tagged or owned by user"}
#   cli: docker exec: exec: "docker": executable file not found in $PATH
#
# MEASURED against a live headscale v0.29.3 (the same 0.29.x surface `aro`
# runs), with non-existent ids so nothing was created:
#
#   {"user":999999}                  → 500 "user not found"      (field IS read)
#   {"user_id":999999}               → 500 "auth-key must be either tagged or
#                                          owned by user"        (field IGNORED)
#   POST /api/v1/preauthkey/expire   → 200                        (route EXISTS)
#   PUT  /api/v1/preauthkey/9/expire → 404 Not Found              (route GONE)
#   `headscale preauthkeys expire --help` → one flag: -i/--id     (no -u/--user)
#
# and, inspecting the created keys:
#
#   {"user":85,"tags":["tag:exit-node"]}     → aclTags: []      (silently dropped)
#   {"user":85,"acl_tags":["tag:exit-node"]} → aclTags: [ … ]   (applied)
#
# So ONE request body and ONE install-kind assumption were wrong in four ways:
# the owner field (`user`, not `user_id`), the tag field (`acl_tags`, not
# `tags`), the expire route (`POST /preauthkey/expire`, not `PUT /{id}/expire`)
# and the CLI rung (hardcoded `docker exec` — impossible on a native install —
# plus a `-u` flag `expire` does not have). The unit test that "covered" the
# request body asserted `"user_id":7`, i.e. the wrong field was PINNED as the
# contract, which is why CI stayed green while the feature was dead.
#
# CONTRACTS
#   A. the create body speaks the fields headscale READS (user, acl_tags) and
#      nothing else, pinned behaviourally
#   B. the CLI rung goes through the install-kind ladder (docker when docker
#      exists, the local binary otherwise) — the `aro` half of the report
#   C. the expire path uses the route this headscale serves, and the CLI flag
#      spelling it actually has
#   D. the measured truth table is documented and the release notes carry it
#   E. registered in the catalogue, tracked by git
#   F. preauth.go is the ONLY place that talks to the preauth-key API, so the
#      fix covers every caller (panel, exit-node register, sidecar, deployrun)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B311: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

PA=internal/headscale/preauth.go
PAT=internal/headscale/preauth_b311_test.go

# Code only — the file's own documentation names the WRONG spellings on purpose
# (that is how the next reader learns why they must not come back).
CODE="$(grep -v '^[[:space:]]*//' "$PA")"
hdr "B311 — the preauth key must be issuable on the host the headscale runs on"

# --- A: create body ----------------------------------------------------------
if printf '%s' "$CODE" | grep -q '"user":' && ! printf '%s' "$CODE" | grep -q '"user_id":'; then
  ok "A1: the owner travels as \`user\` (the field 0.29.x reads), never \`user_id\`"
else
  bad "A1: $PA still sends the ignored \`user_id\` (headscale answers 'auth-key must be either tagged or owned by user')"
fi
if printf '%s' "$CODE" | grep -q 'body\["acl_tags"\] = tags' \
   && ! printf '%s' "$CODE" | grep -q 'body\["tags"\] = tags'; then
  ok "A2: tags travel as \`acl_tags\` (the \`tags\` spelling is silently dropped)"
else
  bad "A2: $PA does not put tags in \`acl_tags\` — a tag:exit-node key would register an untagged node"
fi
# A3: the RESPONSE is wrapped in {"preAuthKey": {…}} with `user` as an object and
# `expiration` as a protobuf Timestamp, so a 200 with a real key used to look like
# an empty answer and the caller fell through to the CLI rung.
if printf '%s' "$CODE" | grep -q 'func parsePreauthKey(' \
   && printf '%s' "$CODE" | grep -q '"preAuthKey"' \
   && printf '%s' "$CODE" | grep -q 'func wireUser(' \
   && printf '%s' "$CODE" | grep -q 'func wireExpiration('; then
  ok "A3: the create response is parsed from the 0.29.x envelope (tolerant user/expiration shapes)"
else
  bad "A3: $PA cannot read the wrapped preAuthKey response — a successful create would fall through to the CLI"
fi

# --- B: the CLI rung ---------------------------------------------------------
if grep -q 'c.runHeadscaleCLI(' "$PA"; then
  ok "B1: the CLI rung goes through runHeadscaleCLI (install-kind aware)"
else
  bad "B1: $PA does not use the install-kind ladder for its CLI rung"
fi
if ! grep -q 'exec.Command("docker"' "$PA"; then
  ok "B2: no hardcoded \`docker\` invocation left in $PA (the aro failure)"
else
  bad "B2: $PA still hardcodes docker — a native install dies with \`executable file not found in \$PATH\`"
fi

# --- C: expire ---------------------------------------------------------------
if grep -q 'c.do("POST", "/api/v1/preauthkey/expire"' "$PA"; then
  ok "C1: expire tries the route this headscale serves (POST /api/v1/preauthkey/expire)"
else
  bad "C1: expire does not use POST /api/v1/preauthkey/expire (0.29.x 404s the PUT spelling)"
fi
if grep -q 'c.do("PUT", "/api/v1/preauthkey/"+keyID+"/expire"' "$PA"; then
  ok "C2: the older per-id PUT rung is kept for an older API"
else
  bad "C2: the older expire rung was dropped instead of kept as a fallback"
fi
EXP_LINE="$(grep '"preauthkeys", "expire"' "$PA" | head -1)"
if printf '%s' "$EXP_LINE" | grep -q -- '--id' \
   && printf '%s' "$EXP_LINE" | grep -q -- '--force' \
   && ! printf '%s' "$EXP_LINE" | grep -q -- '"-u"'; then
  ok "C3: the expire CLI rung passes --id/--force and no user flag"
else
  bad "C3: the expire CLI rung does not match the flag set 0.29.x actually has (-i/--id only): $EXP_LINE"
fi

# --- behavioural: the Go tests pin all of the above --------------------------
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/headscale/ -run 'B311|Preauth' -count=1 2>&1)"
  if printf '%s' "$OUT" | grep -q '^ok' && ! printf '%s' "$OUT" | grep -q 'FAIL'; then
    ok "D1: the B311/preauth tests pass (body fields, CLI ladder, local binary, expire rungs)"
  else
    bad "D1: the preauth tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "D1: go is not on PATH — run the B311 tests on the VM"
fi
if [ -f "$PAT" ] && grep -q 'TestB311_NativeInstallRunsTheLocalBinary' "$PAT" \
   && grep -q 'TestB311_ExpireCLIRungHasNoUserFlag' "$PAT" \
   && grep -q 'TestB311_NestedResponseIsTheSuccessfulPath' "$PAT" \
   && grep -q 'TestB311_FlatLegacyResponseStillParses' "$PAT"; then
  ok "D2: the native-install, expire-flag and wrapped-response regressions exist"
else
  bad "D2: $PAT is missing the native-install / expire-flag / wrapped-response regressions"
fi

# --- E: docs + notes + wiring ------------------------------------------------
if grep -q 'acl_tags' docs/LESSONS.md 2>/dev/null && grep -qi 'preauthkey/expire' docs/LESSONS.md 2>/dev/null; then
  ok "E1: docs/LESSONS.md records the measured field/route truth table"
else
  bad "E1: the measured truth table is not documented in docs/LESSONS.md"
fi
if grep -q 'B311' RELEASE-NOTES.md 2>/dev/null; then
  ok "E2: RELEASE-NOTES.md carries the B311 entry"
else
  bad "E2: RELEASE-NOTES.md has no B311 entry"
fi
if grep -q 'run_check "B311"' scripts/verify_pre_deploy.sh; then
  ok "E3: registered in the pre-deploy catalogue"
else
  bad "E3: B311 is not registered in scripts/verify_pre_deploy.sh"
fi
if git ls-files --error-unmatch scripts/check_b311_preauth_key_request_truth.sh >/dev/null 2>&1; then
  ok "E4: this script is tracked by git (trap #11)"
else
  bad "E4: scripts/check_b311_preauth_key_request_truth.sh is NOT tracked"
fi

# --- F: one chokepoint -------------------------------------------------------
OTHERS="$(git grep -l 'api/v1/preauthkey' -- 'internal/**/*.go' 'cmd/**/*.go' 2>/dev/null | grep -v 'internal/headscale/preauth.go' | grep -v '_test.go' || true)"
if [ -z "$OTHERS" ]; then
  ok "F1: preauth.go is the only file that talks to the preauth-key API (every caller is fixed)"
else
  bad "F1: these files bypass the fixed chokepoint: $(echo "$OTHERS" | tr '\n' ' ')"
fi

printf '\n\033[1mB311 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
