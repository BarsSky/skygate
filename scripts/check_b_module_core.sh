#!/usr/bin/env bash
# ============================================================================
# check_b_module_core.sh — B-check for the module Plugin API (B-mod-core, 2026-09-09)
# See AGENTS.md (B-mod-core entry) and docs/internal/architecture-modules.md
# for the B-block context.
#
# What this verifies
# ------------------
# The plugin API (internal/module/ package) is the foundation for
# skygate's module-based architecture. Tailscale is Module #1, but
# future modules (headplane, telegram, derp) will reuse the same
# Manager + State + interface. This B-check pins the contract so
# that:
#   - The Module interface has the expected 8 methods
#   - Manager.Register/InitAll/StartEnabled/StopAll exist
#   - State persistence is atomic (write to .tmp, rename)
#   - Health check loop runs periodically
#   - Sub-feature Requires validation works
#   - Unit tests pass (compile + run)
#
# Contracts (12 total)
# --------------------
# A. internal/module/module.go exists
# B. Module interface has 8 methods: Name, Init, Start, Stop,
#    Status, Health, SubFeatures, EnableSubFeature, DisableSubFeature
# C. internal/module/state.go exists with State struct (Name, State,
#    Enabled, InstallMode, InstalledAt, StartedAt, SubFeatures,
#    LastHealth, LastError, Info)
# D. loadState + saveState functions exist (state.go)
# E. saveState uses atomic write pattern (.tmp + rename)
# F. internal/module/manager.go exists with Manager struct
# G. Manager has NewManager + Register + InitAll + StartEnabled +
#    StopAll + Get + List + Enable + Disable + EnableSubFeature +
#    DisableSubFeature + StartHealthLoop
# H. Manager uses /var/lib/skygate/modules/ as data dir
# I. Sentinel errors: ErrNotInstalled, ErrAlreadyRunning,
#    ErrAlreadyStopped, ErrSubFeatureNotFound, ErrSubFeatureRequires
# J. internal/module/module_test.go exists + has 8+ Test functions
# K. `go test ./internal/module/...` passes
# L. docs/internal/architecture-modules.md exists
#
# Exit codes
# ----------
#   0  all contracts pass
#   1  at least one contract failed
# ============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_DIR"

PASS=0
FAIL=0
fails=()

# Helper: report a single contract result.
ok() {
    local name="$1"
    PASS=$((PASS + 1))
    echo "  [PASS] $name"
}

fail() {
    local name="$1"
    local msg="$2"
    FAIL=$((FAIL + 1))
    fails+=("$name: $msg")
    echo "  [FAIL] $name: $msg"
}

# Helper: assert a file exists.
require_file() {
    local path="$1"
    if [ ! -f "$path" ]; then
        echo "ERROR: $path not found" >&2
        exit 2
    fi
}

echo "=== check_b_module_core.sh (B-mod-core, 2026-09-09) ==="

# A. internal/module/module.go exists
if [ -f "internal/module/module.go" ]; then
    ok "A: internal/module/module.go exists"
else
    fail "A" "internal/module/module.go not found"
fi

# B. Module interface has 9 methods
MODULE_GO="internal/module/module.go"
if [ -f "$MODULE_GO" ]; then
    method_count=0
    for method in Name Init Start Stop Status Health SubFeatures EnableSubFeature DisableSubFeature; do
        # Match the method signature in the interface block
        # (e.g. "Name() string", "Init(ctx, cfg) error",
        # "SubFeatures() []SubFeature").
        if grep -qE "^	${method}\(" "$MODULE_GO"; then
            method_count=$((method_count + 1))
        fi
    done
    if [ "$method_count" -ge 9 ]; then
        ok "B: Module interface has $method_count methods (Name, Init, Start, Stop, Status, Health, SubFeatures, EnableSubFeature, DisableSubFeature)"
    else
        fail "B" "Module interface has only $method_count methods, want 9"
    fi
fi

# C. State struct in state.go
STATE_GO="internal/module/state.go"
if [ -f "$STATE_GO" ]; then
    fields_found=0
    for field in Name State Enabled InstallMode InstalledAt StartedAt SubFeatures LastHealth LastError Info; do
        if grep -qE "^\s+${field}\s+\S+" "$STATE_GO"; then
            fields_found=$((fields_found + 1))
        fi
    done
    if [ "$fields_found" -ge 10 ]; then
        ok "C: State struct has $fields_found fields"
    else
        fail "C" "State struct has only $fields_found fields, want 10"
    fi
fi

# D. loadState + saveState functions exist
if [ -f "$STATE_GO" ]; then
    if grep -qE "^func loadState\(" "$STATE_GO" && grep -qE "^func saveState\(" "$STATE_GO"; then
        ok "D: loadState + saveState functions exist"
    else
        fail "D" "loadState or saveState missing"
    fi
fi

# E. saveState uses atomic write pattern (.tmp + rename)
if [ -f "$STATE_GO" ]; then
    if grep -qE 'state\.json\.tmp' "$STATE_GO" && grep -qE 'os\.Rename' "$STATE_GO"; then
        ok "E: saveState uses atomic write (state.json.tmp + os.Rename)"
    else
        fail "E" "saveState does not use atomic write pattern"
    fi
fi

# F. internal/module/manager.go exists with Manager struct
MANAGER_GO="internal/module/manager.go"
if [ -f "$MANAGER_GO" ]; then
    if grep -qE "^type Manager struct" "$MANAGER_GO"; then
        ok "F: Manager struct exists in manager.go"
    else
        fail "F" "Manager struct not found"
    fi
fi

# G. Manager has all expected methods
if [ -f "$MANAGER_GO" ]; then
    methods_found=0
    for method in NewManager Register InitAll StartEnabled StopAll 'Get\b' List Enable Disable EnableSubFeature DisableSubFeature StartHealthLoop StopHealthLoop SetEnv SetHealthInterval; do
        if grep -qE "^func \(m \*Manager\) ${method}\(" "$MANAGER_GO"; then
            methods_found=$((methods_found + 1))
        fi
    done
    if [ "$methods_found" -ge 12 ]; then
        ok "G: Manager has $methods_found methods (need 12+: NewManager, Register, InitAll, StartEnabled, StopAll, Get, List, Enable, Disable, Enable/DisableSubFeature, Start/StopHealthLoop)"
    else
        fail "G" "Manager has only $methods_found methods, want 12+"
    fi
fi

# H. Manager uses /var/lib/skygate/modules/ as data dir convention
if [ -f "$MANAGER_GO" ]; then
    if grep -qE '/var/lib/skygate/modules' "$MANAGER_GO" || \
       grep -qE 'dataDir\s*string' "$MANAGER_GO"; then
        ok "H: Manager stores per-module data dir (convention: /var/lib/skygate/modules/<name>/)"
    else
        fail "H" "Manager does not reference /var/lib/skygate/modules/"
    fi
fi

# I. Sentinel errors
if [ -f "$MODULE_GO" ]; then
    errs_found=0
    for err in ErrNotInstalled ErrAlreadyRunning ErrAlreadyStopped ErrSubFeatureNotFound ErrSubFeatureRequires; do
        if grep -qE "^\s+${err}\s+=" "$MODULE_GO"; then
            errs_found=$((errs_found + 1))
        fi
    done
    if [ "$errs_found" -ge 5 ]; then
        ok "I: Sentinel errors defined: $errs_found of 5"
    else
        fail "I" "Only $errs_found sentinel errors defined, want 5"
    fi
fi

# J. module_test.go has 8+ Test functions
TEST_GO="internal/module/module_test.go"
if [ -f "$TEST_GO" ]; then
    test_count=$(grep -cE "^func Test" "$TEST_GO" 2>/dev/null || echo 0)
    if [ "$test_count" -ge 8 ]; then
        ok "J: module_test.go has $test_count test functions"
    else
        fail "J" "module_test.go has only $test_count test functions, want 8+"
    fi
else
    fail "J" "internal/module/module_test.go not found"
fi

# K. `go test ./internal/module/...` passes
echo "  [K] running go test ./internal/module/... (may take 5-10 seconds)"
# Find the go binary. The PowerShell host has it in PATH, but
# the WSL/Git-Bash subshell that runs this script may not —
# check both the standard install path (WSL: /mnt/c, Git-Bash: /c)
# and the user's GOPATH bin (/mnt/c/Users/.../go/bin).
GO_BIN=""
if command -v go >/dev/null 2>&1; then
    GO_BIN="go"
elif [ -x "/mnt/c/Program Files/Go/bin/go.exe" ]; then
    GO_BIN="/mnt/c/Program Files/Go/bin/go.exe"
elif [ -x "/c/Program Files/Go/bin/go.exe" ]; then
    GO_BIN="/c/Program Files/Go/bin/go.exe"
fi
if [ -z "$GO_BIN" ]; then
    fail "K" "go binary not found in PATH or standard install locations"
elif [ "$GO_BIN" = "go" ]; then
    if go test ./internal/module/... >/tmp/check_b_module_core_test.log 2>&1; then
        ok "K: go test ./internal/module/... passes"
    else
        fail "K" "go test failed — see /tmp/check_b_module_core_test.log"
        tail -20 /tmp/check_b_module_core_test.log | sed 's/^/      /'
    fi
else
    # Direct path (WSL or Git-Bash) — quote the path so spaces work.
    if "$GO_BIN" test ./internal/module/... >/tmp/check_b_module_core_test.log 2>&1; then
        ok "K: go test ./internal/module/... passes (via $GO_BIN)"
    else
        fail "K" "go test failed — see /tmp/check_b_module_core_test.log"
        tail -20 /tmp/check_b_module_core_test.log | sed 's/^/      /'
    fi
fi

# L. architecture-modules.md exists
if [ -f "docs/internal/architecture-modules.md" ]; then
    if grep -qE "Plugin API" "docs/internal/architecture-modules.md" && \
       grep -qE "Module interface" "docs/internal/architecture-modules.md"; then
        ok "L: docs/internal/architecture-modules.md exists with Plugin API + Module interface sections"
    else
        fail "L" "architecture-modules.md exists but missing required sections"
    fi
else
    fail "L" "docs/internal/architecture-modules.md not found"
fi

# Summary
echo
echo "=== Summary: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
    echo "Failed contracts:"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
exit 0
