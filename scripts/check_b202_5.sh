#!/usr/bin/env bash
# B202.5 (v1.5.0+) — SSHDumpTransport for cross-host DB
# migrations.
#
# Phase 1.4 of docs/internal/cluster-management.md, the
# cross-host counterpart to the B202 LocalDumpTransport.
# Closes the "operator must hand-migrate the DB via
# scp + pg_restore on the agent" gap that was implicit
# in the B198/B202 work — pre-B202.5 the dbmigrate
# framework's Dump step only ran pg_dump on the local
# host, which works for the live svi→agent move because
# the agent reaches svi's PG via the 172.17.0.1:5433
# bridge, but the bridge requires svi to expose its PG
# port to the agent network. B202.5 adds a transport
# that runs `ssh svi "pg_dump ..."` and streams the
# bytes back, so the operator can flip the DSN + restart
# the agent without depending on direct PG-port
# reachability between svi and agent.
#
# The contracts:
#
#   1. internal/dbmigrate/ssh_transport.go exists
#   2. SSHDumpTransport struct declared (5 fields:
#      SSHHost, SSHUser, SSHKeyPath, SSHPort, PgDumpPath
#      + optional SSHOptions slice)
#   3. SSHDumpTransport.Name() == "ssh" (the identifier
#      persisted in audit_log + SSE)
#   4. SSHDumpTransport.Dump(ctx, sourceDSN, destPath,
#      onLog) (int64, error) — the DumpTransport contract
#   5. NewSSHDumpTransportFromEnv() — reads 5 env vars:
#      SKYGATE_DBMIGRATE_SSH_HOST, _USER, _KEY, _PORT,
#      _PGDUMP. Returns nil if HOST or USER is empty
#      (caller falls back to LocalDumpTransport).
#   6. quoteForRemoteShell() — POSIX-shell-escapes the
#      DSN for `ssh host 'cmd'` (handles embedded single
#      quotes via the close-quote / literal / reopen idiom)
#   7. framework.go default-fallback: if
#      SKYGATE_DBMIGRATE_TRANSPORT=ssh and the SSH config
#      is valid → SSHDumpTransport, else LocalDumpTransport
#   8. ssh_transport_test.go has 5 tests: QuoteForRemoteShell
#      (4 sub-cases), NewFromEnv_RequiresHostAndUser,
#      NewFromEnv_PortParsing, Dump_FakeSsh (Unix only,
#      SKIP on Windows because exec.LookPath doesn't
#      find a bare 'ssh' on Windows), Dump_Validation
#   9. Dump_FakeSsh exists (proves the round-trip
#      stdout→dest + stderr→onLog wiring works, even
#      if SKIP on Windows)
#  10. Compile-time assertion: SSHDumpTransport implements
#      DumpTransport (var _ DumpTransport = SSHDumpTransport{})
#  11. Build + vet + dbmigrate tests pass
#  12. AGENTS.md mentions B202.5
#  13. verify_pre_deploy.sh has a B202.5 run_check
#  14. scripts/b202_5_verify.sh exists (live-verify dry-run
#      on the agent — SSH to localhost, prove the transport's
#      exec.CommandContext wiring works without touching
#      headscale/headplane on the agent or the live
#      skygate_staging DB)

set -u

if [ -n "${SKYGATE_PROJECT_DIR:-}" ]; then
  cd "$SKYGATE_PROJECT_DIR"
else
  cd "$(dirname "$0")/.."
fi

PASS=0
FAIL=0
fails=()

check() {
  local name="$1"
  local result="$2"
  if [ "$result" = "ok" ]; then
    printf "  \033[32m✓\033[0m %s\n" "$name"
    PASS=$((PASS+1))
  else
    printf "  \033[31m✗\033[0m %s\n" "$name"
    FAIL=$((FAIL+1))
    fails+=("$name")
  fi
}

file_exists() { [ -f "$1" ]; }
file_grep() { grep -qE "$1" "$2" 2>/dev/null; return $?; }

# 1. ssh_transport.go exists
file_exists "internal/dbmigrate/ssh_transport.go" \
  && check "internal/dbmigrate/ssh_transport.go exists" ok \
  || check "internal/dbmigrate/ssh_transport.go exists" fail

# 2. SSHDumpTransport struct
if file_exists "internal/dbmigrate/ssh_transport.go"; then
  file_grep "^type SSHDumpTransport struct" "internal/dbmigrate/ssh_transport.go" \
    && check "SSHDumpTransport struct declared" ok \
    || check "SSHDumpTransport struct declared" fail

  # 5 expected fields
  FIELDS_OK=1
  for f in SSHHost SSHUser SSHKeyPath SSHPort PgDumpPath; do
    if ! grep -qE "^[[:space:]]+$f[[:space:]]+string|^[[:space:]]+$f[[:space:]]+int" "internal/dbmigrate/ssh_transport.go"; then
      FIELDS_OK=0
      break
    fi
  done
  [ "$FIELDS_OK" = "1" ] && check "SSHDumpTransport has 5 fields (SSHHost/SSHUser/SSHKeyPath/SSHPort/PgDumpPath)" ok \
    || check "SSHDumpTransport has 5 fields (SSHHost/SSHUser/SSHKeyPath/SSHPort/PgDumpPath)" fail
fi

# 3. Name() == "ssh"
if file_exists "internal/dbmigrate/ssh_transport.go"; then
  file_grep 'func \(SSHDumpTransport\) Name\(\) string \{ return "ssh" \}' "internal/dbmigrate/ssh_transport.go" \
    && check 'SSHDumpTransport.Name() == "ssh"' ok \
    || check 'SSHDumpTransport.Name() == "ssh"' fail
fi

# 4. Dump() method exists (signature check)
if file_exists "internal/dbmigrate/ssh_transport.go"; then
  file_grep "func \(t SSHDumpTransport\) Dump\(" "internal/dbmigrate/ssh_transport.go" \
    && check "SSHDumpTransport.Dump(ctx, sourceDSN, destPath, onLog) method exists" ok \
    || check "SSHDumpTransport.Dump(ctx, sourceDSN, destPath, onLog) method exists" fail
fi

# 5. NewSSHDumpTransportFromEnv reads the 5 env vars
if file_exists "internal/dbmigrate/ssh_transport.go"; then
  file_grep "func NewSSHDumpTransportFromEnv\(\) \*SSHDumpTransport" "internal/dbmigrate/ssh_transport.go" \
    && check "NewSSHDumpTransportFromEnv() exists" ok \
    || check "NewSSHDumpTransportFromEnv() exists" fail
  ENVS_OK=1
  for v in SKYGATE_DBMIGRATE_SSH_HOST SKYGATE_DBMIGRATE_SSH_USER SKYGATE_DBMIGRATE_SSH_KEY SKYGATE_DBMIGRATE_SSH_PORT SKYGATE_DBMIGRATE_SSH_PGDUMP; do
    if ! grep -q "$v" "internal/dbmigrate/ssh_transport.go"; then
      ENVS_OK=0
      break
    fi
  done
  [ "$ENVS_OK" = "1" ] && check "NewSSHDumpTransportFromEnv reads 5 env vars" ok \
    || check "NewSSHDumpTransportFromEnv reads 5 env vars" fail
fi

# 6. quoteForRemoteShell helper exists
if file_exists "internal/dbmigrate/ssh_transport.go"; then
  file_grep "func quoteForRemoteShell\(s string\) string" "internal/dbmigrate/ssh_transport.go" \
    && check "quoteForRemoteShell() helper exists" ok \
    || check "quoteForRemoteShell() helper exists" fail
fi

# 7. framework.go default-fallback uses SSH transport when env says so
if file_exists "internal/dbmigrate/framework.go"; then
  file_grep 'SKYGATE_DBMIGRATE_TRANSPORT' "internal/dbmigrate/framework.go" \
    && file_grep 'NewSSHDumpTransportFromEnv' "internal/dbmigrate/framework.go" \
    && check "framework.go reads SKYGATE_DBMIGRATE_TRANSPORT + NewSSHDumpTransportFromEnv" ok \
    || check "framework.go reads SKYGATE_DBMIGRATE_TRANSPORT + NewSSHDumpTransportFromEnv" fail
fi

# 8. ssh_transport_test.go has the 5 expected test functions
if file_exists "internal/dbmigrate/ssh_transport_test.go"; then
  TESTS_OK=1
  for fn in TestSSHDumpTransport_QuoteForRemoteShell TestSSHDumpTransport_NewFromEnv_RequiresHostAndUser TestSSHDumpTransport_NewFromEnv_PortParsing TestSSHDumpTransport_Dump_FakeSsh TestSSHDumpTransport_Dump_Validation TestSSHDumpTransport_Name; do
    if ! grep -q "^func $fn" "internal/dbmigrate/ssh_transport_test.go"; then
      TESTS_OK=0
      break
    fi
  done
  [ "$TESTS_OK" = "1" ] && check "5+ ssh_transport_test.go test functions present" ok \
    || check "5+ ssh_transport_test.go test functions present" fail
fi

# 9. Dump_FakeSsh test exists (the round-trip — SKIPs on Windows)
if file_exists "internal/dbmigrate/ssh_transport_test.go"; then
  file_grep "TestSSHDumpTransport_Dump_FakeSsh" "internal/dbmigrate/ssh_transport_test.go" \
    && check "Dump_FakeSsh round-trip test exists" ok \
    || check "Dump_FakeSsh round-trip test exists" fail
fi

# 10. Compile-time assertion: SSHDumpTransport implements DumpTransport
if file_exists "internal/dbmigrate/ssh_transport_test.go"; then
  file_grep "var _ DumpTransport = SSHDumpTransport" "internal/dbmigrate/ssh_transport_test.go" \
    && check "var _ DumpTransport = SSHDumpTransport{} compile-time assertion" ok \
    || check "var _ DumpTransport = SSHDumpTransport{} compile-time assertion" fail
fi

# 11. build + vet + dbmigrate tests
GO=""
if command -v go >/dev/null 2>&1; then
  GO="go"
else
  for cand in \
    "C:/Program Files/Go/bin/go.exe" \
    "/c/Program Files/Go/bin/go.exe" \
    "/c/Program Files/Go/bin/go" \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/usr/local/go/bin/go" \
    "/usr/lib/go/bin/go"; do
    [ -x "$cand" ] && GO="$cand" && break
  done
fi
if [ -n "$GO" ]; then
  if "$GO" build ./... >/dev/null 2>&1; then
    check "go build ./... passes" ok
  else
    check "go build ./... passes" fail
  fi
  if "$GO" vet ./internal/dbmigrate/... >/dev/null 2>&1; then
    check "go vet ./internal/dbmigrate/... passes" ok
  else
    check "go vet ./internal/dbmigrate/... passes" fail
  fi
  if "$GO" test ./internal/dbmigrate/... -count=1 >/dev/null 2>&1; then
    check "go test ./internal/dbmigrate/... passes" ok
  else
    check "go test ./internal/dbmigrate/... passes" fail
  fi
else
  check "go binary not found (skipping build/vet/test)" fail
fi

# 12. AGENTS.md mentions B202.5
if [ -f "AGENTS.md" ]; then
  if grep -qE "B202\.5" "AGENTS.md"; then
    check "AGENTS.md mentions B202.5" ok
  else
    check "AGENTS.md mentions B202.5" fail
  fi
else
  check "AGENTS.md mentions B202.5" fail
fi

# 13. verify_pre_deploy.sh has B202.5 run_check
if [ -f "scripts/verify_pre_deploy.sh" ]; then
  if grep -q 'run_check "B202.5"' "scripts/verify_pre_deploy.sh"; then
    check "verify_pre_deploy.sh has B202.5 run_check" ok
  else
    check "verify_pre_deploy.sh has B202.5 run_check" fail
  fi
else
  check "verify_pre_deploy.sh has B202.5 run_check" fail
fi

# 14. b202_5_verify.sh live-verify script exists
file_exists "scripts/b202_5_verify.sh" \
  && check "scripts/b202_5_verify.sh live-verify script exists" ok \
  || check "scripts/b202_5_verify.sh live-verify script exists" fail

echo
echo "=== B202.5: ${PASS} pass, ${FAIL} fail ==="
if [ "$FAIL" -gt "0" ]; then
  echo "FAILURES:"
  for f in "${fails[@]}"; do
    echo "  - $f"
  done
  exit 1
fi
exit 0
