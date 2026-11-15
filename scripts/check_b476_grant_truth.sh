#!/usr/bin/env bash
# ============================================================================
# check_b476_grant_truth.sh — B476
#
# «ПОЧЕМУ ОПЯТЬ НЕ ВСЁ КАК РАНЬШЕ» — the ACL document stopped being accepted.
#
# Measured 2026-11-15 on the live host, one day after v1.5.217 (B470):
#
#   acl-drift: re-apply FAILED (headscale PUT /api/v1/policy: 500
#     {"code":2,"message":"setting policy: parsing policy: multiple errors:
#      ip and app can not both be empty …"}) — the live policy stays stale
#
# The tailnet kept running, the panel kept showing new state, and EVERY rule /
# tag / exit-node / route change was silently not applied, because headscale
# 0.29.3 rejects the WHOLE document when a `grants[]` entry has neither `ip`
# nor `app`. The offending entries were the per-DEVICE mesh grants (one line
# per device — 16 on that tailnet): B470 taught the shared writer the legacy
# `acls[]` port suffix ("':*'") and dropped the `ip` field on the way, and the
# `grants[]` generator is the one that passes "" (no suffix).
#
# The blocked class is "a field that one schema requires and the other rejects,
# written by a helper both schemas share". Contracts:
#   A. the helper takes the schema flag and writes `ip` only when asked
#   B. both generators pass their own schema
#   C. the empty-dst shape (duplicate tags) can no longer be emitted
#   D. tests assert the headscale rule, not a proxy for it
#   E. the measured headscale error is quoted at the site that must honour it
#   F. B470 is renegotiated in the same direction (it must not pin the bug back)
#   G. the docs tell the operator what to do (EN + RU, both catalogues)
#   H. the predicates discriminate the pre-B476 shape
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b476.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

PERDEV=internal/acl/acl_perdevice.go
GEN=internal/acl/acl_generate.go
GENVIA=internal/acl/acl_generate_via.go
ATEST=internal/acl/acl_b476_test.go
B470=scripts/check_b470_prefix_relay.sh
VERIFY=scripts/verify_pre_deploy.sh
DOC=docs/troubleshooting.md
RUDOC=docs/ru/acl-grants-ip.md
DOCIDX=docs/README.md
RUDOCIDX=docs/ru/README.md

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$PERDEV" "$GEN" "$GENVIA" "$ATEST" "$B470" "$VERIFY" "$DOC" "$RUDOC" "$DOCIDX" "$RUDOCIDX"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the helper carries the schema flag ----------------------------------
if has "$PERDEV" 'func writePerDeviceGrants(sb *strings.Builder, usernames []string, tagsByUser map[string][]string, dstPortSuffix string, ipField bool)'; then
  ok "A1: the helper takes the ip-field flag (the schema property, not a constant)"
else
  bad "A1: the helper has no schema flag — one schema's requirement becomes the other's rejection"
fi
if has "$PERDEV" 'if ipField {'; then
  ok "A2: the ip field is conditional on the caller's schema"
else
  bad "A2: the ip field is unconditional (it would invalidate the legacy acls[] document)"
fi
if has "$PERDEV" 'sb.WriteString(", \"ip\": [\"*\"]")'; then
  ok "A3: the grants shape carries the mandatory ip field"
else
  bad "A3: no ip field is written — headscale refuses the document with \"ip and app can not both be empty\""
fi
# A4: the field must come AFTER the dst array closes, otherwise the JSON is malformed.
l_close=$(grep -nF 'sb.WriteString("]")' "$PERDEV" | head -1 | cut -d: -f1)
l_ip=$(grep -nF 'sb.WriteString(", \"ip\": [\"*\"]")' "$PERDEV" | head -1 | cut -d: -f1)
if [ -n "$l_close" ] && [ -n "$l_ip" ] && [ "$l_ip" -gt "$l_close" ]; then
  ok "A4: the ip field is appended after the dst array closes"
else
  bad "A4: the ip field is not positioned after dst (close=${l_close:-?} ip=${l_ip:-?}) — the emitted JSON would be invalid"
fi

# --- B: both generators pass their own schema -------------------------------
if has "$GEN" 'meshTagsByUser, ":*", false)'; then
  ok "B1: the legacy acls[] generator passes \":*\" AND no ip field"
else
  bad "B1: the legacy generator's call does not state its schema"
fi
if has "$GENVIA" 'meshTagsByUser, "", true)'; then
  ok "B2: the grants[] generator passes \"\" AND the ip field"
else
  bad "B2: the grants[] generator would emit a grant with neither ip nor app"
fi

# --- C: the empty-dst shape ---------------------------------------------------
if has "$PERDEV" 'if len(dstTags) == 0 {'; then
  ok "C1: a grant with an EMPTY dst is never emitted"
else
  bad "C1: an empty dst can still be emitted (invalid in both schemas)"
fi
if has "$PERDEV" 'seen[t] = true'; then
  ok "C2: repeated tags are de-duplicated before the >=2 test"
else
  bad "C2: a duplicated tag can still pass the size test and then leave an empty dst"
fi

# --- D: the tests assert the headscale rule ----------------------------------
for _t in TestB476_EveryGrantCarriesIPOrApp TestB476_LegacySchemaStillHasNoIPField TestB476_NoGrantWithEmptyDst TestB476_ValidatorDiscriminates; do
  if has "$ATEST" "$_t"; then
    ok "D1: $_t exists"
  else
    bad "D1: $_t is missing"
  fi
done
if has "$ATEST" 'len(g.IP) == 0 && len(g.App) == 0'; then
  ok "D2: the test asserts headscale's actual rule (ip OR app), not a proxy"
else
  bad "D2: the test does not assert the rule headscale rejects the document on"
fi
# Trap #9: capture first, then match — grep -q into a still-writing go test SIGPIPEs.
# A host without the Go toolchain (or a WSL `bash` that has no Windows PATH) must
# report SKIP, never FAIL: the run has not verified anything about the product.
if ! command -v go >/dev/null 2>&1; then
  printf '  \033[33m-\033[0m D3: SKIP — no go toolchain on PATH (%s)\n' "${BASH:-bash}"
else
  OUT="$(go test ./internal/acl/ -run TestB476 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ] && case "$OUT" in *"ok "*) true;; *) false;; esac; then
    ok "D3: go test ./internal/acl/ -run TestB476 passes"
  else
    bad "D3: B476 tests fail or did not run: $(printf '%s' "$OUT" | tail -3)"
  fi
fi

# --- E/F: the measured failure stays quoted, B470 renegotiated --------------
if has "$PERDEV" 'ip and app can not both be empty'; then
  ok "E1: the measured headscale error is quoted where the format is decided"
else
  bad "E1: the format requirement is undocumented at the site that must honour it"
fi
if has "$B470" 'meshTagsByUser, ":*", false)' && has "$B470" 'meshTagsByUser, "", true)'; then
  ok "F1: B470 pins the schema-aware call (it no longer pins the broken four-argument form)"
else
  bad "F1: B470 still pins a shape that cannot be both correct in one schema and valid in the other"
fi
if has "$VERIFY" '"B476"'; then
  ok "F2: the contract is registered in the gate"
else
  bad "F2: an unregistered check never runs"
fi

# --- G: the operator can find the symptom and the fix -----------------------
if has "$DOC" 'ip and app can not both be empty' && has "$DOC" 'B476'; then
  ok "G1: the EN troubleshooting guide explains the symptom and the fix"
else
  bad "G1: the EN guide does not cover the failure"
fi
if has "$RUDOC" 'ip and app can not both be empty' && has "$RUDOC" '/admin/acls/export'; then
  ok "G2: the RU doc carries the diagnosis and the verification command"
else
  bad "G2: the RU doc is missing the diagnosis or the verification step"
fi
if has "$DOCIDX" 'acl-grants-ip.md' && has "$RUDOCIDX" 'acl-grants-ip.md'; then
  ok "G3: both catalogues index the RU doc (an unindexed doc is not found)"
else
  bad "G3: the RU doc is not indexed in both catalogues"
fi

# --- H: the predicates discriminate the pre-B476 shape ----------------------
cat > "$SKY_TMP/old.go" <<'EOF'
			sb.WriteString("    { \"src\": [\"" + srcTag + "\"], \"dst\": [")
			sb.WriteString("] }")
EOF
discriminates=1
has "$PERDEV" 'if ipField {' || discriminates=0
has "$PERDEV" 'sb.WriteString(", \"ip\": [\"*\"]")' || discriminates=0
has "$GENVIA" 'meshTagsByUser, "", true)' || discriminates=0
for _m in 'if ipField {' 'ip": ["*"]' 'meshTagsByUser, "", true)'; do
  has "$SKY_TMP/old.go" "$_m" && discriminates=0
done
if [ "$discriminates" -eq 1 ]; then
  ok "H1: every predicate is present now and absent from the pre-B476 shape"
else
  bad "H1: the predicates do not discriminate the pre-B476 shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB476: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB476: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
