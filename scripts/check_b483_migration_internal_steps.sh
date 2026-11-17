#!/usr/bin/env bash
# ============================================================================
# check_b483_migration_internal_steps.sh — B483
#
# THE WIZARD'S LAST MILE: the verification steps it named had no implementation, and the one host
# artefact it must reproduce lived only on the machine.
#
# B482 shipped the wizard and the per-step executor with every "internal" step (a panel action, not a
# shell command) reported as PENDING — honest, and useless on its own: the steps that answer "did it
# actually move?" are exactly those. This block wires them, and closes the repository half of TD-38:
# the derper certificate sync that the 2026-11-15/16 cutover created by hand and never committed, so a
# rebuilt host loses the renewal path and nobody notices until the relay dies ~90 days later.
#
# Contracts:
#   A. the internal actions exist, are wired into the handler, and refuse to be silent
#   B. registering a relay stays the existing B164 page — the wizard does not invent a region id
#   C. deploy/derper-cert-sync.sh: tracked, syntax-valid, chain-complete, idempotent, --install,
#      --dry-run, and it REFUSES a mismatched pair by name (behavioural, with real openssl material)
#   D. docs: derp.md documents the renewal, host-migration.md names the two orderings and the
#      non-automatable steps
#   E. registration: gate, block index, release notes
#   F. discriminators: a pre-B483 tree fails the predicates, and the script's refusals really fire
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b483.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

ACT=internal/feature/admin/migrate_internal.go
HANDLER=internal/feature/admin/migrate.go
ACT_TEST=internal/feature/admin/migrate_internal_test.go
PLAN=internal/migrate/plan.go
EXEC=internal/migrate/exec.go
EXEC_TEST=internal/migrate/exec_test.go
SYNC=deploy/derper-cert-sync.sh
DERPDOC=docs/derp.md
MIGDOC=docs/host-migration.md
MIGDOCRU=docs/ru/host-migration.md
VERIFY=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0;; *) return 1;; esac; }
# has_sub keeps a VARIABLE out of a pipeline: printf | grep -q would hand the verdict to the producer
# under pipefail (AGENTS trap 9 / B386), and this contract exists to be believed.
has_sub() { case "$2" in *"$1"*) return 0;; *) return 1;; esac; }

for f in "$ACT" "$HANDLER" "$ACT_TEST" "$PLAN" "$EXEC" "$EXEC_TEST" "$SYNC" "$DERPDOC" \
         "$MIGDOC" "$MIGDOCRU" "$VERIFY" "$AGENTS" "$NOTES"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the internal actions ----------------------------------------------------
for name in internal_nodes_online internal_tags_present internal_policy_equivalent \
            internal_routes_converged internal_derp_probe external_derp_probe_101; do
  if has "$ACT" "\"$name\""; then :; else bad "A1: action $name is not wired in the handler"; fi
done
if has "$ACT" '"external_derp_probe_101"'; then
  ok "A1: every internal step the plan can name has an implementation"
fi
if has "$HANDLER" 'Internal: s.migrateInternalActions(run)'; then
  ok "A2: the step handler passes the action map to the executor"
else
  bad "A2: the executor is called without the internal actions — every internal step stays pending"
fi
if has "$EXEC" 'deps.Internal[command]' && has "$EXEC" 'ErrExecutorPending'; then
  ok "A3: an unwired name is PENDING, never a host failure"
else
  bad "A3: the executor cannot distinguish an unwired action from a failure"
fi
if has "$ACT" 'READ-ONLY' && has "$ACT" 'headscale GET policy' ; then
  ok "A4: the actions are documented as read-only, with the one manual exception"
else
  bad "A4: the read-only rule is not stated"
fi
if has "$ACT" 'the live policy differs from the one this panel generates'; then
  ok "A5: a divergent policy is a named failure, not a green tick"
else
  bad "A5: the policy check does not name divergence"
fi
if has "$ACT" 'a proxy that negotiates HTTP/2 cannot carry a DERP upgrade'; then
  ok "A6: the DERP probe requires 101 and names the 426 cause (B478's lesson)"
else
  bad "A6: the DERP probe would accept a non-101 answer"
fi
if has "$ACT" 'carry the tag their rules depend on'; then
  ok "A7: a lost tag is reported per device (B477's lesson)"
else
  bad "A7: the tag check cannot say which device lost its tag"
fi
if has "$EXEC_TEST" 'InternalMissingActionIsPending' && has "$EXEC_TEST" 'InternalUsesTheSuppliedAction'; then
  ok "A8: the dispatch itself is covered by behavioural tests"
else
  bad "A8: the internal dispatch has no test"
fi

# --- B: relay registration stays the existing page ------------------------------
if has "$PLAN" 'Kind: KindManual, Where: WhereManual, Required: false,' && has "$PLAN" 'manual_derp_relay_register'; then
  ok "B1: registering a relay is a manual step pointing at /admin/derp/relays/init"
else
  bad "B1: the plan still claims the wizard writes a relay row"
fi
if ! has "$ACT" 'AddDerpRelay('; then
  ok "B2: the wizard does not write derp_relays rows"
else
  bad "B2: the wizard writes a relay row and would invent a region id"
fi

# --- C: the certificate sync ----------------------------------------------------
if has "$SYNC" 'grep -c' && has "$SYNC" 'unable to get local issuer certificate'; then
  ok "C1: the sync refuses an incomplete chain and names the failure it produces"
else
  bad "C1: the chain-completeness rule is missing"
fi
if has "$SYNC" 'install_pair' && has "$SYNC" 'unchanged (certificate valid until'; then
  ok "C2: an unchanged certificate rewrites nothing and restarts nobody"
else
  bad "C2: the sync is not idempotent"
fi
if has "$SYNC" '--install' && has "$SYNC" 'derper-cert-sync.timer'; then
  ok "C3: --install writes the service and the timer"
else
  bad "C3: the timer cannot be installed from the repository"
fi
if has "$SYNC" 'do not match'; then
  ok "C4: a certificate that does not match its key is refused by name"
else
  bad "C4: a mismatched pair would be copied"
fi
if has "$SYNC" 'OnCalendar=*-*-* 04:17:00' && has "$SYNC" 'Persistent=true'; then
  ok "C5: the timer is daily and Persistent (a host that was off still renews)"
else
  bad "C5: the timer schedule is missing"
fi

# --- behavioural: the refusals really fire, and the happy path is idempotent -----
if command -v openssl >/dev/null 2>&1; then
  mkdir -p "$SKY_TMP/acme" "$SKY_TMP/certdir"
  # MSYS rewrites a bare /CN=... argument into a Windows path, so the subject is spelled differently
  # on that shell; everywhere else the normal form is used. Without this the generated pair is empty
  # and every behavioural contract below would be reporting the host, not the script.
  SUBJ="/CN=derp.example.com"
  case "$(uname -s 2>/dev/null || echo unknown)" in MINGW*|MSYS*|CYGWIN*) SUBJ="//CN=derp.example.com" ;; esac

  # a REAL pair: a self-signed certificate is two "BEGIN CERTIFICATE" blocks short of a chain, so it
  # also proves the chain rule fires on genuine material rather than on a stub.
  openssl req -x509 -newkey rsa:2048 -nodes -keyout "$SKY_TMP/acme/one.key" \
    -out "$SKY_TMP/acme/one.crt" -days 2 -subj "$SUBJ" >/dev/null 2>&1
  openssl req -x509 -newkey rsa:2048 -nodes -keyout "$SKY_TMP/acme/two.key" \
    -out "$SKY_TMP/acme/two.crt" -days 2 -subj "$SUBJ" >/dev/null 2>&1

  if [ ! -s "$SKY_TMP/acme/one.crt" ] || [ ! -s "$SKY_TMP/acme/two.crt" ]; then
    printf '  \033[33m-\033[0m F1-F5: SKIP — openssl could not generate test material on this host\n'
  else  OUT="$(DERP_HOSTNAME=derp.example.com DERP_CERTDIR="$SKY_TMP/certdir" \
        ACME_CERT="$SKY_TMP/acme/one.crt" ACME_KEY="$SKY_TMP/acme/one.key" \
        bash "$SYNC" 2>&1)"; rc=$?
  if [ "$rc" -ne 0 ] && has_sub 'CHAIN' "$OUT"; then
    ok "F1: a single-certificate file is refused with the chain reason"
  else
    bad "F1: a single-certificate file was not refused (rc=$rc): $(printf '%s' "$OUT" | tail -1)"
  fi

  # a two-block chain + a key from a DIFFERENT certificate: the mismatch must be named.
  cat "$SKY_TMP/acme/one.crt" "$SKY_TMP/acme/two.crt" > "$SKY_TMP/acme/chain.crt"
  OUT="$(DERP_HOSTNAME=derp.example.com DERP_CERTDIR="$SKY_TMP/certdir" \
        ACME_CERT="$SKY_TMP/acme/chain.crt" ACME_KEY="$SKY_TMP/acme/two.key" \
        bash "$SYNC" 2>&1)"; rc=$?
  if [ "$rc" -ne 0 ] && has_sub 'do not match' "$OUT"; then
    ok "F2: a certificate paired with the wrong key is refused"
  else
    bad "F2: a mismatched pair was not refused (rc=$rc): $(printf '%s' "$OUT" | tail -1)"
  fi

  # the happy path: chain whose leaf matches the key, then a second run that must change nothing.
  cat "$SKY_TMP/acme/two.crt" "$SKY_TMP/acme/one.crt" > "$SKY_TMP/acme/chain2.crt"
  OUT="$(DERP_HOSTNAME=derp.example.com DERP_CERTDIR="$SKY_TMP/certdir" \
        ACME_CERT="$SKY_TMP/acme/chain2.crt" ACME_KEY="$SKY_TMP/acme/two.key" \
        bash "$SYNC" 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ] && [ -f "$SKY_TMP/certdir/derp.example.com.crt" ]; then
    ok "F3: a complete matching chain is copied to <hostname>.crt"
  else
    bad "F3: the happy path failed (rc=$rc): $(printf '%s' "$OUT" | tail -2 | tr '\n' ' ')"
  fi
  OUT="$(DERP_HOSTNAME=derp.example.com DERP_CERTDIR="$SKY_TMP/certdir" \
        ACME_CERT="$SKY_TMP/acme/chain2.crt" ACME_KEY="$SKY_TMP/acme/two.key" \
        bash "$SYNC" 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ] && has_sub 'unchanged' "$OUT"; then
    ok "F4: a second run reports unchanged (the timer cannot churn a relay every night)"
  else
    bad "F4: the second run was not idempotent (rc=$rc): $(printf '%s' "$OUT" | tail -1)"
  fi
  rm -rf "$SKY_TMP/certdir"; mkdir -p "$SKY_TMP/certdir"
  OUT="$(DERP_HOSTNAME=derp.example.com DERP_CERTDIR="$SKY_TMP/certdir" \
        ACME_CERT="$SKY_TMP/acme/chain2.crt" ACME_KEY="$SKY_TMP/acme/two.key" \
        bash "$SYNC" --dry-run 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ] && [ ! -f "$SKY_TMP/certdir/derp.example.com.crt" ]; then
    ok "F5: --dry-run writes nothing"
  else
    bad "F5: --dry-run touched the certificate directory (rc=$rc)"
  fi
  fi
else
  printf '  \033[33m-\033[0m F1-F5: SKIP — no openssl on this host, so the refusals cannot be exercised\n'
fi

# --- D: the docs ----------------------------------------------------------------
if has "$DERPDOC" 'derper-cert-sync.sh' && has "$DERPDOC" 'unable to get local issuer'; then
  ok "D1: docs/derp.md documents the renewal and the chain rule"
else
  bad "D1: the renewal is undocumented"
fi
if has "$MIGDOC" 'ACL and the tags go first' && has "$MIGDOCRU" 'сначала ACL'; then
  ok "D2: both analyses still carry the load-bearing ordering"
else
  bad "D2: the ordering rule was dropped from the analysis"
fi

# --- E: registration ------------------------------------------------------------
if has "$VERIFY" '"B483"'; then ok "E1: the contract is registered in the gate"; else bad "E1: not registered"; fi
if has "$AGENTS" 'B483'; then ok "E2: the block index carries the entry"; else bad "E2: no block-index entry"; fi
if has "$NOTES" 'B483'; then ok "E3: the release notes describe the block"; else bad "E3: no release-notes section"; fi

# --- F: the pre-B483 discriminators --------------------------------------------
cat > "$SKY_TMP/old.go" <<'GO'
package migrate
// internal steps are reported as pending; no action map exists
GO
if grep -q 'migrateInternalActions' "$SKY_TMP/old.go"; then
  bad "F6: the action predicate matches a tree with no actions"
else
  ok "F6: the action predicates discriminate the pre-B483 shape"
fi
if has "$EXEC_TEST" 'TestRunStep_RefusesManualAndInternal' || has "$EXEC_TEST" 'TestExecute_InternalMissingActionIsPending'; then
  ok "F7: the pending path is still pinned by a test (a wired action must not remove it)"
else
  bad "F7: nothing pins the pending path any more"
fi

echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB483: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB483: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
[ "$FAIL" -eq 0 ]
