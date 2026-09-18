#!/bin/bash
# check_b165.sh — /my/devices registration form UX
# fix (B165, v1.5.1)
#
# Operator 2026-08-24 UX report: "не красиво
# выглядит поле Регистрации ноды в skygate: поля
# скачут, подсказки не информативны, также нету
# кратких примеров как сформировать ssh ключ на
# машинах". The pre-B165 form had:
#   - OS tiles in a 5-column grid that "jumped"
#     when the screen width changed (auto-fit
#     minmax layout)
#   - Custom TTL value + unit on a single inline-flex
#     row (the label didn't visually own the pair)
#   - Reusable checkbox on its own row with a small
#     gray hint
#   - No example for SSH-key generation (operators
#     who want to use the device as a Linux exit-node
#     or subnet-router had to context-switch to docs)
#
# B165 (this file) pins the fix:
#  1. 2-column form-grid (stable desktop layout,
#     collapses to 1 column on mobile <768px)
#  2. Custom TTL wrapped in a labelled group
#     (.form-group-inline) so the label clearly
#     owns the value+unit pair
#  3. Stronger hint text (.form-hint-strong)
#     instead of the default gray
#  4. New `<details>` block with SSH-key example +
#     per-OS tailscale up commands
#  5. aria-label on the value/unit inputs (a11y)
#  6. New i18n keys (RU + EN) for the help block
#  7. The FAQ card on /my/devices now references
#     the new Help block (less duplication)

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: form uses .form-grid class (stable layout) ==="
# The pre-B165 form had the OS tiles + TTL +
# Reusable in three separate rows with no
# grid wrapper. B165 wraps them in a single
# .form-grid container.
if grep -qE 'class="form-grid"' internal/handlers/templates/user/devices.html; then
    ok "devices.html wraps the registration form in .form-grid"
else
    bad "devices.html: <div class=\"form-grid\"> wrapper MISSING (form fields will 'jump' on resize)"
fi

echo ""
echo "=== contract B: Custom TTL uses .form-group-inline (labelled pair) ==="
# The pre-B165 form had the value + unit in an
# inline-flex row but the label was on the value
# only. B165 wraps them in .form-group-inline +
# adds aria-label for screen readers.
if grep -qE 'class="form-group-inline"' internal/handlers/templates/user/devices.html; then
    ok "Custom TTL row uses .form-group-inline (label owns value+unit)"
else
    bad "devices.html: <div class=\"form-group-inline\"> MISSING (label/value/unit are visually disconnected)"
fi
if grep -qE 'aria-label="\{\{t "keys\.custom_ttl_value_aria"\}\}"' internal/handlers/templates/user/devices.html; then
    ok "Custom TTL value input has aria-label (a11y)"
else
    bad "devices.html: aria-label on the value input MISSING (screen reader can't tell what it is)"
fi
if grep -qE 'aria-label="\{\{t "keys\.custom_ttl_unit_aria"\}\}"' internal/handlers/templates/user/devices.html; then
    ok "Custom TTL unit select has aria-label (a11y)"
else
    bad "devices.html: aria-label on the unit select MISSING"
fi

echo ""
echo "=== contract C: .form-hint-strong (readable hint text) ==="
# The pre-B165 hints used .form-hint (gray-on-gray).
# B165 uses .form-hint-strong for the actionable
# hints (OS hint + TTL hint + reusable hint).
# The CSS class is defined in static/css/themes.css.
if grep -qE '\.form-hint-strong' static/css/themes.css; then
    ok ".form-hint-strong CSS class defined in themes.css"
else
    bad "static/css/themes.css: .form-hint-strong class MISSING"
fi
# At least one device form must use the class.
if grep -qE 'class="form-hint-strong"' internal/handlers/templates/user/devices.html; then
    ok "devices.html uses .form-hint-strong (operator can read the hint)"
else
    bad "devices.html: form-hint-strong class MISSING (hints stay gray-on-gray)"
fi

echo ""
echo "=== contract D: <details> Help block with SSH-key example + per-OS commands ==="
# The new <details> block lives at the bottom of
# the "Add device" card. It has:
#   - reg.help_ssh_title — "Generate an SSH key"
#   - reg.help_linux_title — "Linux / macOS / Windows"
#   - reg.help_mobile_title — "Android / iOS"
#   - reg.help_windows_title — "Windows GUI"
# Each block has a <pre><code> with the actual
# command. The pre-amble copy comes from the
# reg.* i18n keys.
for title in reg.help_ssh_title reg.help_linux_title reg.help_mobile_title reg.help_windows_title; do
    if grep -qE "t \"$title\"" internal/handlers/templates/user/devices.html; then
        ok "Help block references '$title'"
    else
        bad "Help block MISSING '$title' (the operator can't find the SSH-key example)"
    fi
done
# The SSH-key example is a <pre><code> with the
# actual ssh-keygen command. The pre-B165 form
# had no such example (the operator had to read
# docs to know how to set up an exit-node).
if grep -qE 'ssh-keygen -t ed25519' internal/handlers/templates/user/devices.html; then
    ok "Help block has the ssh-keygen example"
else
    bad "Help block: ssh-keygen example MISSING (operator can't generate the key without docs)"
fi
# The exit-node + subnet-router advertise commands.
if grep -qE 'advertise-exit-node' internal/handlers/templates/user/devices.html; then
    ok "Help block has the --advertise-exit-node command"
else
    bad "Help block: --advertise-exit-node command MISSING"
fi
if grep -qE 'advertise-routes=' internal/handlers/templates/user/devices.html; then
    ok "Help block has the --advertise-routes example"
else
    bad "Help block: --advertise-routes example MISSING"
fi

echo ""
echo "=== contract E: i18n keys (RU + EN) ==="
needed=(
  "keys.custom_ttl_value_aria"
  "keys.custom_ttl_unit_aria"
  "reg.os_label"
  "reg.os_hint"
  "reg.help_title"
  "reg.help_ssh_title"
  "reg.help_ssh_intro"
  "reg.help_linux_title"
  "reg.help_linux_intro"
  "reg.help_mobile_title"
  "reg.help_mobile_intro"
  "reg.help_mobile_step1"
  "reg.help_mobile_step2"
  "reg.help_mobile_step3"
  "reg.help_windows_title"
  "reg.help_windows_intro"
)
for k in "${needed[@]}"; do
    c=$(grep -cE "\"$k\"" internal/i18n/catalog_my.go 2>/dev/null || true)
    c=${c:-0}
    if [ "$c" -ge 2 ]; then
        ok "i18n key '$k' present in both RU and EN"
    else
        bad "i18n key '$k' MISSING in catalog_my.go (found $c entries — need 2 for RU+EN)"
    fi
done

echo ""
echo "=== contract F: mobile-responsive form-grid ==="
# The pre-B165 form used inline-flex + auto-fit
# minmax, which broke on mobile. B165's .form-grid
# is a stable 2-column desktop grid that collapses
# to 1 column on <768px (the same breakpoint the
# rest of the UI uses).
if grep -qE '@media \(max-width: 768px\)' static/css/themes.css; then
    ok "@media (max-width: 768px) breakpoint present in themes.css"
else
    bad "static/css/themes.css: 768px mobile breakpoint MISSING"
fi
if grep -qE '\.form-grid' static/css/themes.css && \
   grep -qE 'grid-template-columns: 1fr;' static/css/themes.css; then
    # The .form-grid base rule + the 1fr mobile rule
    # both exist (the mobile rule is inside the
    # @media block). We use two greps to avoid the
    # brittle multi-line regex on the minified
    # themes.css.
    ok ".form-grid base + mobile 1-column rule both present"
else
    bad "static/css/themes.css: .form-grid mobile rule (1 column) MISSING"
fi

echo ""
echo "=== contract G: build + vet clean ==="
out=$(go build ./... 2>&1)
if [ -z "$out" ]; then
    ok "go build ./... clean"
else
    bad "go build output: $out"
fi
out=$(go vet ./... 2>&1)
if [ -z "$out" ]; then
    ok "go vet ./... clean"
else
    bad "go vet output: $out"
fi

echo ""
echo "=== summary ==="
echo "B165: /my/devices registration form UX fix"
echo "all contracts satisfied"
