#!/usr/bin/env bash
# check_b443_cross_user.sh — B443 (2026-10-29)
#
# «ПРАВИЛА ЗАВЯЗАНЫ НА ПОЛЬЗОВАТЕЛЕЙ И МЕЖДУ ПОЛЬЗОВАТЕЛЯМИ НЕТ ГОНКИ ВНУТРИ EXIT-NODES».
#
# Three things stood between the product and that sentence.
#
# 1. A DEVICE TRANSFER REWROTE THE OWNERSHIP BUT NOT THE RULES. /admin/devices Transfer (and
#    the adoption of an ownerless node) updated only node_owner_map, so the PREVIOUS owner
#    kept every rule of a device he no longer controls — he could still decide where that
#    device's traffic goes — while the new owner saw a device with no rules although it was
#    demonstrably routed. Worse, a transferred rule carrying the `all_devices` marker would
#    be re-materialised by propagateAllDeviceRules onto the NEW owner's devices. The rules
#    now move with the device, the two per-owner markers are cleared, and the count is
#    reported (audit + flash) — silently rewriting somebody else's rules is how this class
#    of defect stays invisible.
# 2. A CLASS TAG COULD BECOME A RULE'S src. `deviceTagForRule` returned whatever
#    node_owner_map held, and a class tag (tag:exit-node, tag:private, tag:server, …) names
#    a ROLE that matches every device carrying it — so one user's rule could grant access on
#    other users' devices. Only a PER-NODE tag may be a rule's src.
# 3. THE NO-RACE CLAIM ITSELF. It is confirmable, not just assertable: the owner assignment
#    is a PURE FUNCTION of the claim set, so it cannot depend on row order, on who
#    submitted first, or on a re-run. Three tests pin exactly that.
#
# CONTRACTS: 16.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

REASSIGN=internal/db/device_rules_reassign_b443.go
REASSIGN_T=internal/db/device_rules_reassign_b443_test.go
DEVICES=internal/feature/admin/devices.go
ACLOWN=internal/acl/acl_ownership.go
ACL_T=internal/acl/acl_b443_test.go
DET=internal/prefixowner/prefixowner_b443_test.go
GATE=scripts/verify_pre_deploy.sh
for f in "$REASSIGN" "$REASSIGN_T" "$DEVICES" "$ACLOWN" "$ACL_T" "$DET" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
printf '  DIAG B443: reassign=%s devices=%s acl=%s\n' "$REASSIGN" "$DEVICES" "$ACLOWN"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b443.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. a transfer moves the RULES with the device"
GOFMT_OUT="$(gofmt -l "$REASSIGN" "$REASSIGN_T" "$ACL_T" "$DET" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the new files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$DEVICES" 'db.ReassignDeviceRules('; then
  ok "A2: the transfer hands the device's rules to the new owner"
else
  bad "A2: the transfer still rewrites only node_owner_map — the old owner keeps the rules"
fi
# The marker has to be cleared, not carried: "all MY devices" is about the OLD owner's set.
if has "$REASSIGN" 'all_devices = 0'; then
  ok "A3: the all-devices marker is cleared on the moved rows"
else
  bad "A3: a moved rule keeps all_devices=1 and would fan out onto the NEW owner's devices"
fi
if has "$REASSIGN" "group_label = ''"; then
  ok "A4: the previous owner's group label is cleared"
else
  bad "A4: the new owner inherits the previous owner's naming"
fi
# Reported, not silent: audit row + the operator's flash sentence.
if has "$DEVICES" 'rules_moved=' && has "$DEVICES" 'rule(s) moved to the new owner'; then
  ok "A5: the audit row and the flash both name how many rules moved"
else
  bad "A5: the rewrite is silent — the operator cannot see that another user's config changed"
fi
if has "$DEVICES" 'device_transfer_rules_failed'; then
  ok "A6: a failed reassignment is audited instead of being swallowed"
else
  bad "A6: a failure would leave the device owned by one user and configured by another, silently"
fi

hdr "B. only a PER-NODE tag may be a rule's src"
if has "$ACLOWN" 'db.IsPerNodeTag(tag)'; then
  ok "B1: deviceTagForRule refuses a class tag"
else
  bad "B1: a class tag can still become a rule src — it matches other users' devices"
fi
if has "$ACL_T" 'TestB443_ClassTagIsNeverARuleSource'; then
  ok "B2: the refusal is pinned by name"
else
  bad "B2: nothing pins the class-tag refusal"
fi

hdr "C. «между пользователями нет гонки» is CONFIRMED, not asserted"
if has "$DET" 'TestB443_AssignmentIsOrderIndependent'; then
  ok "C1: every permutation of the same claims yields the same owner"
else
  bad "C1: the assignment is not proven order-independent"
fi
if has "$DET" 'TestB443_WhoWroteFirstDoesNotMatter'; then
  ok "C2: who submitted first cannot win"
else
  bad "C2: nothing proves the answer is free of arrival order"
fi
if has "$DET" 'TestB443_RepeatedEvaluationIsStable'; then
  ok "C3: a re-run does not move a prefix under the clients"
else
  bad "C3: nothing proves a re-run is stable"
fi

hdr "D. the suites are green"
if ! command -v go >/dev/null 2>&1; then
  skip "D1: no go toolchain on PATH — cannot run TestB443 here (CI is authoritative)"
elif go test ./internal/db ./internal/acl ./internal/prefixowner -run 'TestB443' -count=1 >"$WORK/t.log" 2>&1; then
  ok "D1: TestB443 green in db, acl and prefixowner"
else
  bad "D1: TestB443 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
fi
if ! command -v go >/dev/null 2>&1; then
  skip "D2: no go toolchain on PATH — cannot run the package suites here"
elif go test ./internal/db ./internal/acl ./internal/prefixowner -count=1 >"$WORK/p.log" 2>&1; then
  ok "D2: the three touched packages are green (no B265/B274/B284 regression)"
else
  bad "D2: a package suite failed — $(tail -3 "$WORK/p.log" | tr '\n' ' ')"
fi

hdr "E. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b443_cross_user.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git"
else
  bad "E1: this script is not tracked by git"
fi
if grep -qF 'check_b443_cross_user' "$GATE" 2>/dev/null; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B443"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "E3: the B443 catalog description contains a backtick (TD-15)" ;;
  "")    bad "E3: the B443 catalog entry was not found" ;;
  *)     ok "E3: the B443 catalog description has no backtick (TD-15)" ;;
esac

printf '\n\033[1mB443 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
