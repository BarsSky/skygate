#!/usr/bin/env bash
# ============================================================================
# check_b259_tailscale_toggle.sh — B259 flip Tailscale path via UI
# ============================================================================
# 2026-09-16: v1.5.8+ (B259) — operator reports "I don't have
# access to docker-compose anymore, that file is lost". After
# B258 surfaced "edit docker-compose.yml + restart" as the only
# way to re-enable Tailscale in the container, the operator
# needed a UI-only toggle. B259 adds:
#   1. DB-overridable auth key path (tailscale.auth_key_path
#      in global_settings, with SKYGATE_TS_AUTHKEY_FILE as
#      fallback) — mirrors the existing
#      tailscale.login_server override pattern (B258 sibling).
#   2. "Enable in-container Tailscale" button on
#      /admin/tailscale (shown in the disabled banner) —
#      persists /data/ts/authkey to the DB override, generates
#      a fresh preauth key via headscale, writes it to the
#      file, and starts tailscaled.
#   3. "Disable in-container Tailscale" button on
#      /admin/tailscale (shown in the normal card) — flips
#      back to /dev/null, stops tailscaled if running, and
#      removes the auth key file.
#
# This B-check pins: source shape + DB-overridable resolution
# + new dispatcher cases + handler wiring + template buttons
# + i18n parity + AGENTS.md entry.
# ============================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

PASS=0
FAIL=0
log()  { printf '  %s\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }

SRC="internal/feature/admin/tailscale.go"
TPL="internal/handlers/templates/admin/tailscale.html"
RU="internal/i18n/catalog_tailscale.go"
TEST="internal/feature/admin/tailscale_b259_test.go"

# --- A. DB-overridable path resolution ---
echo "=== A. tailscaleAuthKeyPath resolves DB > env > default ==="
if grep -qF 'const tailscaleAuthKeyPathDBKey = "tailscale.auth_key_path"' "$SRC"; then
  ok "tailscaleAuthKeyPathDBKey constant defined"
else bad "tailscaleAuthKeyPathDBKey constant must be defined"; fi
if grep -qE 'func \(s \*Service\) tailscaleAuthKeyPath\(\) string' "$SRC"; then
  ok "tailscaleAuthKeyPath helper defined"
else bad "tailscaleAuthKeyPath helper must be defined"; fi
if grep -qE 'func \(s \*Service\) tailscaleAuthKeyPathSource\(\) string' "$SRC"; then
  ok "tailscaleAuthKeyPathSource helper defined (for the template source-hint)"
else bad "tailscaleAuthKeyPathSource helper must be defined"; fi

# --- B. handler wiring (enable + disable + dispatcher) ---
echo
echo "=== B. handler wiring ==="
if grep -qF 'func (s *Service) handleTailscaleEnableInContainer' "$SRC"; then
  ok "handleTailscaleEnableInContainer handler defined"
else bad "handleTailscaleEnableInContainer handler must be defined"; fi
if grep -qF 'func (s *Service) handleTailscaleDisableInContainer' "$SRC"; then
  ok "handleTailscaleDisableInContainer handler defined"
else bad "handleTailscaleDisableInContainer handler must be defined"; fi
if grep -qF 'case "enable_in_container":' "$SRC"; then
  ok "dispatcher has 'enable_in_container' case"
else bad "PostAdminTailscale must dispatch 'enable_in_container'"; fi
if grep -qF 'case "disable_in_container":' "$SRC"; then
  ok "dispatcher has 'disable_in_container' case"
else bad "PostAdminTailscale must dispatch 'disable_in_container'"; fi

# --- C. Enable handler does the 3 steps in the right order ---
echo
echo "=== C. Enable handler does the right 3 steps ==="
# The 3 steps are: (1) SetGlobalSetting to /data/ts/authkey,
# (2) generate preauth key + write file, (3) start tailscaled.
# The "FIRST step is the DB write" contract pins that the
# startTailscaled below actually sees the new path.
if grep -qF 'db.SetGlobalSetting(s.dbc(), tailscaleAuthKeyPathDBKey, newPath)' "$SRC"; then
  ok "enable handler persists new path to DB (FIRST step)"
else bad "enable handler must persist new path to DB FIRST"; fi
if grep -qF 'hs.CreatePreauthKeyWithTags(userID' "$SRC"; then
  ok "enable handler generates preauth key via headscale"
else bad "enable handler must generate preauth key via headscale"; fi
if grep -qE 's\.startTailscaled\(\)' "$SRC"; then
  ok "enable handler calls startTailscaled"
else bad "enable handler must call startTailscaled"; fi

# --- D. Disable handler is symmetric ---
echo
echo "=== D. Disable handler stops + persists + cleans up ==="
if grep -qF 'db.SetGlobalSetting(s.dbc(), tailscaleAuthKeyPathDBKey, newPath)' "$SRC"; then
  ok "disable handler persists /dev/null to DB"
else bad "disable handler must persist /dev/null to DB"; fi
if grep -qF 's.stopTailscaled()' "$SRC"; then
  ok "disable handler stops tailscaled if running"
else bad "disable handler must stop tailscaled if running"; fi

# --- E. template renders the two new buttons ---
echo
echo "=== E. template renders Enable/Disable buttons ==="
if grep -qF 'value="enable_in_container"' "$TPL"; then
  ok "template has enable_in_container form"
else bad "template must have enable_in_container form"; fi
if grep -qF 'value="disable_in_container"' "$TPL"; then
  ok "template has disable_in_container form"
else bad "template must have disable_in_container form"; fi

# --- F. i18n keys (RU + EN) ---
echo
echo "=== F. i18n keys (RU + EN) ==="
for k in enable_in_container_btn \
         enable_in_container_confirm \
         disable_in_container_heading \
         disable_in_container_help \
         disable_in_container_btn \
         disable_in_container_confirm; do
  cnt=$(grep -cF "\"tailscale.$k\"" "$RU" 2>/dev/null || echo 0)
  if [ "$cnt" -ge 2 ]; then ok "i18n key present (RU+EN): tailscale.$k"
  else bad "i18n key $k only in $cnt/2 maps (need RU+EN)"; fi
done

# --- G. unit tests ---
echo
echo "=== G. unit tests for B259 ==="
for t in TestTailscaleAuthKeyPath_ResolutionOrder \
         TestTailscaleAuthKeyPathSource_EnvFallback \
         TestTailscaleAuthKeyDisabled_RespectsRealPath \
         TestTailscaleAuthKeyDisabled_RespectsDevNull \
         TestTailscaleAuthKeyDisabled_RespectsDevNullVariants \
         TestEnableInContainerPersistsDBPath \
         TestGenerateAndWriteTailscaleKeyForEnable_B259_DelegatesToFindUserForHostname; do
  if grep -qF "func $t" "$TEST"; then ok "$t exists"
  else bad "$t must exist in $TEST"; fi
done

# --- H. go build + vet + classifier tests ---
echo
echo "=== H. go build + vet + classifier tests ==="
GO="$(command -v go 2>/dev/null || true)"
if [ -z "$GO" ] && [ -x "$REPO_ROOT/scripts/find_go.sh" ]; then
  win_go="$(bash "$REPO_ROOT/scripts/find_go.sh" 2>/dev/null | tr -d '\r' | head -1)"
  if [ -n "$win_go" ]; then GO="$win_go"; fi
fi
if [ -z "$GO" ]; then
  echo "  (skipping H — go binary not found)"
else
  if echo "$GO" | grep -qE '^[A-Z]:'; then
    GO_RUN() { cmd.exe //c "\"$GO\" $*" >/dev/null 2>&1; }
  else
    GO_RUN() { "$GO" "$@" >/dev/null 2>&1; }
  fi
  if GO_RUN build ./internal/feature/admin/...; then ok "go build ./internal/feature/admin/..."
  else bad "go build ./internal/feature/admin/... failed"; fi
  if GO_RUN vet ./internal/feature/admin/...; then ok "go vet ./internal/feature/admin/..."
  else bad "go vet ./internal/feature/admin/... failed"; fi
  if GO_RUN test ./internal/feature/admin/ -run "TestTailscaleAuthKey|TestEnableInContainer|TestGenerateAndWriteTailscaleKeyForEnable"; then
    ok "go test ./internal/feature/admin/ -run B259 tests"
  else
    bad "go test B259 failed"
  fi
fi

# --- I. AGENTS.md B259 catalog entry ---
echo
echo "=== I. AGENTS.md B259 catalog entry ==="
if grep -qF 'B259' "$REPO_ROOT/AGENTS.md"; then ok "AGENTS.md mentions B259"
else bad "AGENTS.md must mention B259 + the UI-toggle rationale"; fi

# --- J. B259.1 fix: delegate to findUserForHostname ---
# Operator 2026-09-17: "раз skygate-host принадлежит infra то
# от лица пользователя infra все и делать — зачем плодить сущности".
# The pre-B259.1 helper looked up the headscale user via inline
# `u.Name == hostname` logic, which forced the operator to
# manually create a phantom `skygate-host` headscale user.
# B259.1 replaces that with findUserForHostname (the canonical
# B251 helper that pins hostname `skygate-host` → user `infra`
# per operator 2026-08-13 directive — uid=85). After B259.1,
# B259's "Включить Tailscale в контейнере" button works against
# the existing infra user with no manual provisioning.
echo
echo "=== J. B259.1 fix: delegates to findUserForHostname ==="
if grep -qF 's.findUserForHostname(context.Background(), hs, hostname)' "$SRC"; then
  ok "generateAndWriteTailscaleKeyForEnable calls findUserForHostname (no phantom user)"
else bad "generateAndWriteTailscaleKeyForEnable must call findUserForHostname (B259.1)"; fi
# Scope the negative checks to the function body — there are
# OTHER hs.ListUsers() calls elsewhere in tailscale.go we don't
# want to break (e.g. the audit-row builders).
awk '/^func \(s \*Service\) generateAndWriteTailscaleKeyForEnable\(/{flag=1} flag{print} /^func \(s \*Service\) handleTailscaleDisableInContainer\(/{flag=0; exit}' "$SRC" > /tmp/b259_body.txt
if grep -qF 'hs.ListUsers()' /tmp/b259_body.txt; then
  bad "generateAndWriteTailscaleKeyForEnable body must NOT call hs.ListUsers() (findUserForHostname is the sole source of truth)"
else
  ok "generateAndWriteTailscaleKeyForEnable body has no hs.ListUsers() (delegated)"
fi
if grep -qF 'strings.TrimSuffix(hostname, "-1")' /tmp/b259_body.txt; then
  bad "generateAndWriteTailscaleKeyForEnable body must NOT contain the legacy u.Name==hostname-or-strip-1 sentinel"
else
  ok "generateAndWriteTailscaleKeyForEnable body has no TrimSuffix(hostname, -1) sentinel (B259.1)"
fi
rm -f /tmp/b259_body.txt
if grep -qF 'B259.1' "$REPO_ROOT/AGENTS.md"; then ok "AGENTS.md mentions B259.1"
else bad "AGENTS.md must mention B259.1 (the findUserForHostname delegation fix)"; fi

# --- K. regression guard: NO other inline hostname→user lookups ---
# B259.1 closed the inline u.Name == hostname lookup in
# generateAndWriteTailscaleKeyForEnable, but a future PR could
# re-introduce the same anti-pattern in a new endpoint
# (e.g. /admin/headscale, /admin/devices, /my/exit-rules). The
# pattern is: "for each u in headscale_users { if u.Name ==
# <some_var_holding_hostname> ... }". This section source-greps
# the entire admin + my feature packages for that shape and
# fails the deploy if any caller outside findUserForHostname
# re-introduces it.
#
# Allowed: the `findUserForHostname` body itself (which is
#   gated by `hostname == "skygate-host"` reserved-name shortcut,
#   NOT a ListUsers + u.Name loop).
# Allowed: u.Name == "infra" / u.Name == <expected_admin> exact-name
#   comparisons (these check for SPECIFIC known names, not for
#   "lookup by hostname" — see infra_owner_sanity.go:107 and
#   users_sync_banner.go:229).
# Disallowed: `u.Name == hostname` or `u.Name == <var>` where the
#   variable holds a tailnet hostname (the lookup-by-name-of-
#   skynet-node anti-pattern).
echo
echo "=== K. regression guard: no inline hostname→user lookups outside findUserForHostname ==="
# Search every .go file under the admin + my feature packages
# for the two patterns the B259 inline lookup relied on. We use
# a targeted pattern that's tight enough to skip legitimate
# `u.Name == "infra"` / `u.Name == expectedAdmin` checks but
# catches `u.Name == hostname` and `u.Name == <varname>` where
# the variable obviously holds a tailnet hostname (skygate-host
# / TailscaleHostname() / SKYGATE_TS_HOSTNAME).
HIT=0
for f in $(find "$REPO_ROOT/internal/feature/admin" "$REPO_ROOT/internal/feature/my" -name '*.go' -not -name '*_test.go'); do
  rel="${f#$REPO_ROOT/}"
  # 1. u.Name == hostname (or vice versa). Skip comment lines
  #    (start with //) — the findUserForHostname doc-comment +
  #    the B259.1 inline doc explicitly contain this pattern as
  #    a "DO NOT do this" warning, not as live code. We only
  #    want to flag actual executable lookups.
  if grep -nE '\.Name\s*==\s*hostname\b|hostname\b\s*==\s*\.Name' "$f" | grep -v '://' | grep -vE '^[0-9]+:\s*//' >/dev/null 2>&1; then
    bad "$rel: inline '.Name == hostname' lookup detected — call findUserForHostname instead (B251/B259.1)"
    HIT=1
  fi
  # 2. u.Name == <var> where var is clearly a hostname (skygate-host,
  #    TailscaleHostname, SKYGATE_TS_HOSTNAME). Same comment-skip rule.
  if grep -nE '\.Name\s*==\s*(skygate-host|TailscaleHostname|SKYGATE_TS_HOSTNAME)' "$f" | grep -vE '^[0-9]+:\s*//' >/dev/null 2>&1; then
    bad "$rel: inline '.Name == <hostname-var>' lookup detected — call findUserForHostname instead (B251/B259.1)"
    HIT=1
  fi
done
if [ "$HIT" = "0" ]; then
  ok "no inline hostname→user lookups found in admin/ + my/ packages"
fi

# --- N. Start must actually work (operator report 2026-09-19) ---
# Clicking Start produced:
#   tailscale up: exit status 1 — output: failed to connect to local tailscaled;
#   it doesn't appear to be running
# Two defects: (1) tailscaled was spawned as
#   setsid nohup tailscaled --statedir=… ">/var/log/tailscaled.log" "2>&1" "&"
# with no shell, so those tokens were ARGV and tailscaled exited at once;
# (2) the readiness wait trusted a stale socket FILE (the run dir is a bind
# mount), so `tailscale up` ran against nothing.
if grep -v '^[[:space:]]*//' "$SRC" | grep -q '">/var/log/tailscaled.log"'; then
  bad "N1: $SRC still passes shell redirection tokens to exec (tailscaled exits immediately)"
else
  ok "N1: no shell redirection tokens in the tailscaled exec (real *os.File instead)"
fi
if grep -q 'net.DialTimeout("unix", tailscaledSocketPath' "$SRC"; then
  ok "N2: daemon readiness dials the control socket (a stale socket file is not 'running')"
else
  bad "N2: readiness does not dial the socket — a stale socket file will be read as 'running'"
fi
if grep -q 'os.Remove(tailscaledSocketPath)' "$SRC"; then
  ok "N3: a stale control socket is removed before the start attempt"
else
  bad "N3: no stale-socket cleanup before starting tailscaled"
fi
if grep -q 'detachProcess(tsCmd)' "$SRC" \
   && grep -q 'func detachProcess' internal/feature/admin/proc_unix.go \
   && grep -q 'func detachProcess' internal/feature/admin/proc_windows.go; then
  ok "N4: tailscaled is detached into its own session (build-tagged helper for Windows)"
else
  bad "N4: detachProcess helper missing (the UI-started daemon must survive the handler)"
fi
if grep -q 'tailLogTail(tsLog' "$SRC"; then
  ok "N5: a failed start reports the tailscaled log tail instead of a bare timeout"
else
  bad "N5: the 'not ready' error does not include tailscaled's own log"
fi

echo
echo "============================================="
printf '  %d passed, %d failed\n' "$PASS" "$FAIL"
echo "============================================="
[ "$FAIL" -eq 0 ]
