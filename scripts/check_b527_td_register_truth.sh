#!/usr/bin/env bash
# B527 (2026-11-27): the technical-debt register must stop lying.
#
# The 2026-11-27 audit read docs/ROADMAP.md §7 and found two rows that describe defects the
# project had ALREADY closed: TD-29 (the admin per-rule delete endpoint) was closed by B379,
# and TD-31 (ci_gate turned a short sha into "never tested") by B385. A register that lists
# fixed work as open is worse than no register: it is the list a future reader trusts when
# deciding what to do next, and it made the audit's own answer ("what notes remain?") wrong.
#
# This contract keeps the register honest in the three ways it can drift:
#   A. a row known-closed must SAY so, with the block that closed it;
#   B. every DONE/CLOSED row must name the block (a bare "DONE" cannot be checked);
#   C. the RU register must not disagree with the EN one about which rows are closed.
set -uo pipefail
if [ -f "$(dirname "$0")/../go.mod" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/go.mod" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f go.mod ] || { printf 'B527: cannot locate go.mod from %s\n' "$PWD" >&2; exit 2; }

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

EN=docs/ROADMAP.md
RU=docs/ru/ROADMAP.md
hdr "B527 — the technical-debt register tells the truth"

# --- A: the two rows the audit caught are marked closed, with their closing block -------
A_MISS=""
grep -qE '^\| TD-29 .*B379' "$EN" \
  || A_MISS="${A_MISS}TD-29 is not marked closed by B379 (the endpoint has been routed since B379; B376 E10 now asserts the route)"$'\n'
grep -qE '^\| TD-31 .*B385' "$EN" \
  || A_MISS="${A_MISS}TD-31 is not marked closed by B385 (ci_gate prints its interpreter and resolves the full sha)"$'\n'
if [ -z "$A_MISS" ]; then
  ok "A1: TD-29 and TD-31 are recorded as CLOSED, each naming the block that closed it"
else
  bad "A1: the register still describes closed work as open:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
fi

# --- B: no DONE/CLOSED row without a block reference ------------------------------------
# A "DONE" nobody can verify is a claim, not a fact. Rows marked DONE/CLOSED must name a
# B-block (or a version, for the pre-B-numbering rows), so the reader can check it.
B_MISS=""
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -E 's/^\| (TD-[0-9]+(\/[A-Z0-9.-]+)*) .*/\1/')
  case " $line " in
    *DONE*|*CLOSED*)
      case "$line" in
        *B[0-9]*) : ;;                      # the block that closed it
        *v[0-9]*) : ;;                      # or the release it shipped in
        *20[0-9][0-9]-[0-9][0-9]-[0-9][0-9]*) : ;;   # or the dated review that closed it
        *"no closing block"*) : ;;          # or an explicit waiver: provenance unknown, said out loud
        *) B_MISS="${B_MISS}${id}: marked closed but names no block, version, date or waiver"$'\n' ;;
      esac ;;
  esac
done < <(grep -E '^\| TD-' "$EN" || true)
if [ -z "$B_MISS" ]; then
  ok "B1: every closed row names the block or version that closed it"
else
  bad "B1: closed rows that cannot be checked:"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi

# --- C: the RU register agrees about which rows are closed ------------------------------
# docs/ru/ is a mirror, not a translation of convenience: if the RU table says OPEN while
# the EN table says DONE, a Russian-speaking operator reads the opposite of the truth.
C_MISS=""
for id in TD-29 TD-31; do
  en_line=$(grep -E "^\| $id " "$EN" | head -1)
  ru_line=$(grep -E "^\| $id " "$RU" | head -1)
  [ -n "$ru_line" ] || { C_MISS="${C_MISS}${id} is missing from the RU register"$'\n'; continue; }
  case "$en_line" in
    *DONE*|*CLOSED*)
      case "$ru_line" in
        *ЗАКРЫТ*|*DONE*|*CLOSED*|*B379*|*B385*) : ;;
        *) C_MISS="${C_MISS}${id}: EN says closed, RU does not"$'\n' ;;
      esac ;;
  esac
done
if [ -z "$C_MISS" ]; then
  ok "C1: both registers agree that the corrected rows are closed"
else
  bad "C1: the RU register disagrees with the EN one:"
  printf '%s' "$C_MISS" | sed 's/^/       /' >&2
fi

# --- D: structural sanity ---------------------------------------------------------------
D_MISS=""
rows=$(grep -cE '^\| TD-' "$EN" || true)
[ "${rows:-0}" -ge 25 ] || D_MISS="${D_MISS}the EN register has only ${rows:-0} rows (a shrunk register hides debt)"$'\n'
dups=$(grep -oE '^\| TD-[0-9]+' "$EN" | sort | uniq -d)
[ -z "$dups" ] || D_MISS="${D_MISS}duplicate TD id(s): $dups"$'\n'
if [ -z "$D_MISS" ]; then
  ok "D1: $rows rows, no duplicate ids"
else
  bad "D1: the register structure is wrong:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
fi

# --- E: bookkeeping ---------------------------------------------------------------------
E_MISS=""
git ls-files --error-unmatch scripts/check_b527_td_register_truth.sh >/dev/null 2>&1 \
  || E_MISS="${E_MISS}this script is NOT tracked by git"$'\n'
grep -q 'check_b527_td_register_truth.sh' scripts/verify_pre_deploy.sh \
  || E_MISS="${E_MISS}verify_pre_deploy.sh does not register B527"$'\n'
grep -q '\*\*B527\*\*' AGENTS.md \
  || E_MISS="${E_MISS}AGENTS.md's block index has no B527 entry"$'\n'
if [ -z "$E_MISS" ]; then
  ok "E1: tracked, registered and indexed"
else
  bad "E1: the block bookkeeping is incomplete:"
  printf '%s' "$E_MISS" | sed 's/^/       /' >&2
fi

hdr "B527 summary"
printf 'B527: %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
