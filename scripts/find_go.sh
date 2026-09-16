#!/usr/bin/env bash
# find_go.sh — locate the `go` binary from inside Git Bash on Windows.
#
# Why this script exists: the bash tool used by Mavis on Windows
# runs through Git Bash, where `go` is not on $PATH. We delegate
# to cmd.exe which DOES have go.exe on its PATH (set by the default
# Windows install).

set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
BAT="$HERE/_find_go.bat"

if [ ! -f "$BAT" ]; then
  cat > "$BAT" <<'EOF'
@echo off
powershell -NoProfile -Command "(Get-Command go.exe -ErrorAction SilentlyContinue).Source"
EOF
fi

# `cmd.exe` IS on Git Bash's PATH (at /c/Windows/System32/cmd.exe in the
# unix namespace). The output is a Windows-style path like
# "C:\Program Files\Go\bin\go.exe" which we keep verbatim — callers
# invoke it via `cmd.exe //c "\"$GO\" $cmd\""` rather than running
# it directly as a bash exec (cygwin/MSYS mixed-mode breakage).
win_go="$(cmd.exe //c "$BAT" 2>/dev/null | tr -d '\r' | head -1)"
if [ -n "$win_go" ]; then
  if cmd.exe //c "\"$win_go\" version" >/dev/null 2>&1; then
    echo "$win_go"
    exit 0
  fi
fi

exit 1
