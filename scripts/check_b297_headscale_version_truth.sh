#!/usr/bin/env bash
# check_b297_headscale_version_truth.sh
#
# 2026-09-23 (B297) — the RUNNING headscale's version must be READ, not assumed.
#
# Live reason: skygate's only answer to "which headscale is this?" was
# SKYGATE_HEADSCALE_VERSION_PIN, an env var the operator types once —
# internal/config/config.go says so out loud ("The pin is an env var (not
# auto-detected) because skygate doesn't shell into the headscale container").
# The two live hosts already disagree (`aro` runs 0.29.0, the agent VM runs
# 0.29.3), and 0.29.x is NOT uniform in the surface skygate depends on:
# `POST /api/v1/node/{id}/approve_routes` is gone from REST at 0.29.1, the REST
# expire path broke at 0.29.2, `grants[]` replaced `acls[]` at 0.29.0-beta.4 and
# 0.29.2 is the version that rejects wildcards in `tagOwners`. A stale pin
# therefore (a) makes "a newer headscale is available" wrong in whichever
# direction it is wrong, (b) marks patch releases as breaking (or the reverse) in
# the headscale_releases history, and (c) is entirely silent on the one page
# whose job is comparing versions.
#
# CONTRACTS
#   A. the probe is a LADDER — authenticated gateway endpoint → root endpoint →
#      CLI — and every failed rung is remembered, so "unknown" names what it tried
#   B. the parsers understand every spelling a real headscale answers with, and
#      refuse to present a non-version as a version
#   C. the monitor prefers the DETECTED version and keeps the declaration for
#      display (the page and the bot read the effective one)
#   D. a FAILED probe keeps the last real detection and records the failure — it
#      never silently hands control back to the declaration
#   E. no comparison or alert still reads the declaration directly
#   F. boot wiring: detect once, re-probe on every tick, log a MISMATCH
#   G. /admin/headscale shows detected + declared + source + mismatch (RU+EN)
#   H. the regression tests exist and pass; this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B297: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

VER=internal/headscale/version_b297.go
VTEST=internal/headscale/version_b297_test.go
MON=internal/headscale_version/monitor.go
MTEST=internal/headscale_version/monitor_b297_test.go
MAIN=cmd/skygate/main.go
ADMIN=internal/feature/admin/headscale.go
TMPL=internal/handlers/templates/admin/headscale.html
I18N=internal/i18n/catalog_admin.go

hdr "B297 — the running headscale's version must be read, not declared"

# --- A: the ladder ------------------------------------------------------------
for c in 'VersionViaAPI = "GET /api/v1/version"' 'VersionViaRoot = "GET /version"' 'VersionViaCLI = "headscale version"'; do
  if grep -qF "$c" "$VER"; then
    ok "A1: the ladder names the rung $c"
  else
    bad "A1: the ladder is missing the rung $c"
  fi
done
api_line=$(grep -n 'versionFromHTTP(ctx, "/api/v1/version", true)' "$VER" | head -1 | cut -d: -f1)
root_line=$(grep -n 'versionFromHTTP(ctx, "/version", false)' "$VER" | head -1 | cut -d: -f1)
cli_line=$(grep -n 'c.versionFromCLI()' "$VER" | head -1 | cut -d: -f1)
if [ -n "$api_line" ] && [ -n "$root_line" ] && [ -n "$cli_line" ] \
   && [ "$api_line" -lt "$root_line" ] && [ "$root_line" -lt "$cli_line" ]; then
  ok "A2: the rungs run in the documented order (API → root → CLI)"
else
  bad "A2: the rung order is wrong (api=${api_line:-none} root=${root_line:-none} cli=${cli_line:-none})"
fi
tried=$(grep -c 'out.Tried = append(out.Tried' "$VER")
if [ "$tried" -ge 3 ]; then
  ok "A3: every failed rung is remembered ($tried sites)"
else
  bad "A3: only $tried failed rung(s) are remembered — 'unknown' cannot name what it tried"
fi
if grep -q 'func (s ServerVersion) Reason() string' "$VER" && grep -q 'strings.Join(s.Tried' "$VER"; then
  ok "A4: a failed probe always carries a reason"
else
  bad "A4: an undetected version has no reason text"
fi
if grep -q 'func (c \*Client) DetectServerVersion(ctx context.Context) ServerVersion {' "$VER"; then
  ok "A5: DetectServerVersion cannot fail the caller (no error return)"
else
  bad "A5: DetectServerVersion returns an error — a probe must never fail a boot"
fi
if grep -q 'case <-time.After(versionProbeTimeout):' "$VER"; then
  ok "A6: the CLI rung is bounded (a hung docker exec cannot hold a boot phase)"
else
  bad "A6: the CLI rung is unbounded"
fi
if grep -q 'var versionCLIRunnerFn' "$VER"; then
  ok "A7: the CLI rung has a test seam"
else
  bad "A7: the CLI rung cannot be driven from a test"
fi

# --- B: the parsers -----------------------------------------------------------
if grep -q '"version", "Version", "server_version", "serverVersion", "headscale_version"' "$VER"; then
  ok "B1: the JSON parser accepts every version-field spelling"
else
  bad "B1: the JSON parser looks for one exact key only"
fi
if grep -q 'maxVersionBodyBytes' "$VER" && grep -q 'len(trimmed) > 200' "$VER"; then
  ok "B2: a long body is never loose-searched (an error page is not a version)"
else
  bad "B2: a long error page can be mined for a version-looking number"
fi
if grep -q 'versionStrict' "$VER" && grep -q 'versionInText' "$VER"; then
  ok "B3: a named field is validated strictly, free text loosely"
else
  bad "B3: the parsers are missing"
fi
if grep -q 'strings.Contains(lower, "server")' "$VER"; then
  ok "B4: the CLI parser prefers the SERVER line over the client binary's version"
else
  bad "B4: a stale local CLI could be reported as the running server version"
fi
if grep -q 'resp.StatusCode < 200 || resp.StatusCode >= 300' "$VER"; then
  ok "B5: a non-2xx answer is a NOTE, never a version"
else
  bad "B5: an error body can be read as the running version"
fi

# --- C: the monitor prefers the detection -------------------------------------
if grep -q 'VersionProbe func(ctx context.Context) (version, via string, err error)' "$MON"; then
  ok "C1: the monitor takes a version probe"
else
  bad "C1: the monitor has no probe seam"
fi
if grep -q 'func (m \*Monitor) effectivePinnedLocked() string' "$MON" && grep -q 'return m.detected' "$MON"; then
  ok "C2: the effective version is the detected one"
else
  bad "C2: the effective version does not prefer the detection"
fi
if grep -q 'func (m \*Monitor) VersionStatus() ServerVersionStatus' "$MON"; then
  ok "C3: the page can read detected + declared + source + mismatch"
else
  bad "C3: the monitor cannot report the version picture"
fi
if grep -q 'st.Mismatch = CompareSemver(m.detected, declared) != 0' "$MON"; then
  ok "C4: the mismatch test is semver-aware (0.29 == 0.29.0 is not a mismatch)"
else
  bad "C4: the mismatch test is textual and would cry wolf"
fi
if grep -q 'return m.Latest, m.UpdateAvailable, m.BreakingAvailable, m.CheckedAt, hist, m.effectivePinnedLocked()' "$MON"; then
  ok "C5: Snapshot() reports the effective version (page + bot)"
else
  bad "C5: Snapshot() still reports the declaration"
fi

# --- D: a failed probe is honest ----------------------------------------------
if grep -q 'm.detectedErr = err.Error()' "$MON"; then
  ok "D1: a failed probe records why"
else
  bad "D1: a failed probe is silent"
fi
if grep -q 'A FAILED probe keeps the last version we did read' "$MON"; then
  ok "D2: the reason a failure keeps the last detection is documented"
else
  bad "D2: the failure policy is undocumented"
fi
if grep -q 'the probe answered no version' "$MON"; then
  ok "D3: an empty answer is a failed detection, not an empty version"
else
  bad "D3: an empty answer could be stored as a version"
fi

# --- E: nothing compares the declaration directly -----------------------------
if grep -q 'IsBreaking(m.Pinned' "$MON" || grep -q 'CompareSemver(r.TagName, m.Pinned)' "$MON"; then
  bad "E1: a comparison still reads the declaration directly"
else
  ok "E1: every comparison goes through the effective version"
fi
if grep -q 'pinned := m.effectivePinned()' "$MON" && grep -q 'breaking := IsBreaking(pinned, r.TagName)' "$MON"; then
  ok "E2: the tick compares against the effective version"
else
  bad "E2: the tick does not compute the effective version"
fi
if grep -q 'if m.Notifier == nil || effective == "" {' "$MON"; then
  ok "E3: alerts no longer REQUIRE the declaration (a detected host alerts too)"
else
  bad "E3: a host with a detection but no pin stays silent"
fi
probe_before=$(grep -n 'm.ProbeVersion(ctx)' "$MON" | head -1 | cut -d: -f1)
poll_line=$(grep -n 'r, err := c.Latest(ctx)' "$MON" | head -1 | cut -d: -f1)
if [ -n "$probe_before" ] && [ -n "$poll_line" ] && [ "$probe_before" -lt "$poll_line" ]; then
  ok "E4: the probe runs before the GitHub poll (an offline host still detects)"
else
  bad "E4: the version refresh is skipped when the GitHub poll fails"
fi
if grep -q 'm.ProbeVersion(ctx)' <(sed -n '/^func (m \*Monitor) Start/,/^}/p' "$MON"); then
  ok "E5: Start probes immediately, so the first render is truthful"
else
  bad "E5: the first render waits for the first 24h tick"
fi

# --- F: boot wiring -----------------------------------------------------------
if grep -q 'hsVer := hs.DetectServerVersion(ctx)' "$MAIN"; then
  ok "F1: the boot path asks the daemon"
else
  bad "F1: the boot path never probes"
fi
if grep -q 'headscale-version: MISMATCH' "$MAIN"; then
  ok "F2: a declaration that disagrees with the daemon is logged"
else
  bad "F2: a stale pin stays silent at boot"
fi
if grep -q 'hsMon.VersionProbe = hs.VersionProbe' "$MAIN" && grep -q 'hsMon.DeclaredPin = cfg.HeadscaleVersionPin' "$MAIN"; then
  ok "F3: the monitor re-probes and keeps the declaration for display"
else
  bad "F3: the monitor is not wired to the probe"
fi
if grep -q 'func (c \*Client) VersionProbe(ctx context.Context) (string, string, error)' "$VER"; then
  ok "F4: the adapter between the probe and the monitor exists"
else
  bad "F4: the monitor has nothing to call"
fi

# --- G: the page --------------------------------------------------------------
if grep -q 'version = s.HeadscaleUpdateMonitor.VersionStatus()' "$ADMIN" && grep -q '"Version":      version,' "$ADMIN"; then
  ok "G1: the handler fills the version status"
else
  bad "G1: the handler does not read the version status"
fi
for pat in '{{if .Version.Mismatch}}' '{{.Version.Detected}}' '{{.Version.Via}}' '{{.Version.Err}}' '{{.Version.Declared}}'; do
  if grep -qF "$pat" "$TMPL"; then
    ok "G2: the template renders $pat"
  else
    bad "G2: the template does not render $pat"
  fi
done
missing=""
for k in headscale_admin.version_title headscale_admin.version_detected_label \
         headscale_admin.version_declared_label headscale_admin.version_declared_help \
         headscale_admin.version_via headscale_admin.version_unknown \
         headscale_admin.version_unknown_help headscale_admin.version_mismatch \
         headscale_admin.version_probe_failed; do
  n=$(grep -c "\"$k\"" "$I18N")
  [ "$n" -eq 2 ] || missing="$missing $k($n)"
done
if [ -z "$missing" ]; then
  ok "G3: every new key exists exactly twice (RU + EN)"
else
  bad "G3: i18n keys missing or unpaired:$missing"
fi
if grep -q 'определяется у запущенного headscale автоматически' "$I18N" \
   && grep -q 'detected from the running headscale automatically' "$I18N"; then
  ok "G4: the old pin help text now says the version is detected automatically (RU+EN)"
else
  bad "G4: the page still claims the version is only what .env says"
fi

# --- H: tests + git -----------------------------------------------------------
for t in "$VTEST" "$MTEST"; do
  if [ -f "$t" ]; then ok "H1: $t exists"; else bad "H1: $t is missing"; fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/headscale/ ./internal/headscale_version/ -run 'B297' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "H2: the B297 tests pass"
  else
    bad "H2: the B297 tests failed:"
    printf '%s\n' "$OUT" | tail -25 | sed 's/^/       /' >&2
  fi
else
  skip "H2: go not on PATH — run the B297 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b297_headscale_version_truth.sh >/dev/null 2>&1; then
  ok "H3: scripts/check_b297_headscale_version_truth.sh is tracked by git"
else
  bad "H3: scripts/check_b297_headscale_version_truth.sh is NOT tracked"
fi

printf '\n\033[1mB297 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
