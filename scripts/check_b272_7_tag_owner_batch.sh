#!/usr/bin/env bash
# check_b272_7_tag_owner_batch.sh
#
# 2026-09-20 (B272.7, v1.5.35) — the reconciler must permit EVERY tag it is
# about to apply in ONE policy write.
#
# Live case (native host `aro`, right after the privileged policy helper was
# finally installed on v1.5.31):
#
#   tag-reconcile: checked=4 applied=1 failed=2
#   400 requested tags [tag:dev-daniil-laptop] are invalid or not permitted
#   $ grep -c 'tag:dev-' /etc/headscale/policy.hujson
#   1
#
# Three devices needed three dev-tags, one landed. Root cause: EnsureTagOwner is
# a read-modify-write of the WHOLE policy and the reconciler called it once per
# device; on a `policy.mode: file` host the write is performed ASYNCHRONOUSLY by
# `skygate-policy.path` (write the file + restart headscale). Call N read the
# policy before the applier had written call N-1's result, so every write after
# the first started from the same stale snapshot and dropped its predecessor's
# tag. Retry logic cannot fix that — the lost write SUCCEEDED. Only removing the
# interleaving can: one combined write per pass.
#
# CONTRACTS
#   A  headscale client: EnsureTagOwners (batch) is the single write path
#   B  the per-tag entry point delegates to the batch (one code path)
#   C  the reconciler computes the pending set first and writes once
#   D  the batch keeps the retry/fallback semantics (transient-only retry,
#      per-device failure reporting when the single write fails)
#   E  Go behaviour contracts (three devices → one write, empty request → no
#      write, validation before write, one write for many tags against a stub)
#   F  live state on this host (SKIPs when the service/journal is unavailable)
#   G  the contract script itself is tracked by git (trap #11: .gitignore can
#      eat a new script silently)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B272.7: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

TAGS=internal/headscale/tags.go
AUTO=internal/nodeownership/auto.go
BATCH_TEST=internal/nodeownership/auto_b272_7_batch_test.go
HS_TEST=internal/headscale/tags_b272_7_batch_test.go

hdr "B272.7 — one tagOwners policy write per reconcile pass"

# --- A: the batch write path exists -----------------------------------------
if grep -q 'func (c \*Client) EnsureTagOwners(wants map\[string\]\[\]string) error' "$TAGS"; then
  ok "A1: EnsureTagOwners(map[string][]string) exists on the headscale client"
else
  bad "A1: EnsureTagOwners is missing — the reconciler cannot batch its policy writes"
fi
if grep -q 'func (c \*Client) loadPolicyMap(op string) (map\[string\]interface{}, error)' "$TAGS" \
   && grep -q 'func policyTagOwners(p map\[string\]interface{}, op string) (map\[string\]interface{}, error)' "$TAGS"; then
  ok "A2: the read/parse half is shared (loadPolicyMap + policyTagOwners), so both paths parse identically"
else
  bad "A2: the policy read/parse half is not factored out — the two paths can drift"
fi
if grep -q 'hujson.Standardize' "$TAGS" && grep -q 'unquotePolicyIfStringified' "$TAGS" \
   && grep -q 'func (c \*Client) loadPolicyMap' "$TAGS"; then
  ok "A3: the B251 HuJSON + stringified-policy handling survives the refactor (inside the shared loader)"
else
  bad "A3: the HuJSON/stringified-policy handling was lost — headscale 0.29 policies would stop parsing"
fi
# Exactly one SetPolicy call in the file: a second one would be a per-tag write
# path that could race the applier again.
SETPOLICY_CALLS="$(grep -c 'c\.SetPolicy(' "$TAGS")"
if [ "$SETPOLICY_CALLS" = "1" ]; then
  ok "A4: exactly one SetPolicy call in tags.go (one write path, nothing to interleave)"
else
  bad "A4: $SETPOLICY_CALLS SetPolicy calls in tags.go, want 1 — a per-tag write path can race the asynchronous applier"
fi
if grep -q 'sort.Strings(tags)' "$TAGS"; then
  ok "A5: validation runs in a deterministic order (stable error text + stable tag list)"
else
  bad "A5: the batch does not sort its input — the reported error and log line would be non-deterministic"
fi

# --- B: one code path for the single-tag callers -----------------------------
if grep -q 'return c.EnsureTagOwners(map\[string\]\[\]string{tag: owners})' "$TAGS"; then
  ok "B1: EnsureTagOwner delegates to EnsureTagOwners (adopt/transfer paths share the batch writer)"
else
  bad "B1: EnsureTagOwner kept its own copy of the write logic — the two can disagree"
fi

# --- C: the reconciler writes once ------------------------------------------
if grep -q 'EnsureTagOwners(wants map\[string\]\[\]string) error' "$AUTO"; then
  ok "C1: nodeLister requires the batch method (so a fake cannot silently skip it)"
else
  bad "C1: nodeLister does not require EnsureTagOwners"
fi
if grep -q 'func ensureTagOwnersBatch(' "$AUTO" && grep -q 'func ensureTagOwnersRetry(' "$AUTO"; then
  ok "C2: ensureTagOwnersBatch + ensureTagOwnersRetry exist in the reconciler"
else
  bad "C2: the reconciler has no batch pre-pass"
fi
BATCH_CALLS="$(grep -c 'ensureTagOwnersBatch(hs, rows, byID, baseDomain, ensured)' "$AUTO")"
if [ "$BATCH_CALLS" = "1" ]; then
  ok "C3: the batch is computed once per pass from the same rows/nodes the loop uses"
else
  bad "C3: $BATCH_CALLS batch invocations, want exactly 1 per pass"
fi
# The pre-pass must run BEFORE the first AddTag, otherwise the tags it permits
# arrive too late for the tick that needed them.
BATCH_LINE="$(grep -n 'ensureTagOwnersBatch(hs, rows, byID, baseDomain, ensured)' "$AUTO" | head -1 | cut -d: -f1)"
LOOP_LINE="$(grep -n 'for _, r := range rows {' "$AUTO" | head -1 | cut -d: -f1)"
if [ -n "$BATCH_LINE" ] && [ -n "$LOOP_LINE" ] && [ "$BATCH_LINE" -lt "$LOOP_LINE" ]; then
  ok "C4: the batch runs before the apply loop (line $BATCH_LINE < $LOOP_LINE)"
else
  bad "C4: the batch pre-pass does not precede the apply loop (batch=$BATCH_LINE loop=$LOOP_LINE)"
fi
# The set must be computed with the SAME predicates the loop uses, or the batch
# permits tags nothing applies (policy drift) or misses tags it does apply.
if grep -q 'func tagOwnersFor(row db.NodeOwner, baseDomain string) (\[\]string, error)' "$AUTO" \
   && [ "$(grep -c 'tagOwnersFor(' "$AUTO")" -ge 2 ]; then
  ok "C5: tagOwnersFor is shared by the batch and the per-tag path (one owner rule)"
else
  bad "C5: the owner derivation is duplicated between the batch and the per-tag path"
fi

# --- D: retry + fallback semantics survive ----------------------------------
if grep -A12 'func ensureTagOwnersRetry(' "$AUTO" | grep -q 'isTransientHeadscaleDown(err)'; then
  ok "D1: the batch is retried only for the transient class (headscale restarting after a policy write)"
else
  bad "D1: the batch retry does not consult isTransientHeadscaleDown — it would retry a permission refusal"
fi
if grep -q 'falling back to one policy write per tag' "$AUTO"; then
  ok "D2: a failed batch is logged and falls back to the per-tag path (per-device failure reporting)"
else
  bad "D2: a failed batch gives no fallback and no named reason"
fi
if grep -q 'tag-reconcile: permitted %d tag(s) in one policy write' "$AUTO"; then
  ok "D3: a successful batch is logged with its tag list — the operator can see one write, not N"
else
  bad "D3: the batch write is not logged — the operator cannot tell batching apart from silence"
fi
if grep -q 'if len(wants) == 0 {' "$AUTO" && grep -A3 'if len(wants) == 0 {' "$AUTO" | grep -q 'return nil'; then
  ok "D4: an empty pending set issues no write (a needless file-mode write restarts headscale)"
else
  bad "D4: an empty pending set would trigger a policy write"
fi
# The batch concentrates the restart into the tick that applies the tags, so the
# APPLY call is exposed to the same transient window as the permit call.
if grep -q 'func addTagRetry(hs nodeLister, nodeID int64, tag string) error' "$AUTO" \
   && grep -A10 'func addTagRetry(' "$AUTO" | grep -q 'isTransientHeadscaleDown(err)'; then
  ok "D5: the apply call retries the transient window too (the batch puts the restart in the same tick)"
else
  bad "D5: AddTag has no transient retry — the tick that permitted the tags can lose every apply to the restart it caused"
fi
if grep -q 'addTagRetry(hs, id, r.Tag)' "$AUTO" && ! grep -q 'if err := hs.AddTag(id, r.Tag); err != nil' "$AUTO"; then
  ok "D6: the reconciler applies through addTagRetry (no direct AddTag left in the pass)"
else
  bad "D6: the reconciler still calls AddTag directly, bypassing the retry"
fi

# --- E: behaviour ------------------------------------------------------------
if [ -f "$BATCH_TEST" ] && grep -q 'func TestB2727_ThreeDevicesOnePolicyWrite' "$BATCH_TEST"; then
  ok "E1: the three-devices-one-tag regression test exists (the aro symptom, reproduced)"
else
  bad "E1: $BATCH_TEST does not reproduce the aro case"
fi
if [ -f "$HS_TEST" ] && grep -q 'func TestEnsureTagOwners_OneWriteForManyTags' "$HS_TEST"; then
  ok "E2: the client-level contract (three missing tags → one PUT) exists"
else
  bad "E2: $HS_TEST does not pin the one-write-per-batch contract"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test -count=1 -run 'B2727|EnsureTagOwner' ./internal/nodeownership/ ./internal/headscale/ 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q '^FAIL' <<< "$OUT"; then
    ok "E3: the batch contracts pass (three devices → one write; batch failure still reports per device)"
  else
    bad "E3: the batch contracts failed: $OUT"
  fi
else
  skip "E3: go not on PATH"
fi

# --- F: live state (operator confirmation after the deploy) ------------------
# After v1.5.35 is deployed, the journal must show ONE batch line listing the
# tags that were pending, instead of a write per device. SKIPs on a host with no
# running service (Windows dev box, CI).
if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet skygate 2>/dev/null; then
  if command -v journalctl >/dev/null 2>&1; then
    BATCH_LOG="$(journalctl -u skygate --since '-24h' --no-pager 2>/dev/null | grep -c 'permitted .* tag(s) in one policy write' || true)"
    if [ "${BATCH_LOG:-0}" -ge 1 ]; then
      ok "F1: the running service has logged a batched tagOwners write ($BATCH_LOG occurrence(s) in 24h)"
    else
      skip "F1: no batched write in the last 24h — normal when nothing drifted (check the tag-reconcile summary instead)"
    fi
    POLICY="${SKYGATE_HEADSCALE_POLICY_PATH:-/etc/headscale/policy.hujson}"
    if [ -r "$POLICY" ]; then
      TAGGED="$(grep -c 'tag:dev-' "$POLICY" 2>/dev/null || true)"
      ok "F2: policy $POLICY lists ${TAGGED:-0} tag:dev- entr(y/ies) — compare against node_owner_map rows"
    else
      skip "F2: $POLICY is not readable from this user (expected on a native install — run the check as the service user or root)"
    fi
  else
    skip "F1/F2: journalctl is unavailable"
  fi
else
  skip "F1/F2: skygate is not running as a systemd service on this host (live checks belong on the VM)"
fi

# --- G: the script must be TRACKED by git (trap #11) -------------------------
if git ls-files --error-unmatch scripts/check_b272_7_tag_owner_batch.sh >/dev/null 2>&1; then
  ok "G1: scripts/check_b272_7_tag_owner_batch.sh is tracked by git (.gitignore trap #11)"
else
  bad "G1: scripts/check_b272_7_tag_owner_batch.sh is NOT tracked by git — .gitignore can eat it silently"
fi

printf '\n\033[1mB272.7 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
