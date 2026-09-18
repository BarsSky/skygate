#!/usr/bin/env bash
# check_b113.sh — v1.3.13 (youtube.com/32 bug fix): verify
# targetValue validation for target_type=ip|subnet forms.
#
# Bug: Pre-v1.3.13, an operator who typed a bare hostname (e.g.
# "youtube.com") in the IP field would get "youtube.com/32" saved
# to the DB. The ACL builder then promoted it to a host alias
# "h-rule-youtube-com-32: youtube.com/32" — a malformed CIDR
# that headscale rejects, causing the whole policy re-apply to
# fail.
#
# Fix: form_my.go validates targetValue via isValidIPOrCIDR
# before any processing. For target_type=domain, the form
# does DNS resolution (hostname is valid input there).
#
# Pin: 4 contracts:
#   1. isValidIPOrCIDR helper exists in form_my.go
#   2. helper is called in PostMyExitRule (form path)
#   3. helper accepts bare IPs + IPv4/IPv6 + CIDRs
#   4. helper rejects bare hostnames + garbage

set -u
cd "$(dirname "$0")/.."

fail=0

# 1. isValidIPOrCIDR helper exists
if ! grep -qE '^func isValidIPOrCIDR' internal/feature/exit_rules/form_my.go; then
    echo "SKY-FAIL: isValidIPOrCIDR helper missing in form_my.go" >&2
    fail=1
else
    echo "  PASS: isValidIPOrCIDR helper defined"
fi

# 2. called in PostMyExitRule (form path) with targetType ip|subnet check
if ! grep -qF 'isValidIPOrCIDR(targetValue)' internal/feature/exit_rules/form_my.go; then
    echo "SKY-FAIL: isValidIPOrCIDR not called in form_my.go" >&2
    fail=1
else
    echo "  PASS: isValidIPOrCIDR called in form_my.go"
fi
if ! grep -qF 'invalid target_value' internal/feature/exit_rules/form_my.go; then
    echo "SKY-FAIL: 400-BadRequest response with 'invalid target_value' not in form_my.go" >&2
    fail=1
else
    echo "  PASS: 400 response with 'invalid target_value' on bad input"
fi

# 3. unit test exists with table-driven cases for IPv4+IPv6+CIDR+hostname+garbage
if ! grep -qF 'func TestIsValidIPOrCIDR_IPv4' internal/feature/exit_rules/form_my_validate_test.go; then
    echo "SKY-FAIL: TestIsValidIPOrCIDR_IPv4 unit test missing" >&2
    fail=1
else
    echo "  PASS: TestIsValidIPOrCIDR_IPv4 unit test present"
fi
# Pinned bad-input cases (the original bug + common typos)
for needle in 'youtube.com' 'youtube.com/32' '"google.com"' '999.999.999.999'; do
    if ! grep -qF "$needle" internal/feature/exit_rules/form_my_validate_test.go; then
        echo "SKY-FAIL: test case for $needle missing in form_my_validate_test.go" >&2
        fail=1
    fi
done
echo "  PASS: bad-input test cases (youtube.com, youtube.com/32, google.com, 999.999.999.999)"

# 4. go build passes
GO=""
if command -v go >/dev/null 2>&1; then
    GO="go"
else
    for cand in \
        "/c/Program Files/Go/bin/go.exe" \
        "/c/Program Files/Go/bin/go" \
        "/mnt/c/Program Files/Go/bin/go.exe" \
        "/mnt/c/Program Files/Go/bin/go" \
        "/usr/local/go/bin/go" \
        "/usr/lib/go/bin/go" \
        "/opt/go/bin/go" \
        "/snap/bin/go"; do
        if [ -x "$cand" ]; then
            GO="$cand"
            break
        fi
    done
fi
if [ -z "$GO" ]; then
    echo "SKY-FAIL: go binary not found" >&2
    fail=1
elif ! "$GO" build ./... >/dev/null 2>&1; then
    echo "SKY-FAIL: go build ./... failed (GO=$GO)" >&2
    fail=1
else
    echo "  PASS: go build ./... clean (GO=$GO)"
fi
# Also run the unit test
if [ -n "$GO" ]; then
    if ! "$GO" test -count=1 -run TestIsValidIPOrCIDR ./internal/feature/exit_rules/ >/dev/null 2>&1; then
        echo "SKY-FAIL: TestIsValidIPOrCIDR unit test failed" >&2
        fail=1
    else
        echo "  PASS: TestIsValidIPOrCIDR unit test passes"
    fi
fi

if [ $fail -eq 0 ]; then
    echo ""
    echo "B113 PASS: youtube.com/32 bug fix is in effect (4 contracts)"
    exit 0
else
    echo ""
    echo "B113 FAIL: youtube.com/32 bug fix incomplete" >&2
    exit 1
fi
