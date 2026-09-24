#!/usr/bin/env bash
# check_b320_tailscale_self_name.sh
#
# 2026-09-24 (B320, v1.5.85) — the canonical tailnet name must belong to the LIVE
# client, and the automated repair may only ever delete an offline ghost.
#
# OPERATOR REPORT (verbatim):
#
#   «Посмотри не переприменился ли skygate-host - он offline и это tailscale клиент при
#    skygate при этом появился skygate-host-1 и если это тоже клиент tailscale при
#    skygate то у нас явно конфликт так как при skygate устройство с tailscale всегда
#    базово имеет имя skygate-host и skygate-host-1 появляется только у тех что
#    дублируют текущий в кластере …»
#
# WHAT THE LIVE TOOLS FOUND (read-only, agent VM):
#
#   headscale: id=57 given_name="skygate-host" name="skygate-host-1-1"
#              tag:dev-infra-skygate-host 100.64.0.9  OFFLINE since 2026-09-21 21:32 (machine key #1)
#              id=87 given_name="skygate-host-1" name="skygate-host-1"
#              tag:dev-infra-skygate-host-1 100.64.0.10 ONLINE, this container (machine key #2)
#   client:    tailscale status --json → .Self.HostName = "skygate-host-1"
#   config:    SKYGATE_TS_HOSTNAME=skygate-host-1 in BOTH .env and docker-compose.yml
#
# So the canonical name is held by a GHOST (an earlier registration with a different
# machine key, offline for days) and the live client wears a suffix. The `-1` is not a
# re-application of anything: it is the v0.33.1.9 placeholder that B251 fixed in the
# code (default `skygate-host`, matched by STRICT equality in `isInfraNode` /
# `isReservedSelfHostname`) but which nobody removed from the operator's files — which
# is why every "is this me?" check (infra ownership, colocation sanity, the SSH-source
# ACL, the self-probes) was keying off the ghost.
#
# CONTRACTS
#   A. the canonical name is a named constant, not a literal sprinkled around
#   B. the delete candidate is guarded: canonical name, OFFLINE, infra family, never live
#   C. the infra-family matcher understands the REAL tag shapes (`tag:dev-infra-…`)
#   D. the page carries the conflict and renders it with both node ids + the action
#   E. the action is routed and audited, and it never renames behind the operator's back
#   F. i18n RU+EN pairs
#   G. tests exist and pass; this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B320: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

NAME=internal/feature/admin/tailscale_selfname_b320.go
TS=internal/feature/admin/tailscale.go
TPL=internal/handlers/templates/admin/tailscale.html
MAIN=cmd/skygate/main.go
I18N=internal/i18n/catalog_tailscale.go
TEST=internal/feature/admin/tailscale_selfname_b320_test.go

hdr "B320 — the canonical name must belong to the live client"

# --- A: the canonical name is one constant -------------------------------------------
if grep -q 'ReservedSelfHostname = "skygate-host"' "$NAME"; then
  ok "A1: the canonical name is a named constant"
else
  bad "A1: the canonical name is not centralised"
fi
if grep -q 'NeedsName = st.Live != "" && !strings.EqualFold(st.Live, st.Canonical)' "$NAME"; then
  ok "A2: a suffixed live name is detected (case-insensitively, like headscale)"
else
  bad "A2: the live-vs-canonical comparison is missing"
fi
if grep -q 'EnvPinned' "$NAME" && grep -q 'SKYGATE_TS_HOSTNAME' "$NAME"; then
  ok "A3: the page can name the config pin that would re-apply the suffix"
else
  bad "A3: the env pin is not surfaced"
fi

# --- B: the guards on the destructive half -------------------------------------------
if grep -q 'func pickSelfNameGhost(nodes \[\]headscale.NodeView, live, canonical string)' "$NAME"; then
  ok "B1: the candidate picker is a pure function (testable without headscale)"
else
  bad "B1: the candidate picker is not separable"
fi
for guard in 'if n.Online {' 'if live != "" && strings.EqualFold(name, live) {' 'if !isInfraFamilyNode(n) {'; do
  if grep -qF "$guard" "$NAME"; then
    ok "B2: guard present: $guard"
  else
    bad "B2: missing guard: $guard"
  fi
done
if grep -q 'if !strings.EqualFold(name, canonical) {' "$NAME"; then
  ok "B3: only a node wearing the CANONICAL name is a candidate (strict, B251)"
else
  bad "B3: the name check is not strict equality"
fi
if grep -q 'action=delete_ghost' "$NAME" && grep -q '"tailscale.reclaim_name"' "$NAME"; then
  ok "B4: the deletion is audited with the ghost's identity"
else
  bad "B4: the deletion is not audited"
fi

# --- C: the real tag shapes ----------------------------------------------------------
if grep -q 'strings.Contains(lt, "-infra-")' "$NAME"; then
  ok "C1: the matcher understands tag:dev-infra-<host> (the REAL shape)"
else
  bad "C1: the matcher does not match tag:dev-infra-… (a ':infra-' check matches NOTHING)"
fi
if grep -q 'strings.HasPrefix(lt, "tag:infra")' "$NAME"; then
  ok "C2: the legacy tag:infra-<host> form is covered too"
else
  bad "C2: the legacy tag form is not covered"
fi

# --- D: the page ---------------------------------------------------------------------
if grep -q 'SelfName SelfNameState' "$TS" && grep -q 'st.SelfName = s.selfNameState(tailscaleSelfHostname())' "$TS"; then
  ok "D1: the Tailscale page computes the self-name state"
else
  bad "D1: the state is not wired into the page"
fi
if grep -q '.State.SelfName.Conflict' "$TPL" && grep -q '.State.SelfName.GhostID' "$TPL" \
   && grep -q '.State.SelfName.Live' "$TPL"; then
  ok "D2: the banner names the live name AND the ghost"
else
  bad "D2: the template does not render the conflict"
fi
if grep -q '/admin/tailscale/reclaim-name' "$TPL"; then
  ok "D3: the action has a form"
else
  bad "D3: the action has no UI"
fi
if grep -q 'tailscale set --hostname=' "$TPL"; then
  ok "D4: the page prints the rename command (what the operator may prefer to run by hand)"
else
  bad "D4: the rename command is not shown"
fi

# --- E: routing + honesty ------------------------------------------------------------
if grep -q 'POST /admin/tailscale/reclaim-name' "$MAIN"; then
  ok "E1: the route is registered"
else
  bad "E1: the route is missing"
fi
if grep -q 'renameSelfClient(ctx, ReservedSelfHostname)' "$NAME"; then
  ok "E2: the running client is renamed only AFTER the ghost is gone"
else
  bad "E2: the rename is not sequenced after the delete"
fi
if grep -q 'the env still pins the old name' "$NAME" || grep -q 'the rename itself needs the daemon' "$NAME" \
   || grep -q 'not running — press Start' "$NAME"; then
  ok "E3: the handler says when it cannot finish the job instead of pretending"
else
  bad "E3: the handler silently half-fixes"
fi

# --- F: i18n -------------------------------------------------------------------------
MISSING=""
for k in tailscale.name_conflict_title tailscale.name_conflict_live tailscale.name_conflict_running \
         tailscale.name_conflict_canonical tailscale.name_conflict_ghost tailscale.name_conflict_seen \
         tailscale.name_conflict_env tailscale.name_conflict_help tailscale.name_conflict_btn \
         tailscale.name_conflict_confirm; do
  n=$(grep -c "\"$k\"" "$I18N")
  if [ "$n" -lt 2 ]; then MISSING="$MISSING $k($n)"; fi
done
if [ -z "$MISSING" ]; then
  ok "F1: every B320 key is defined in BOTH catalogues (rule 10)"
else
  bad "F1: keys missing from one side (count in parentheses):$MISSING"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/i18n/ -run TestCatalogsParity -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "F2: the i18n parity test passes"
  else
    bad "F2: i18n parity failed:"
    printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "F2: go not on PATH — run the i18n parity test on the VM"
fi

# --- G: tests + git ------------------------------------------------------------------
if [ -f "$TEST" ]; then ok "G1: $TEST exists"; else bad "G1: the B320 tests are missing"; fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/admin/ -run 'B320' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "G2: the B320 tests pass"
  else
    bad "G2: the B320 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go vet ./internal/feature/admin/ ./cmd/skygate/ 2>&1)"
  if [ -z "$OUT" ]; then
    ok "G3: go vet is clean"
  else
    bad "G3: go vet reported:"
    printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "G2/G3: go not on PATH — run the B320 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b320_tailscale_self_name.sh >/dev/null 2>&1; then
  ok "G4: scripts/check_b320_tailscale_self_name.sh is tracked by git"
else
  bad "G4: scripts/check_b320_tailscale_self_name.sh is NOT tracked (AGENTS trap #11)"
fi

printf '\n\033[1mB320 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
