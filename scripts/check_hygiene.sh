#!/usr/bin/env bash
# check_hygiene.sh — audit-only generator + build footprint snapshot.
#
# 2026-09-15 (operator request): "проверить проект на генерацию
# мусорных файлов и сколько весит суммарно вся сборка и есть ли
# утечки и загрязнение системы?". Two-axis audit:
#
#   1. Repo junk — files in the working tree that should NOT be
#      committed. Sizes in MB.
#   2. Build artifacts — skygate.exe (or ./skygate on Linux),
#      go-build cache, Go module cache. Sizes in MB.
#
# This script does NOT modify anything. It prints a markdown table
# you can paste into a PR description or weekly hygiene report.
# Run on the DEV machine after a long session, and on the agent VM
# after a long live-ops window.
#
# Optionally the script can probe the LIVE VM (192.168.13.69) for
# the production-side pollution: /tmp/*.sh build-up, journal size,
# docker reclaimable. Pass --live to enable.

set -euo pipefail
# Disable bash pathname expansion (globbing) for the rest of the
# script. Without this, patterns like "/tmp/*.sh" in `grep`
# invocations get glob-expanded to the list of matching files
# before grep sees them — `grep -Fxq '/tmp/*.sh' .gitignore`
# then matches against literal files in /tmp, not against the
# .gitignore line content. Re-enable with `set +f` if needed.
set -f

ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn(){ printf '  \033[33m!\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

LIVE=0
if [ "${1:-}" = "--live" ]; then LIVE=1; fi

du_mb() {
  # du -sb returns bytes; awk converts to MB.
  du -sb "$1" 2>/dev/null | awk '{ printf "%.1f MB", $1/1024/1024 }'
}

file_count() {
  # find -type f counts files; -maxdepth 1 keeps it cheap.
  find "$1" -maxdepth "${2:-1}" -type f 2>/dev/null | wc -l
}

hdr "Repo junk (working tree)"
printf '  %-40s  %12s  %6s\n' "Path" "Size" "Files"
printf '  %s\n' "----------------------------------------  ------------  ------"
for path in .trash tmp; do
  if [ -d "$path" ]; then
    printf '  %-40s  %12s  %6d\n' "$path" "$(du_mb "$path")" "$(file_count "$path" 99)"
  fi
done
# Root-level check_*.sh scripts are gitignored (see .gitignore
# line: 'check_*.sh'). They're invisible to git but visible to ls.
# Temporarily re-enable globbing (we set -f at script top to
# protect grep patterns from accidental expansion).
set +f
shopt -s nullglob 2>/dev/null || true
chks=( check_*.sh )
n=${#chks[@]}
if [ "$n" -gt 0 ]; then
  printf '  %-40s  %12s  %6d\n' "check_*.sh (root, gitignored)" "(in .gitignore)" "$n"
fi
vs=( verify_*.sh )
n=${#vs[@]}
if [ "$n" -gt 0 ]; then
  printf '  %-40s  %12s  %6d\n' "verify_*.sh (root, gitignored)" "(in .gitignore)" "$n"
fi
shopt -u nullglob 2>/dev/null || true
set -f

hdr "Build artifacts"
printf '  %-40s  %12s\n' "Path" "Size"
printf '  %s\n' "----------------------------------------  ------------"

if [ -f skygate.exe ]; then
  printf '  %-40s  %12s\n' "skygate.exe (Windows build)" "$(du_mb skygate.exe)"
fi
if [ -f skygate ]; then
  printf '  %-40s  %12s\n' "./skygate (Linux build)"      "$(du_mb skygate)"
fi

# Go build cache: %LocalAppData%\Go-Build on Windows, ~/.cache/go-build on Linux.
WIN_CACHE="${LOCALAPPDATA:-}/Go-Build"
LIN_CACHE="${HOME:-}/.cache/go-build"
if [ -n "${LOCALAPPDATA:-}" ] && [ -d "$WIN_CACHE" ]; then
  printf '  %-40s  %12s\n' "$WIN_CACHE" "$(du_mb "$WIN_CACHE")"
elif [ -n "${HOME:-}" ] && [ -d "$LIN_CACHE" ]; then
  printf '  %-40s  %12s\n' "$LIN_CACHE" "$(du_mb "$LIN_CACHE")"
fi

# Go module cache: %UserProfile%\go\pkg\mod on Windows, ~/go/pkg/mod on Linux.
GOMODCACHE="${USERPROFILE:-${HOME:-}}/go/pkg/mod"
if [ -n "${USERPROFILE:-${HOME:-}}" ] && [ -d "$GOMODCACHE" ]; then
  printf '  %-40s  %12s\n' "$GOMODCACHE" "$(du_mb "$GOMODCACHE")"
fi

hdr "Temp artifacts (auto-clean candidates)"
TMP_TOTAL="?"
if [ -d "${TEMP:-/tmp}" ]; then
  TMP_TOTAL=$(du_mb "${TEMP:-/tmp}" 2>/dev/null || true)
  TMP_TOTAL=${TMP_TOTAL:-"? (inaccessible)"}
fi
printf '  %-40s  %12s\n' "${TEMP:-/tmp}" "$TMP_TOTAL"
# Optional: count skygate-named temp binaries that survived a session.
if [ -d "${TEMP:-/tmp}" ]; then
  n=$(find "${TEMP:-/tmp}" -maxdepth 2 -name "skygate*" -type f 2>/dev/null | wc -l 2>/dev/null || echo 0)
  n=$(printf '%s' "$n" | tr -dc '0-9')
  n=${n:-0}
  case "$n" in ''|*[!0-9]*) n=0 ;; esac
  if [ "$n" -gt 0 ]; then
    warn "${n} stale skygate binaries in ${TEMP:-/tmp} — review + delete before next deploy"
  fi
fi

hdr "Sanity"
if [ -f skygate.exe ]; then ok "skygate.exe present"; else warn "skygate.exe missing (build not run yet?)"; fi
if grep -q "/tmp/\*\.sh"   .gitignore \
   && grep -q "/tmp/\*\.html" .gitignore \
   && grep -q "/tmp/\*\.json" .gitignore; then
  ok ".gitignore catches /tmp/*.{sh,html,json}"
else
  warn ".gitignore missing one of /tmp/*.sh, *.html, *.json — extend the patterns block"
fi

if [ "$LIVE" = "0" ]; then
  printf '\nRe-run with \033[1m--live\033[0m to probe the agent VM (192.168.13.69) for /tmp/*.sh + journal size + docker reclaimable.\n'
  exit 0
fi

hdr "Live probe: agent VM 192.168.13.69"
ssh_cmd='docker exec skygate-skygate-1 find /tmp -maxdepth 1 -name "*.sh" 2>/dev/null | wc -l; ls -la /var/log/syslog 2>/dev/null; journalctl --disk-usage; docker system df 2>/dev/null'
warn "Live probe not enabled by default — uncomment below to activate."
printf '\n  ssh hermes-debug@192.168.13.69 \'%s\'\n' "$ssh_cmd"
