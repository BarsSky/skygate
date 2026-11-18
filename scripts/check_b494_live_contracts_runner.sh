#!/usr/bin/env bash
# B494 (TD-36) — the LIVE contracts need a runner on the host that serves the tailnet.
#
# `verify_pre_deploy.sh` is a BUILD-TIME catalog: every contract that needs a live installation answers
# SKIP there, and CI has no deployment. So the checks that verify the tailnet's real state
# (duplicate headscale users, reconcile audit writes, orphaned node_owner_map rows, tagOwners
# coverage, node attribution, B188.2's live policy read) had to be launched by hand, one per command,
# on the production host — and nobody did it regularly. `scripts/live_gate.sh` is that
# runner: one command, one verdict, and an exit code that can gate a nightly job.
#
# Measured while building it (2026-11-18, on the production host) — three of these are now contracts:
#   * run from a bare directory it reported 15 "FAIL"s for `check_b188_2.sh`, all about SOURCE files
#     that were not there. A live runner must not turn a missing checkout into a product verdict.
#   * it counted a check that printed only a SKIP row and exited 0 as PASS. "Exited cleanly" is not
#     "answered".
#   * `check_b188_2.sh` skipped its live sections with "live database unreachable: psql -h  -p 5432",
#     because the shared DB helper parsed a `sqlite:` DSN with a PostgreSQL parser. Same class as
#     B488/B489: one shape detector, in `lib/live_db.sh`.
#
# Contracts:
#   A. the runner exists, parses, is LF-only and git-TRACKED (trap #11)
#   B. it asks `lib/live_db.sh` for the shape and refuses CLEANLY (exit 2) when there is no install
#   C. it carries the live contracts, gates the checkout-only one, and never greps the source itself
#   D. its verdict distinguishes PASS / SKIP / FAIL by what the check PRINTED, not by rc alone
#   E. its flags work: --json (valid JSON), --strict, --only, and an unknown flag is refused
#   F. B340: scratch files live under mktemp, never a fixed /tmp path
#   G. the shared DB helper knows the native shape (the B188.2 finding), so a SQLite install answers
#   H. the docs tell the operator how to run it, and say it is NOT part of the build gate
#   I. discoverable: registered in the catalog, indexed in AGENTS.md, recorded in the roadmap

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

RUN="scripts/live_gate.sh"
DBLIB="scripts/lib/db_credentials.sh"
OPS="docs/operations.md"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-check.XXXXXX")" || { echo "  FAIL  cannot create a scratch dir"; exit 1; }
trap 'rm -rf "$SKY_TMP"' EXIT

# ═══════════════ A. the file itself ═══════════════
if [ -f "$RUN" ]; then ok "A1: scripts/live_gate.sh exists"; else bad "A1: the runner is missing"; fi
if bash -n "$RUN" 2>/dev/null; then ok "A2: it parses (bash -n)"; else bad "A2: it does not parse"; fi
cr="$(awk 'index($0, "\r") { n++ } END { print n + 0 }' "$RUN" 2>/dev/null)"
[ "${cr:-1}" = "0" ] && ok "A3: LF-only" || bad "A3: it carries CR line endings ($cr)"
if git ls-files --error-unmatch "$RUN" >/dev/null 2>&1; then
    ok "A4: git tracks it (trap #11)"
else
    bad "A4: it is NOT tracked by git — the released tarball would not carry it"
fi

# ═══════════════ B. one shape detector, clean refusal ═══════════════
case "$(cat "$RUN")" in
    *'lib/live_db.sh'*) ok "B1: it asks lib/live_db.sh for the installation shape" ;;
    *) bad "B1: it does not use the shared shape detector" ;;
esac
case "$(cat "$RUN")" in
    *'docker inspect'*) bad "B2: it re-implements the docker probe instead of asking live_kind" ;;
    *) ok "B2: no second docker detector" ;;
esac
out="$(SKYGATE_LIVE_ENV_FILE="$SKY_TMP/no-such.env" bash "$RUN" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ]; then ok "B3: with no installation it exits 2 (never a false green)"; else bad "B3: it exited $rc with no installation, expected 2"; fi
case "$out" in
    *'no live installation'*) ok "B4: and says why, naming the two shapes it looked for" ;;
    *) bad "B4: the refusal does not explain itself: $out" ;;
esac

# ═══════════════ C. the contracts it carries ═══════════════
missing=""
for c in check_b_duplicate_users.sh check_b_reconcile_audit_writes.sh check_b_node_owner_map_orphans.sh \
         check_b_tag_owners.sh check_b_node_attribution.sh check_b188_2.sh; do
    grep -q "$c" "$RUN" 2>/dev/null || missing="$missing $c"
done
case "$missing" in
    '') ok "C1: all six live-state checks are wired" ;;
    *)  bad "C1: missing from the runner:$missing" ;;
esac
case "$(cat "$RUN")" in
    *'[ ! -d internal/acl ]'*) ok "C2: the checkout-only check is gated on the checkout being present (a package dir, never one file)" ;;
    *) bad "C2: the source-grepping check is not gated — outside a checkout it reports missing files as failures" ;;
esac

# ═══════════════ D. the verdict is about what was PRINTED ═══════════════
case "$(cat "$RUN")" in
    *"grep -qE '^  PASS'"*) ok "D1: a PASS row is required before a check counts as PASS" ;;
    *) bad "D1: the verdict treats exit 0 alone as PASS — a check that only SKIPped would look green" ;;
esac
case "$(cat "$RUN")" in
    *"grep -qE '^  FAIL'"*) ok "D2: a FAIL row forces FAIL" ;;
    *) bad "D2: FAIL rows are not consulted" ;;
esac

# ═══════════════ E. flags ═══════════════
jout="$(SKYGATE_LIVE_ENV_FILE="$SKY_TMP/no-such.env" bash "$RUN" --json 2>&1)"
case "$jout" in
    *'"verdict":"no-installation"'*) ok "E1: --json answers with a machine-readable refusal" ;;
    *) bad "E1: --json output is not the documented shape: $jout" ;;
esac
case "$(cat "$RUN")" in
    *'--strict'*) ok "E2: --strict exists (a nightly job can demand every answer)" ;;
    *) bad "E2: no --strict" ;;
esac
case "$(cat "$RUN")" in
    *'--only'*) ok "E3: --only exists" ;;
    *) bad "E3: no --only" ;;
esac
SKYGATE_LIVE_ENV_FILE="$SKY_TMP/no-such.env" bash "$RUN" --bogus >/dev/null 2>&1; rc=$?
[ "$rc" -eq 2 ] && ok "E4: an unknown flag is refused" || bad "E4: an unknown flag exited $rc, expected 2"

# ═══════════════ F. B340 ═══════════════
case "$(cat "$RUN")" in
    *'mktemp -d'*) ok "F1: scratch files live under mktemp" ;;
    *) bad "F1: no mktemp — a fixed /tmp path would be unwritable for the next user (trap #13)" ;;
esac
if grep -qE '/tmp/(skygate-live|live_gate)[^.]*$' "$RUN" 2>/dev/null; then
    bad "F2: it names a FIXED /tmp path"
else
    ok "F2: no fixed /tmp path"
fi

# ═══════════════ G. the DB helper knows the native shape (the B188.2 finding) ═══════════════
case "$(cat "$DBLIB")" in
    *'live_db_sql'*) ok "G1: the live-DB helper delegates to lib/live_db.sh" ;;
    *) bad "G1: the helper still parses every DSN as PostgreSQL — a SQLite install answers 'psql: socket not found'" ;;
esac
case "$(cat "$DBLIB")" in
    *'SKYGATE_LIVE_DB_MODE="native"'*) ok "G2: it has a native mode (SQLite via live_db_sql)" ;;
    *) bad "G2: no native mode in the live-DB helper" ;;
esac

# ═══════════════ H. the docs ═══════════════
case "$(cat "$OPS" 2>/dev/null)" in
    *'live_gate.sh'*) ok "H1: operations.md documents the runner" ;;
    *) bad "H1: the runner is not documented" ;;
esac
case "$(cat "$OPS" 2>/dev/null)" in
    *'NOT part of'*|*'not part of'*) ok "H2: the docs say it is not part of the build gate" ;;
    *) bad "H2: the docs do not draw the build-gate/live-gate distinction" ;;
esac

# ═══════════════ I. discoverability ═══════════════
if grep -q 'check_b494_live_contracts_runner.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "I1: the contract is registered in the catalog"
else
    bad "I1: the contract is not registered"
fi
if grep -q '^\- \*\*B494\*\*' AGENTS.md 2>/dev/null; then
    ok "I2: the AGENTS index carries its line"
else
    bad "I2: no AGENTS index line"
fi
if grep -q 'B494' docs/ROADMAP.md 2>/dev/null; then
    ok "I3: the roadmap records it (TD-36)"
else
    bad "I3: the roadmap does not mention B494"
fi

echo
echo "  B494: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
