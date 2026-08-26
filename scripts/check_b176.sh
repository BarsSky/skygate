#!/bin/bash
# check_b176.sh — B176 (v1.5.2) headscale 0.29 dev-tag lowercase fix.
#
# Operator 2026-08-25 (after B175 shipped):
# "старое отображение информации при навеадении на тег
#  ожидания осталось также не обновил с новым проходом тег
#  обновлятор устройство - не было обновления на VM?
#  Или в чем то другом проблема?"
#
# "The old tooltip text on the pending tag is still there,
#  the new tag-autoupdater pass didn't update the device
#  on VM? Or is there another problem?"
#
# Root cause (B176): headscale 0.29 rejects tags that contain
# uppercase letters ("Error: setting tags: rpc error: tag
# should be lowercase"). Pre-B176 the backfill constructed
# `tag:dev-<user>-<hostname>` from the live headscale
# hostname (e.g. "SkyBars") without lowercasing, so headscale
# silently rejected the AddTag call. The /my/devices UI
# showed the same uppercase dev-tag as "⏳ pending" because
# the live headscale never had the tag.
#
# Node 35 "SkyBars" on the live VM is the canonical repro
# of this bug (verified via `docker exec headscale headscale
# nodes tag -i 35 -t 'tag:private,tag:dev-skyadmin-SkyBars'
# --force` → "Error: tag should be lowercase").
#
# B176 fix: lowercase n.Hostname before constructing
# `tag:dev-<user>-<hostname>` at all 4 call sites
# (internal/nodeownership/nodeownership.go, internal/feature/
# my/devices.go × 2, internal/feature/admin/devices.go). The
# post-fix dev-tag `tag:dev-skyadmin-skybars` matches the
# headscale 0.29 lowercase requirement + the v0.28.0
# `tag:dev-<user>-<device>` convention used elsewhere.
#
# B175.1 fix (same commit, no separate scope): the i18n
# key `devices.dev_tag_pending_help` was rewritten to:
#   - explain that the autoupdater ticks every 5 min (B175)
#   - tell the user to ask the admin if it persists
#   - point at the uppercase-hostname edge case (B176)
# The pre-B175.1 text ("следующий /my/devices повторит
# попытку") was the operator's specific complaint on
# 2026-08-25.
#
# B-check split:
#  A. Source contract (dev-tag construction lowercases
#     n.Hostname at all 4 sites)
#  B. i18n contract (the dev_tag_pending_help tooltip
#     mentions the autoupdater + the B176 fix)
#  C. Build contract (go build + go vet + go test)
set -euo pipefail

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }
skip(){ echo "  SKIP  $1"; }
hdr() { echo; echo "=== $1 ==="; }

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO"

# ---------------------------------------------------------------------------
hdr "contract A: source contract (dev-tag construction lowercases n.Hostname)"

# A.1 — nodeownership.go constructs the dev-tag with
# strings.ToLower(n.Hostname). Pre-B176 used n.Hostname
# directly, which produces an uppercase tag that headscale
# 0.29 rejects.
if grep -qE 'devTag := fmt\.Sprintf\("tag:dev-%s-%s", portalUsername, strings\.ToLower\(n\.Hostname\)\)' internal/nodeownership/nodeownership.go; then
    ok "nodeownership.go lowercases n.Hostname in dev-tag (B176: headscale 0.29 will accept the tag)"
else
    bad "nodeownership.go does NOT lowercase n.Hostname (B176: headscale 0.29 rejects uppercase tags — bug is back)"
fi

# A.2 — feature/my/devices.go (live branch) lowercases.
if grep -qE 'devTag = fmt\.Sprintf\("tag:dev-%s-%s", username, strings\.ToLower\(n\.Hostname\)\)' internal/feature/my/devices.go; then
    live_count=$(grep -c 'devTag = fmt\.Sprintf("tag:dev-%s-%s", username, strings\.ToLower(n.Hostname))' internal/feature/my/devices.go || true)
    if [ "$live_count" -ge 2 ]; then
        ok "feature/my/devices.go lowercases n.Hostname in BOTH devTag sites (live + snapshot branch) — $live_count occurrences"
    else
        bad "feature/my/devices.go only has $live_count lowercased devTag sites (expected 2: live + snapshot)"
    fi
else
    bad "feature/my/devices.go does NOT lowercase n.Hostname in devTag (B176: UI would show 'tag:dev-skyadmin-SkyBars' which headscale never has)"
fi

# A.3 — feature/admin/devices.go (post-transfer dev-tag)
# lowercases. The admin transfer code path is rare but
# uses the same pattern.
if grep -qE 'newDevTag := fmt\.Sprintf\("tag:dev-%s-%s", targetUsername, strings\.ToLower\(liveHostname\)\)' internal/feature/admin/devices.go; then
    ok "feature/admin/devices.go lowercases liveHostname in post-transfer dev-tag (B176: admin reassign won't trigger the uppercase bug)"
else
    bad "feature/admin/devices.go does NOT lowercase liveHostname (B176: admin transfer would create an uppercase dev-tag that headscale rejects)"
fi

# A.4 — No stragglers. There should be NO remaining
# `fmt.Sprintf("tag:dev-%s-%s", ..., [a-zA-Z]Hostname)` (without
# ToLower) anywhere in the codebase. If a future refactor
# adds a new call site without ToLower, the live VM bug
# recurs. The pattern matches n.Hostname + e.deviceHostname
# + e.DeviceHostname + liveHostname (the 4 forms used in
# the codebase).
stragglers=$(grep -rE 'fmt\.Sprintf\("tag:dev-%s-%s",[^)]*[a-zA-Z][a-zA-Z]*[hH]ostname' --include='*.go' internal/ 2>/dev/null | grep -v 'ToLower' | grep -v '_test\.go' | wc -l || true)
if [ "${stragglers:-0}" -eq 0 ]; then
    ok "No uppercase dev-tag stragglers in any *.go file (B176: all sites lowercased — nodeownership.go, devices.go × 2, admin/devices.go, acl.go × 2)"
else
    bad "Found $stragglers dev-tag call site(s) that use [a-zA-Z]Hostname without ToLower (B176: a new uppercase bug)"
    grep -rE 'fmt\.Sprintf\("tag:dev-%s-%s",[^)]*[a-zA-Z][a-zA-Z]*[hH]ostname' --include='*.go' internal/ | grep -v 'ToLower' | grep -v '_test\.go' || true
fi

# A.5 — internal/acl/acl.go is a SEPARATE check because
# the dev-tag there is built from `e.deviceHostname`
# (DB-stored, not the live n.Hostname) and used in the
# headscale policy file. If the rule's tag doesn't match
# the node's actual tag, the rule is silently ignored by
# headscale (same effect as the device tag being missing).
if grep -qE '"tag:dev-"\s*\+\s*e\.userName\s*\+\s*"-"\s*\+\s*strings.ToLower\(e\.deviceHostname\)' internal/acl/acl.go; then
    ok "acl.go (ruleEntry loop) lowercases e.deviceHostname in tag:dev-<user>-<device> construction (B176: ACL policy matches the lowercase node tag)"
else
    bad "acl.go (ruleEntry loop) does NOT lowercase e.deviceHostname (B176: headscale policy has uppercase src, node has lowercase tag → rule never matches)"
fi

if grep -qE '"tag:dev-"\s*\+\s*e\.UserName\s*\+\s*"-"\s*\+\s*strings.ToLower\(e\.DeviceHostname\)' internal/acl/acl.go; then
    ok "acl.go (DeviceRule loop) lowercases e.DeviceHostname in tag:dev-<user>-<device> construction (B176: ACL policy matches the lowercase node tag)"
else
    bad "acl.go (DeviceRule loop) does NOT lowercase e.DeviceHostname (B176: headscale policy has uppercase src, node has lowercase tag → rule never matches)"
fi

# ---------------------------------------------------------------------------
hdr "contract B: i18n contract (tooltip mentions the autoupdater + B176)"

# B.1 — RU text mentions the autoupdater (5 min tick).
if grep -qE 'node-discovery обновляет каждые 5 мин' internal/i18n/catalog_my.go; then
    ok "RU tooltip mentions the 5-min autoupdater (B175.1: 'tag applies automatically — every 5 min')"
else
    bad "RU tooltip does NOT mention the 5-min autoupdater (B175.1: pre-fix text 'следующий /my/devices повторит попытку' is still there)"
fi

# B.2 — RU text mentions uppercase edge case (B176).
if grep -qE 'B176|заглавные буквы' internal/i18n/catalog_my.go; then
    ok "RU tooltip mentions the uppercase-hostname edge case (B175.1: tells user to ask admin if persists)"
else
    bad "RU tooltip does NOT mention the B176 edge case (B175.1: silent failure mode is undocumented)"
fi

# B.3 — EN text mentions the autoupdater.
if grep -qE 'node-discovery autoupdater ticks every 5 min' internal/i18n/catalog_my.go; then
    ok "EN tooltip mentions the 5-min autoupdater (B175.1: 'autoupdater ticks every 5 min')"
else
    bad "EN tooltip does NOT mention the 5-min autoupdater (B175.1 regression)"
fi

# B.4 — EN text mentions B176 fix.
if grep -qE 'B176|uppercase letters' internal/i18n/catalog_my.go; then
    ok "EN tooltip mentions the uppercase-letter edge case (B175.1)"
else
    bad "EN tooltip does NOT mention the B176 edge case (B175.1)"
fi

# B.5 — The pre-B175.1 misleading text is GONE.
if grep -qE 'следующий /my/devices повторит попытку' internal/i18n/catalog_my.go; then
    bad "RU tooltip still has the old 'следующий /my/devices повторит попытку' (B175.1 regression)"
else
    ok "RU pre-B175.1 misleading text is GONE (no more 'следующий /my/devices повторит попытку')"
fi
if grep -qE 'next /my/devices retries' internal/i18n/catalog_my.go; then
    bad "EN tooltip still has the old 'next /my/devices retries' (B175.1 regression)"
else
    ok "EN pre-B175.1 misleading text is GONE (no more 'next /my/devices retries')"
fi

# ---------------------------------------------------------------------------
hdr "contract C: build + vet + test"

# C.1 — go build ./... exits 0.
GO_BIN=""
for cand in "$(command -v go)" \
    "/c/Program Files/Go/bin/go.exe" \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/usr/local/go/bin/go" \
    "/usr/lib/go/bin/go" \
    "/opt/go/bin/go"; do
    if [ -n "$cand" ] && [ -x "$cand" ]; then
        GO_BIN="$cand"
        break
    fi
done
if [ -z "$GO_BIN" ]; then
    skip "go build ./... (go binary not on PATH — skipping)"
elif "$GO_BIN" build ./... 2>/dev/null; then
    ok "go build ./... clean"
else
    bad "go build ./... FAILED"
fi

# C.2 — go vet ./... exits 0.
if [ -z "$GO_BIN" ]; then
    skip "go vet ./... (go binary not on PATH — skipping)"
elif "$GO_BIN" vet ./... 2>/dev/null; then
    ok "go vet ./... clean"
else
    bad "go vet ./... FAILED"
fi

# C.3 — go test ./... passes (no regression in existing tests).
if [ -z "$GO_BIN" ]; then
    skip "go test ./... (go binary not on PATH — skipping)"
elif "$GO_BIN" test ./... 2>/dev/null; then
    ok "go test ./... passes (B176 + B175.1: no test regression)"
else
    bad "go test ./... FAILED"
fi

echo
echo "B176 + B175.1 check OK — the dev-tag construction lowercases n.Hostname at all 4 call sites (so headscale 0.29's 'tag should be lowercase' error no longer happens), and the i18n tooltip on the pending dev-tag tells the user (a) the autoupdater ticks every 5 min, (b) ask admin if it persists > 5 min, (c) the most common cause is uppercase letters in the hostname."
