#!/usr/bin/env bash
# ============================================================================
# check_b470_prefix_relay.sh — B470
#
# «НЕ РАБОТАЕТ ДИСКОРД НА basic, И НЕ ВСЁ ПОКРЫВАЕТ» — the two halves of one decision
# disagreed, and nothing said so.
#
# Measured 2026-11-14, read-only, on the live tailnet:
#   * device `basic` exits through karolina (device_exit_node_prefs, set by its user);
#   * `prefix_owner` had the Cloudflare ranges (104.16.0.0/12, 162.158.0.0/15,
#     172.64.0.0/13, …) on EMILIA, because the ownership was a 1:1 TIE between two
#     devices and the alphabetical hostname tie-break picked the other relay; the rules
#     that produced emilia's claim had drifted there from karolina in the first place;
#   * the ACL therefore pinned `via=[emilia]` for those prefixes — and `via` is a
#     permission FILTER (B276), so `basic`, on karolina, silently lost them.
# On top of that the coverage itself was thin: `discordapp.net` and `discord.tools` have
# no A records at all, `discord.com`/`discord.gg` had expanded into 39/46 single /32s
# instead of CDN ranges, and gateway.discord.gg / cdn.discordapp.com / media.discordapp.net
# had no rules whatsoever.
#
# Fixing that in the panel means 21 prefix pins and ~60 rule rows one at a time, so this
# block ships ONE auditable command that reuses the panel's own primitives — and while
# running it the first time, the legacy `acls[]` generator turned out to be broken:
#
#   headscale PUT /api/v1/policy: 500 … cannot unmarshal JSON string into Go
#   v2.AliasWithPorts within "/acls/5/dst/0": dst="tag:dev-skyadmin-cyborg":
#   port range "dev-skyadmin-cyborg": invalid port range format
#
# i.e. on the DEFAULT configuration (SKYGATE_ACL_VIA_ENABLED unset) NO policy could be
# applied at all, because the per-device mesh block wrote bare tags into a schema where
# every dst entry is parsed as host:ports.
#
# Contracts:
#   A. the command exists, is dispatched, and reads the assignment table
#   B. it reuses the panel's primitives and audits with the panel's action names
#   C. a dry run writes nothing (every write sits behind --apply)
#   D. a pin re-applies the ACL, and the ACL schema comes from the LIVE policy
#   E. the legacy generator writes ports in every dst tag; the grants generator does not
#   F. the row selection speaks both database dialects (no PG-only ANY(array))
#   G. the diagnosis and the fix are written down for the operator (EN + RU)
#   H. tests pin the shape and the schema detection
#   I. the detector discriminates the pre-B470 shape
# ============================================================================
set -uo pipefail
# B339: read the cmd/skygate SURFACE, never cmd/skygate/main.go as one file (the same rule
# B467/B468 follow) — a contract that pins the entrypoint's path turns a pure move red.
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b470.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

CLI=cmd/skygate/prefix_owner_cli_b470.go
CTEST=cmd/skygate/prefix_owner_cli_b470_test.go
PERDEV=internal/acl/acl_perdevice.go
GEN=internal/acl/acl_generate.go
GENVIA=internal/acl/acl_generate_via.go
ATEST=internal/acl/acl_b470_test.go
DOC=docs/acl-rules-reference.md
RUDOC=docs/ru/exit-relay-prefix.md
DOCIDX=docs/README.md
RUDOCIDX=docs/ru/README.md
gosurface CMD cmd/skygate/*.go

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$CLI" "$CTEST" "$PERDEV" "$GEN" "$GENVIA" "$ATEST" "$DOC" "$RUDOC" "$DOCIDX" "$RUDOCIDX"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the command exists and is reachable ---------------------------------
if has "$CMD" 'case "dns-records", "dns-set-a", "prefix-owner":'; then
  ok "A1: the prefix-owner verb is dispatched from the entrypoint"
else
  bad "A1: `skygate prefix-owner` is not reachable"
fi
if has "$CMD" 'prefix-owner           which relay serves a prefix'; then
  ok "A2: the verb is in the usage text (an undocumented verb is an undiscoverable one)"
else
  bad "A2: the usage text does not mention prefix-owner"
fi
if has "$CLI" 'FROM prefix_owner' && has "$CLI" '--list'; then
  ok "A3: --list reads the assignment table (the table B275/B276 act on)"
else
  bad "A3: the command does not expose the current ownership"
fi

# --- B: the panel's primitives, the panel's audit names ---------------------
if has "$CLI" 'prefixowner.SetManual(pool, p, relay)'; then
  ok "B1: a pin uses the SAME primitive the panel's per-prefix action uses"
else
  bad "B1: the CLI writes prefix ownership some other way"
fi
if has "$CLI" 'db.UpdateDeviceRuleExitNode(pool, h.id, relay)'; then
  ok "B2: moving a rule uses the SAME primitive the panel's per-rule action uses"
else
  bad "B2: the CLI rewrites device_rules some other way"
fi
# The action names are matched WITHOUT their quotes (B466 A3b / trap #15): a contract that
# pins the quote style breaks on a formatting change.
if has "$CLI" 'prefix_owner_pin' && has "$CLI" 'device_rule_relay_move'; then
  ok "B3: both writes are audited with the panel's action names"
else
  bad "B3: a CLI write would be invisible in /admin/audit"
fi

# --- C: a dry run writes nothing -------------------------------------------
# Every write sits behind `if !apply` — the pin loop, the rule loop and the ACL step.
_pin_gate=$(grep -c 'if !apply {' "$CLI" 2>/dev/null || echo 0)
if [ "${_pin_gate:-0}" -ge 2 ]; then
  ok "C1: each write loop checks --apply before writing"
else
  bad "C1: a write is not gated by --apply (only $_pin_gate gate(s) found)"
fi
if has "$CLI" 'dry run: nothing written (add --apply)'; then
  ok "C2: a dry run says that it wrote nothing"
else
  bad "C2: a dry run is indistinguishable from an applied one"
fi
if grep -q 'if !apply || \*list' "$CLI" || grep -q 'if !write || \*list' "$CLI"; then
  ok "C3: --list can be combined with a write flag without writing"
else
  bad "C3: asking for a listing could apply a change"
fi

# --- D: a pin must move the policy, in the schema headscale serves ----------
if has "$CLI" 'reapplyACL(pool, cfg)'; then
  ok "D1: a pin re-applies the ACL (a database row alone does not move headscale)"
else
  bad "D1: the pin would sit in the database while headscale kept the old relay"
fi
if has "$CLI" 'noACL := fs.Bool("no-acl"'; then
  ok "D2: skipping the re-apply is an explicit flag, not an accident"
else
  bad "D2: the ACL step cannot be skipped deliberately"
fi
if has "$CLI" 'func livePolicyUsesVia(hs *headscale.Client) (useVia bool, why string, err error)' \
   && has "$CLI" 'hs.GetACL()'; then
  ok "D3: the schema is read from the LIVE policy"
else
  bad "D3: the CLI guesses the schema from its own environment (the 500 that started this block)"
fi
if has "$CLI" 'livePolicyUsesVia(hs)'; then
  ok "D4: the detected schema is what the re-apply asks for"
else
  bad "D4: the detection is not used by the re-apply"
fi
if has "$CLI" 'disagrees with the' && has "$CLI" 'the live schema wins'; then
  ok "D5: an env/live disagreement is REPORTED, not silently resolved"
else
  bad "D5: an operator whose shell disagrees with the deployment gets no signal"
fi

# --- E: the legacy dst shape ------------------------------------------------
if has "$PERDEV" 'sb.WriteString(dstPortSuffix)'; then
  ok "E1: the per-device block takes the port suffix from the caller (schema property, not a guess)"
else
  bad "E1: the per-device block hardcodes one schema's dst shape"
fi
if has "$GEN" 'writePerDeviceGrants(&sb, usernames, meshTagsByUser, ":*")'; then
  ok "E2: the legacy acls[] generator passes the REQUIRED :* suffix"
else
  bad "E2: the legacy acls[] generator writes bare tags — headscale rejects the whole policy"
fi
if has "$GENVIA" 'writePerDeviceGrants(&sb, usernames, meshTagsByUser, "")'; then
  ok "E3: the grants[] generator keeps bare aliases (the form the live policy uses)"
else
  bad "E3: the grants[] schema dst shape was changed without evidence"
fi
# The measured failure text is quoted where the next reader will hit it.
if has "$PERDEV" 'invalid port range format'; then
  ok "E4: the generator records the measured headscale error next to the fix"
else
  bad "E4: the port-suffix requirement is undocumented at the site that must honour it"
fi

# --- F: both dialects ---------------------------------------------------------
# Comment lines are excluded: the code documents the SHIPPED form (that is how the next
# reader learns why the helper is used), and a check must not turn its own explanation
# into the regression it looks for (B468 A2, trap #15).
if grep -n -- '= ANY(' "$CLI" 2>/dev/null | grep -v ':[[:space:]]*//' | grep -q .; then
  bad "F1: the CLI selects rows with = ANY(array) — a PostgreSQL-only form that dies on SQLite"
else
  ok "F1: no PostgreSQL-only row selection in the CLI"
fi
if has "$CLI" 'db.PlaceholdersRange('; then
  ok "F2: the IN lists are built from the shared placeholder helper"
else
  bad "F2: the IN lists are hand-rolled"
fi

# --- G: documented ------------------------------------------------------------
if has "$DOC" 'skygate prefix-owner --relay=karolina'; then
  ok "G1: the EN reference documents the fix command"
else
  bad "G1: the procedure is not documented"
fi
if has "$DOC" 'is a permission filter'; then
  ok "G2: the EN reference names the mechanism (via is a filter, not a hint)"
else
  bad "G2: the docs do not explain WHY one device loses one site"
fi
if has "$RUDOC" 'skygate prefix-owner --relay=karolina'; then
  ok "G3: the RU doc carries the same procedure"
else
  bad "G3: the RU operator has no runbook"
fi
if has "$RUDOCIDX" 'exit-relay-prefix.md' && has "$DOCIDX" 'exit-relay-prefix.md'; then
  ok "G4: both catalogues index the RU doc (an unindexed doc is not found)"
else
  bad "G4: the RU doc is not in the catalogue"
fi

# --- H: tests -----------------------------------------------------------------
_h=0
for t in TestB470_LegacyPerDeviceGrantsCarryPorts TestB470_LegacyEveryDstEntryIsHostPorts TestB470_ViaSchemaIsUntouched; do
  has "$ATEST" "func $t(" && _h=$((_h+1))
done
if [ "$_h" -ge 3 ]; then
  ok "H1: the dst shape is pinned for both schemas, and every legacy dst entry is swept against the host:ports grammar"
else
  bad "H1: only $_h of 3 generator tests exist"
fi
_j=0
for t in TestB470_LivePolicyUsesVia TestB470_LivePolicyUsesVia_ErrorIsReported; do
  has "$CTEST" "func $t(" && _j=$((_j+1))
done
if [ "$_j" -ge 2 ]; then
  ok "H2: the schema detection is pinned against a fake headscale (incl. the failure path)"
else
  bad "H2: only $_j of 2 detection tests exist"
fi

# --- I: the detector discriminates the pre-B470 shape ------------------------
cat > "$SKY_TMP/old.go" <<'EOF'
// The prefix page lets the operator pin one prefix at a time.
// Renewal of the ACL happens on the next sync pass.
EOF
discriminates=1
for _m in 'func livePolicyUsesVia(' 'the live schema wins' 'db.PlaceholdersRange(' 'prefix_owner_pin'; do
  has "$CLI" "$_m" || discriminates=0
done
for _m in 'sb.WriteString(dstPortSuffix)' 'invalid port range format'; do
  has "$PERDEV" "$_m" || discriminates=0
done
has "$GEN" 'meshTagsByUser, ":*"' || discriminates=0
has "$GENVIA" 'meshTagsByUser, "")' || discriminates=0
for _m in 'livePolicyUsesVia(' 'prefix_owner_pin' 'PlaceholdersRange(' 'dstPortSuffix'; do
  has "$SKY_TMP/old.go" "$_m" && discriminates=0
done
if [ "$discriminates" -eq 1 ]; then
  ok "I1: every predicate is present now and absent from the pre-B470 shape"
else
  bad "I1: the predicates do not discriminate the pre-B470 shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB470: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB470: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
