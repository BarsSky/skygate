#!/bin/bash
# scripts/check_b237_15.sh — B237.15 (v1.5.2+) deployment variants
# build-time contract. Pins the deploy-surface pieces that the
# 2026-09-07 deployment-variants work added:
#
#   V1+V5: prebuilt-image docker compose (docker-compose.ghcr.yml,
#          docker-compose.lite.yml)
#   V2:   podman compose (doc-only in README.md, no new file)
#   V3:   install.sh single-file autodetect installer
#   V4:   install-{debian,rh,alpine,bare}.sh per-OS installers
#   V6:   Setup-Skygate-Win.ps1 Windows installer
#   V8:   .github/workflows/release.yml (publishes ghcr image +
#          tarball artifacts)
#
# What this check guards against:
#   - Drift between the installer and the release workflow (e.g.
#     someone renames the asset in release.yml but install.sh
#     still requests the old name → SHA256 verify fails on every
#     install)
#   - Missing per-OS installer (someone deletes install-alpine.sh
#     but the autodetect dispatcher in install.sh still routes
#     Alpine users to it)
#   - The PowerShell installer falling behind (someone changes
#     the asset extension from .zip to .tar.gz but
#     Setup-Skygate-Win.ps1 still expects .zip)
#   - Dockerfile.prebuilt regressing into a single-stage build
#     (which would defeat the whole point — the image would be
#     600 MB and 60s to first start again)
#   - entrypoint.sh's SKYGATE_PREBUILT guard regressing (the
#     prebuilt image would try to do an in-container build
#     that fails because go isn't installed)
#
# What this check does NOT cover:
#   - The actual functionality of the installers (e.g. does
#     the systemd unit start the service correctly). That's
#     covered by the deploy-e2e job in CI which runs each
#     per-OS installer in a fresh docker container of the
#     matching distro.
#   - The release workflow itself firing on a tag push. That
#     requires actual GitHub Actions and can only be tested
#     on a real tag. The check pins the WORKFLOW FILE, not
#     its execution.
#
# Exit 0 on all green, non-zero on any FAIL.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# --- A. Release workflow (V8) ---

# A.1 workflow file exists
if [ -f .github/workflows/release.yml ]; then
    ok "A.1 .github/workflows/release.yml exists (V8 release workflow)"
else
    bad "A.1 .github/workflows/release.yml missing (V8 prerequisite)"
fi

# A.2 workflow triggers on tag push
if grep -qE "tags:[[:space:]]*$" .github/workflows/release.yml 2>/dev/null && \
   grep -qE "'v\[0-9\]" .github/workflows/release.yml 2>/dev/null; then
    ok "A.2 release.yml triggers on tag push (v* pattern)"
else
    bad "A.2 release.yml must trigger on tag push (v* pattern)"
fi

# A.3 workflow builds + pushes to ghcr.io
if grep -q 'ghcr.io' .github/workflows/release.yml 2>/dev/null; then
    ok "A.3 release.yml publishes to ghcr.io"
else
    bad "A.3 release.yml must publish to ghcr.io (BarsSky/skygate)"
fi

# A.4 workflow builds linux-amd64 + linux-arm64 images
if grep -q 'linux/amd64' .github/workflows/release.yml 2>/dev/null && \
   grep -q 'linux/arm64' .github/workflows/release.yml 2>/dev/null; then
    ok "A.4 release.yml builds for linux/amd64 + linux/arm64"
else
    bad "A.4 release.yml must build for both linux/amd64 and linux/arm64"
fi

# A.5 workflow publishes go binary tarballs
if grep -q 'tar.gz' .github/workflows/release.yml 2>/dev/null; then
    ok "A.5 release.yml publishes go binary tarballs (.tar.gz)"
else
    bad "A.5 release.yml must publish go binary tarballs"
fi

# A.6 workflow publishes SHA256SUMS
if grep -q 'SHA256SUMS' .github/workflows/release.yml 2>/dev/null; then
    ok "A.6 release.yml publishes SHA256SUMS file"
else
    bad "A.6 release.yml must publish SHA256SUMS (the file install scripts verify against)"
fi

# A.7 workflow creates a GitHub Release
if grep -q 'softprops/action-gh-release' .github/workflows/release.yml 2>/dev/null; then
    ok "A.7 release.yml creates a GitHub Release with the artifacts"
else
    bad "A.7 release.yml must create a GitHub Release (softprops/action-gh-release)"
fi

# --- B. Dockerfile.prebuilt (V8 image build) ---

# B.1 Dockerfile.prebuilt exists
if [ -f Dockerfile.prebuilt ]; then
    ok "B.1 Dockerfile.prebuilt exists (multi-stage prebuilt image)"
else
    bad "B.1 Dockerfile.prebuilt missing (V8 prebuilt image)"
fi

# B.2 multi-stage (separate builder + runtime stages)
if grep -qE '^FROM .* AS builder' Dockerfile.prebuilt 2>/dev/null && \
   grep -qE '^FROM alpine' Dockerfile.prebuilt 2>/dev/null; then
    ok "B.2 Dockerfile.prebuilt is multi-stage (builder + alpine runtime)"
else
    bad "B.2 Dockerfile.prebuilt must be multi-stage (separate builder + alpine runtime)"
fi

# B.3 CGO_ENABLED=0 (pure Go, no libc)
if grep -q 'CGO_ENABLED=0' Dockerfile.prebuilt 2>/dev/null; then
    ok "B.3 Dockerfile.prebuilt uses CGO_ENABLED=0 (matches v1.3.1 sqlite removal)"
else
    bad "B.3 Dockerfile.prebuilt must set CGO_ENABLED=0 (static binary, no libc)"
fi

# B.4 SKYGATE_PREBUILT=1 baked in (entrypoint skips the build step)
if grep -q 'SKYGATE_PREBUILT=1' Dockerfile.prebuilt 2>/dev/null; then
    ok "B.4 Dockerfile.prebuilt sets SKYGATE_PREBUILT=1 (entrypoint skips in-container build)"
else
    bad "B.4 Dockerfile.prebuilt must set SKYGATE_PREBUILT=1 (otherwise entrypoint tries to build)"
fi

# B.5 tailscale binaries copied in
if grep -q 'tailscale/tailscale' Dockerfile.prebuilt 2>/dev/null; then
    ok "B.5 Dockerfile.prebuilt includes tailscale binaries (in-image, no sidecar)"
else
    bad "B.5 Dockerfile.prebuilt must include tailscale binaries (in-image)"
fi

# --- C. entrypoint.sh SKYGATE_PREBUILT guard (V8 runtime) ---

# C.1 entrypoint.sh checks SKYGATE_PREBUILT env var
if grep -q 'SKYGATE_PREBUILT' entrypoint.sh 2>/dev/null; then
    ok "C.1 entrypoint.sh gates the build step on SKYGATE_PREBUILT"
else
    bad "C.1 entrypoint.sh must check SKYGATE_PREBUILT (skip the build step for prebuilt images)"
fi

# C.2 entrypoint.sh still works for the dev path (no SKYGATE_PREBUILT)
# (the `else` branch with `go mod download` + `go build` is preserved)
if grep -q 'go mod download' entrypoint.sh 2>/dev/null && \
   grep -q 'go build.*ldflags' entrypoint.sh 2>/dev/null; then
    ok "C.2 entrypoint.sh preserves the in-container build path (dev mode)"
else
    bad "C.2 entrypoint.sh must preserve the in-container build path (dev mode)"
fi

# --- D. Docker compose variants (V1 + V5) ---

# D.1 docker-compose.ghcr.yml exists
if [ -f docker-compose.ghcr.yml ]; then
    ok "D.1 docker-compose.ghcr.yml exists (V1 prebuilt-image variant)"
else
    bad "D.1 docker-compose.ghcr.yml missing (V1 prebuilt-image variant)"
fi

# D.2 docker-compose.ghcr.yml uses image: (not build:)
if grep -qE 'image:.*ghcr.io' docker-compose.ghcr.yml 2>/dev/null && \
   ! grep -qE '^[[:space:]]+build:' docker-compose.ghcr.yml 2>/dev/null; then
    ok "D.2 docker-compose.ghcr.yml uses image: (no build:) — pulls prebuilt"
else
    bad "D.2 docker-compose.ghcr.yml must use image: (no build: block) — that's the whole point"
fi

# D.3 docker-compose.ghcr.yml sets SKYGATE_PREBUILT=1 (defense-in-depth)
if grep -q 'SKYGATE_PREBUILT=1' docker-compose.ghcr.yml 2>/dev/null; then
    ok "D.3 docker-compose.ghcr.yml sets SKYGATE_PREBUILT=1 (defense-in-depth)"
else
    bad "D.3 docker-compose.ghcr.yml must set SKYGATE_PREBUILT=1 (defense-in-depth)"
fi

# D.4 docker-compose.lite.yml exists
if [ -f docker-compose.lite.yml ]; then
    ok "D.4 docker-compose.lite.yml exists (V5 sky-only variant)"
else
    bad "D.4 docker-compose.lite.yml missing (V5 sky-only variant)"
fi

# D.5 docker-compose.lite.yml has a healthcheck (the leaner file SHOULD have one)
if grep -q 'healthcheck:' docker-compose.lite.yml 2>/dev/null; then
    ok "D.5 docker-compose.lite.yml defines a healthcheck (best-practice for the lean file)"
else
    bad "D.5 docker-compose.lite.yml should define a healthcheck"
fi

# D.6 docker-compose.lite.yml does NOT have a docker.sock bind-mount
# (smaller attack surface, intentional trade-off: no in-container
# auto-updater). The file may MENTION docker.sock in comments
# (we explain why it's omitted), but there must be no actual
# `- /var/run/docker.sock:` bind-mount line.
if ! grep -qE '^\s*-\s*/var/run/docker\.sock' docker-compose.lite.yml 2>/dev/null; then
    ok "D.6 docker-compose.lite.yml has no docker.sock bind-mount (smaller attack surface, intentional)"
else
    bad "D.6 docker-compose.lite.yml must NOT have a /var/run/docker.sock bind-mount (V5 trade-off)"
fi

# --- E. Bare-metal installers (V3 + V4) ---

# E.1 install.sh exists
if [ -x deploy/install.sh ]; then
    ok "E.1 deploy/install.sh exists and is executable (V3 autodetect)"
else
    bad "E.1 deploy/install.sh missing or not executable (V3 autodetect)"
fi

# E.2 install.sh dispatches to per-OS scripts
if grep -q 'install-${OS_FAMILY}.sh' deploy/install.sh 2>/dev/null; then
    ok "E.2 deploy/install.sh dispatches to install-{OS_FAMILY}.sh"
else
    bad "E.2 deploy/install.sh must dispatch to the per-OS installer"
fi

# E.3 per-OS installers all exist + are executable
for f in debian rh alpine bare; do
    if [ -x "deploy/install-${f}.sh" ]; then
        ok "E.3 deploy/install-${f}.sh exists and is executable"
    else
        bad "E.3 deploy/install-${f}.sh missing or not executable (V4 per-OS installer)"
    fi
done

# E.4 per-OS scripts source install-common.sh
for f in debian rh alpine bare; do
    if grep -q 'install-common.sh' "deploy/install-${f}.sh" 2>/dev/null; then
        ok "E.4 deploy/install-${f}.sh sources install-common.sh (shared helpers)"
    else
        bad "E.4 deploy/install-${f}.sh must source install-common.sh (DRY)"
    fi
done

# E.5 install-common.sh is not executable (it's a source-only file).
# We check the GIT INDEX mode (not the filesystem mode) because
# Windows doesn't track unix mode bits — the file is ALWAYS
# "executable" from bash's perspective on Windows, but git can
# still track 100644 (not executable) in the index. On Linux
# this matches the filesystem mode directly.
mode="$(git ls-files -s deploy/install-common.sh 2>/dev/null | awk '{ print $1 }')"
if [ "$mode" = "100644" ]; then
    ok "E.5 deploy/install-common.sh is source-only (git mode 100644, not 100755)"
else
    bad "E.5 deploy/install-common.sh must be git mode 100644 (sourced, not run). Current: $mode"
fi

# E.6 install-common.sh implements SHA256 verification
if grep -q 'sha256sum' deploy/install-common.sh 2>/dev/null; then
    ok "E.6 deploy/install-common.sh verifies SHA256 (the security-critical check)"
else
    bad "E.6 deploy/install-common.sh must verify SHA256 (security-critical)"
fi

# E.7 install-common.sh has a SKIP_VERIFY escape hatch
if grep -q 'SKIP_VERIFY' deploy/install-common.sh 2>/dev/null; then
    ok "E.7 deploy/install-common.sh has a SKIP_VERIFY escape hatch (air-gapped installs)"
else
    bad "E.7 deploy/install-common.sh must have a SKIP_VERIFY escape hatch"
fi

# E.8 systemd unit template in install-common.sh
if grep -q 'write_systemd_unit' deploy/install-common.sh 2>/dev/null; then
    ok "E.8 deploy/install-common.sh defines the systemd unit template (shared by debian+rh)"
else
    bad "E.8 deploy/install-common.sh must define the systemd unit template (shared by debian+rh)"
fi

# E.9 OpenRC service in install-alpine.sh
if grep -q '/etc/init.d/skygate' deploy/install-alpine.sh 2>/dev/null; then
    ok "E.9 deploy/install-alpine.sh writes the OpenRC service (Alpine-specific)"
else
    bad "E.9 deploy/install-alpine.sh must write /etc/init.d/skygate (OpenRC, not systemd)"
fi

# --- F. Windows installer (V6) ---

# F.1 Setup-Skygate-Win.ps1 exists
if [ -f deploy/Setup-Skygate-Win.ps1 ]; then
    ok "F.1 deploy/Setup-Skygate-Win.ps1 exists (V6 Windows installer)"
else
    bad "F.1 deploy/Setup-Skygate-Win.ps1 missing (V6 Windows installer)"
fi

# F.2 the PowerShell script is valid syntax (parse-only)
# This is a best-effort check — pwsh may not be on PATH in CI
# for non-Windows runners, in which case we skip the check.
if command -v powershell >/dev/null 2>&1; then
    if powershell -NoProfile -Command '
        $errors = $null
        $tokens = $null
        [void][System.Management.Automation.Language.Parser]::ParseFile(
            "deploy/Setup-Skygate-Win.ps1", [ref]$tokens, [ref]$errors)
        if ($errors) { $errors | ForEach-Object { Write-Host $_ }; exit 1 }
        else { exit 0 }
    ' 2>/dev/null; then
        ok "F.2 deploy/Setup-Skygate-Win.ps1 has valid PowerShell syntax"
    else
        bad "F.2 deploy/Setup-Skygate-Win.ps1 has PowerShell syntax errors"
    fi
else
    echo "  SKIP  F.2 Setup-Skygate-Win.ps1 syntax check (no powershell in PATH)"
fi

# F.3 the PowerShell script uses New-Service (the Windows-native service-registration)
if grep -q 'New-Service' deploy/Setup-Skygate-Win.ps1 2>/dev/null; then
    ok "F.3 deploy/Setup-Skygate-Win.ps1 uses New-Service (Windows-native service registration)"
else
    bad "F.3 deploy/Setup-Skygate-Win.ps1 must use New-Service (not sc.exe or NSSM)"
fi

# F.4 the PowerShell script writes a Windows env registry key
# (the Windows equivalent of systemd's EnvironmentFile=)
if grep -q 'CurrentControlSet.*Environment' deploy/Setup-Skygate-Win.ps1 2>/dev/null; then
    ok "F.4 deploy/Setup-Skygate-Win.ps1 writes the Environment registry key (Windows env-file equivalent)"
else
    bad "F.4 deploy/Setup-Skygate-Win.ps1 must write CurrentControlSet\\Services\\...\\Environment (Windows env-file equivalent)"
fi

# F.5 the PowerShell script downloads the windows-amd64 zip
if grep -q 'windows-amd64' deploy/Setup-Skygate-Win.ps1 2>/dev/null; then
    ok "F.5 deploy/Setup-Skygate-Win.ps1 downloads the windows-amd64 asset"
else
    bad "F.5 deploy/Setup-Skygate-Win.ps1 must download the windows-amd64 asset (matches release.yml naming)"
fi

# --- G. README documentation (V2 podman section) ---

# G.1 README has a "Deployment variants" section
if grep -q 'Deployment variants' README.md 2>/dev/null; then
    ok "G.1 README.md has a Deployment variants section"
else
    bad "G.1 README.md must have a Deployment variants section"
fi

# G.2 README has a Podman Compose section
if grep -qE 'Podman.*Compose' README.md 2>/dev/null; then
    ok "G.2 README.md has a Podman Compose section (V2)"
else
    bad "G.2 README.md must have a Podman Compose section (V2)"
fi

# G.3 README has a Windows section
if grep -qE 'Windows.*native|native.*Windows' README.md 2>/dev/null; then
    ok "G.3 README.md has a Windows native section (V6)"
else
    bad "G.3 README.md must have a Windows native section (V6)"
fi

# G.4 README has a bare-metal / systemd section
if grep -qE 'systemd|OpenRC' README.md 2>/dev/null; then
    ok "G.4 README.md has a bare-metal systemd/OpenRC section (V3+V4)"
else
    bad "G.4 README.md must have a bare-metal systemd/OpenRC section (V3+V4)"
fi

# G.5 README has a prebuilt-image compose section
if grep -q 'ghcr' README.md 2>/dev/null; then
    ok "G.5 README.md has a prebuilt-image (ghcr) compose section (V1)"
else
    bad "G.5 README.md must have a prebuilt-image (ghcr) compose section (V1)"
fi

# G.6 README has a lite / sky-only compose section
if grep -qE 'lite|sky-only' README.md 2>/dev/null; then
    ok "G.6 README.md has a lite / sky-only compose section (V5)"
else
    bad "G.6 README.md must have a lite / sky-only compose section (V5)"
fi

# --- H. Naming contract between release.yml + install scripts ---
# The asset naming convention in release.yml MUST match what
# install scripts + Windows installer + docker-compose expect.
# If the names drift, the installs fail at download time.

# H.1 release.yml uses the same tarball naming as install scripts
# (skygate-vX.Y.Z-linux-amd64.tar.gz). The pattern in release.yml
# uses a literal "skygate-" prefix + the version + "-" + the
# archive_dir (e.g. linux-amd64) + ".tar.gz".
if grep -qE 'skygate-v\$\{.*\}-\$\{.*archive_dir.*\}\.tar\.gz' .github/workflows/release.yml 2>/dev/null || \
   grep -qE 'skygate-v.+-linux-amd64\.tar\.gz' .github/workflows/release.yml 2>/dev/null; then
    ok "H.1 release.yml uses skygate-vX.Y.Z-<triple>.tar.gz (matches install-common.sh expectations)"
else
    bad "H.1 release.yml asset naming must match install-common.sh (skygate-vX.Y.Z-<triple>.tar.gz)"
fi

# H.2 release.yml uses the same windows zip naming as Setup-Skygate-Win.ps1
if grep -q 'skygate.*windows-amd64.*zip' .github/workflows/release.yml 2>/dev/null && \
   grep -q 'windows-amd64.*zip' deploy/Setup-Skygate-Win.ps1 2>/dev/null; then
    ok "H.2 release.yml and Setup-Skygate-Win.ps1 agree on the windows zip name"
else
    bad "H.2 release.yml and Setup-Skygate-Win.ps1 must agree on the windows zip name"
fi

# H.3 install-common.sh extracts the asset name from the URL the
# SAME WAY the SHA256SUMS file lists it (basename match)
if grep -q 'basename.*tarball_url' deploy/install-common.sh 2>/dev/null; then
    ok "H.3 install-common.sh looks up the SHA256 by asset basename (the only way the names can match)"
else
    bad "H.3 install-common.sh must look up the SHA256 by asset basename"
fi

# --- I. Build + tests ---
# (the actual install functionality is tested by the deploy-e2e
# job in CI; this just confirms the static checks pass)

# I.1 go build clean
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go build ./... 2>/dev/null; then
        ok "I.1 go build ./... clean (B237.15 didn't break the build)"
    else
        bad "I.1 go build ./... failed"
    fi
else
    echo "  SKIP  I.1 go build (no go in PATH)"
fi

# I.2 bash syntax check on the install scripts
for f in install.sh install-debian.sh install-rh.sh install-alpine.sh install-bare.sh install-common.sh; do
    if bash -n "deploy/${f}" 2>/dev/null; then
        ok "I.2 deploy/${f} has valid bash syntax"
    else
        bad "I.2 deploy/${f} has bash syntax errors"
    fi
done

# --- J. verify_pre_deploy.sh registration ---

# J.1 the check is registered in verify_pre_deploy.sh
if grep -q 'check_b237_15' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "J.1 scripts/verify_pre_deploy.sh includes the B237.15 check"
else
    bad "J.1 scripts/verify_pre_deploy.sh must include the B237.15 check (see the run_check block for B237.15)"
fi

# J.2 AGENTS.md mentions B237.15
if grep -q 'B237\.15' AGENTS.md 2>/dev/null; then
    ok "J.2 AGENTS.md documents B237.15 (so future agents know the deploy-variants work is pinned)"
else
    bad "J.2 AGENTS.md must document B237.15 (B-check conventions)"
fi

# --- Summary ---

echo
echo "=== B237.15 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
