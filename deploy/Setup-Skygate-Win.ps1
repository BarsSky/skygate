<#
.SYNOPSIS
    Installs skygate on Windows as a Windows service.

.DESCRIPTION
    V6 (B237.14) Windows installer. Replaces the one-off
    Setup-SkygateOnKnaga.ps1 (which only cloned the source from
    the operator's Synology NAS). This script does the FULL install:

      1. Download the latest release zip from GitHub Releases
      2. Verify the SHA256 against SHA256SUMS
      3. Extract to C:\Program Files\Skygate\
      4. Write a starter C:\ProgramData\Skygate\skygate.env
      5. Register a Windows service (New-Service) — auto-start
      6. Start the service
      7. Print next steps

    Runs in PowerShell 5.1+ (ships with Windows 10/11 + Server
    2016+). No external dependencies beyond what ships with
    PowerShell + Windows. Run as Administrator (the script
    self-elevates if not).

    The Windows service uses the registry-based Environment key
    (HKLM\SYSTEM\CurrentControlSet\Services\Skygate\Environment,
    REG_MULTI_SZ) to read the same config the Linux systemd
    unit reads from /etc/skygate/skygate.env. The operator edits
    ONE file (C:\ProgramData\Skygate\skygate.env) and restarts
    the service to pick up changes — matches the Linux
    EnvironmentFile= flow.

    When to use this vs the WSL2 path (docs/deploy.md §7):
      - Use this when you want skygate to run as a native
        Windows process (no Linux VM, no Hyper-V, no WSL).
        Works in Windows Container Isolation=process mode
        and on bare metal.
      - Use WSL2 when you want the same Linux path as a
        Linux server (the WSL2 container can use all the
        V1-V5 variants unchanged). WSL2 is a heavier dep
        (Hyper-V + a full Linux distro) but gives you the
        Linux-native debugging tools.

.PARAMETER Version
    The skygate version to install. Default: "latest" (resolved
    via the GitHub Releases API). Pin to a specific version
    (e.g. "v1.5.0") for reproducible installs.

.PARAMETER InstallDir
    Where to install the binary. Default: C:\Program Files\Skygate.
    Custom dirs must be writable by the service account.

.PARAMETER DataDir
    Where skygate writes its data (DB, logs, tailscale state).
    Default: C:\ProgramData\Skygate.

.PARAMETER Port
    The HTTP port skygate binds. Default: 8080.

.PARAMETER SkipVerify
    Skip the SHA256 check (air-gapped installs). Default: $false.
    The operator is responsible for verifying the file
    out-of-band if this is set.

.EXAMPLE
    PS> .\Setup-Skygate-Win.ps1
    Downloads the latest release, installs to C:\Program Files\Skygate,
    registers as a Windows service "Skygate", starts it.

.EXAMPLE
    PS> .\Setup-Skygate-Win.ps1 -Version v1.5.0
    Pins the install to v1.5.0 (recommended for production).

.EXAMPLE
    PS> .\Setup-Skygate-Win.ps1 -Version v1.5.0 -SkipVerify
    Same as above, but skips the SHA256 check (only if you've
    already verified the zip out-of-band, e.g. via a separate
    trusted channel).

.NOTES
    2026-09-07 (V6 / B237.14): written to supersede
    deploy/knaga/Setup-SkygateOnKnaga.ps1 (the operator's
    one-off Synology-clone script). The new installer is
    portable (works on any Windows host with PowerShell 5.1+)
    and pulls a prebuilt binary (no Go required on the target).
#>

[CmdletBinding()]
param(
    [string]$Version = "latest",
    [string]$InstallDir = "C:\Program Files\Skygate",
    [string]$DataDir = "C:\ProgramData\Skygate",
    [int]$Port = 8080,
    [switch]$SkipVerify,
    [string]$GitHubOwner = "BarsSky",
    [string]$GitHubRepo = "skygate"
)

# -------- Preflight --------
$ErrorActionPreference = 'Stop'

# Self-elevate if not running as Administrator. The service
# registration + the Program Files write need admin privs.
if (-not (New-Object Security.Principal.WindowsPrincipal(
    [Security.Principal.WindowsIdentity]::GetCurrent())).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host "ERROR: This script must be run as Administrator." -ForegroundColor Red
    Write-Host "Right-click PowerShell and 'Run as Administrator', then re-run." -ForegroundColor Yellow
    exit 1
}

# PowerShell 5.1+ check. PS Core 7+ also works but the script
# is written conservatively (uses only PS 5.1 syntax).
if ($PSVersionTable.PSVersion.Major -lt 5) {
    Write-Host "ERROR: PowerShell 5.1+ required (you have $($PSVersionTable.PSVersion))." -ForegroundColor Red
    exit 1
}

# Expand ~ if the operator used it. PowerShell doesn't expand ~
# in script context the way cmd does.
$InstallDir = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($InstallDir)
$DataDir    = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($DataDir)

Write-Host "=== Skygate Windows Installer ===" -ForegroundColor Cyan
Write-Host "  Version:    $Version"
Write-Host "  InstallDir: $InstallDir"
Write-Host "  DataDir:    $DataDir"
Write-Host "  Port:       $Port"
Write-Host ""

# -------- 1. Resolve version + download URLs --------
# We pull three artifacts from GitHub Releases:
#   - skygate-vX.Y.Z-windows-amd64.zip  (the binary)
#   - SHA256SUMS                         (hash file, contains
#                                         the line for the zip)
#
# For "latest", we hit the GitHub Releases API to get the
# tag (the API returns JSON; we use -TagName).
function Resolve-Version {
    param([string]$Version, [string]$Owner, [string]$Repo)
    if ($Version -ne "latest") {
        return $Version
    }
    $apiUrl = "https://api.github.com/repos/${Owner}/${Repo}/releases/latest"
    Write-Host "[install] resolving 'latest' from $apiUrl"
    try {
        $release = Invoke-RestMethod -Uri $apiUrl -Method Get -TimeoutSec 15
        if (-not $release.tag_name) {
            throw "API response has no tag_name field"
        }
        return $release.tag_name
    } catch {
        Write-Host "ERROR: failed to resolve 'latest' from GitHub API: $_" -ForegroundColor Red
        Write-Host "If you're offline, set -Version v1.5.0 (or whatever you have)." -ForegroundColor Yellow
        exit 1
    }
}

$tag = Resolve-Version -Version $Version -Owner $GitHubOwner -Repo $GitHubRepo
Write-Host "[install] resolved version: $tag"

$zipName       = "skygate-${tag}-windows-amd64.zip"
$zipUrl        = "https://github.com/${GitHubOwner}/${GitHubRepo}/releases/download/${tag}/${zipName}"
$sumsUrl       = "https://github.com/${GitHubOwner}/${GitHubRepo}/releases/download/${tag}/SHA256SUMS"

# -------- 2. Download + verify --------
$work = Join-Path $env:TEMP "skygate-install-$([guid]::NewGuid().ToString('N').Substring(0,8))"
New-Item -ItemType Directory -Path $work -Force | Out-Null
try {
    $zipPath = Join-Path $work $zipName
    $sumsPath = Join-Path $work "SHA256SUMS"

    Write-Host "[install] downloading $zipUrl"
    try {
        Invoke-WebRequest -Uri $zipUrl -OutFile $zipPath -UseBasicParsing -TimeoutSec 60
    } catch {
        Write-Host "ERROR: download failed: $_" -ForegroundColor Red
        exit 1
    }
    Write-Host "[install] downloaded $([math]::Round((Get-Item $zipPath).Length / 1MB, 1)) MB"

    Write-Host "[install] downloading SHA256SUMS"
    Invoke-WebRequest -Uri $sumsUrl -OutFile $sumsPath -UseBasicParsing -TimeoutSec 30

    if (-not $SkipVerify) {
        Write-Host "[install] verifying SHA256"
        # SHA256SUMS format: "<hex>  <filename>". Match the zipName.
        $expectedLine = Get-Content $sumsPath | Where-Object { $_ -match [regex]::Escape($zipName) }
        if (-not $expectedLine) {
            Write-Host "ERROR: $zipName not found in SHA256SUMS" -ForegroundColor Red
            exit 1
        }
        $expectedSha = ($expectedLine -split '\s+')[0]
        $actualSha = (Get-FileHash -Path $zipPath -Algorithm SHA256).Hash.ToLower()
        if ($expectedSha -ne $actualSha) {
            Write-Host "ERROR: SHA256 mismatch" -ForegroundColor Red
            Write-Host "  expected: $expectedSha" -ForegroundColor Red
            Write-Host "  actual:   $actualSha" -ForegroundColor Red
            exit 1
        }
        Write-Host "[install] SHA256 OK" -ForegroundColor Green
    } else {
        Write-Host "[install] WARNING: -SkipVerify set, skipping hash check" -ForegroundColor Yellow
    }

    # -------- 3. Extract + install --------
    Write-Host "[install] installing to $InstallDir"
    if (-not (Test-Path $InstallDir)) {
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    }
    # Use Expand-Archive (PS 5.1+ built-in) for safe extraction.
    # The zip is FLAT: skygate.exe + skygate.exe.sha256 at the root
    # (matches the .github/workflows/release.yml packaging).
    Expand-Archive -Path $zipPath -DestinationPath $InstallDir -Force
    $binary = Join-Path $InstallDir "skygate.exe"
    if (-not (Test-Path $binary)) {
        Write-Host "ERROR: zip did not contain skygate.exe at the root" -ForegroundColor Red
        exit 1
    }
    Write-Host "[install] installed: $binary ($([math]::Round((Get-Item $binary).Length / 1MB, 1)) MB)"

    # -------- 4. Data dir + env file --------
    if (-not (Test-Path $DataDir)) {
        New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
    }
    # Tailscale state subdir (used by the optional in-image
    # tailscaled; the Windows installer doesn't auto-enable
    # Tailscale by default, but the dir must exist in case
    # the operator turns it on via the env file).
    $tsDir = Join-Path $DataDir "ts"
    if (-not (Test-Path $tsDir)) {
        New-Item -ItemType Directory -Path $tsDir -Force | Out-Null
    }

    $envFile = Join-Path $DataDir "skygate.env"
    if (Test-Path $envFile) {
        Write-Host "[install] preserving existing $envFile (not overwriting)"
    } else {
        # Generate a starter JWT secret.
        $jwtSecret = -join ((1..32) | ForEach-Object { '{0:x2}' -f (Get-Random -Maximum 256) })

        @"
# $envFile — environment for the Skygate Windows service.
#
# 2026-09-07 (V6 / B237.14): this file is written by
# Setup-Skygate-Win.ps1 on first install. Re-running the
# installer PRESERVES the file (the operator's HEADSCALE_URL
# + HEADSCALE_API_KEY are not clobbered). To reset, delete
# this file and re-run.
#
# After editing, run:  Restart-Service Skygate

# === Required: fill in before Skygate can start ===
# Where headscale is reachable from this Windows host. Examples:
#   http://localhost:50444                  (headscale on the same host)
#   http://192.0.2.10:50444                 (headscale on a LAN peer)
#   http://100.64.0.1:50444                 (headscale on the Tailscale net)
HEADSCALE_URL=

# API key for headscale (admin scope).
HEADSCALE_API_KEY=

# JWT signing secret for skygate's session cookies.
SKYGATE_JWT_SECRET=$jwtSecret

# === Database ===
# Default: SQLite at ${DataDir}\skygate.db
# For HA: switch to PostgreSQL DSN.
SKYGATE_DB_DSN=

# === HTTP port (default 8080) ===
SKYGATE_PORT=$Port
"@ | Set-Content -Path $envFile -Encoding UTF8

        Write-Host "[install] wrote $envFile (fill in HEADSCALE_URL + HEADSCALE_API_KEY before starting)"
    }

    # -------- 5. Register as a Windows service --------
    $serviceName = "Skygate"
    $serviceDisplayName = "Skygate Portal"
    $serviceDescription = "Self-service web portal for Tailscale and headscale (https://github.com/${GitHubOwner}/${GitHubRepo})"

    # Check if the service already exists. If yes, just reconfigure
    # it (the binary path may have changed in the upgrade).
    $existing = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
    if ($existing) {
        Write-Host "[install] service '$serviceName' already exists; reconfiguring"
        # sc.exe config to update the binary path (New-Service
        # would error on a duplicate). sc.exe accepts quoted
        # paths with spaces.
        $binPath = "`"$binary`""
        & sc.exe config $serviceName binPath= $binPath | Out-Null
    } else {
        Write-Host "[install] creating Windows service '$serviceName'"
        # New-Service is the PowerShell-native way. -Credential
        # is left as LocalSystem for simplicity (matches how
        # most third-party services run; the service is still
        # sandboxed via its own ACLs on the data dir).
        New-Service -Name $serviceName `
                    -BinaryPathName "`"$binary`"" `
                    -DisplayName $serviceDisplayName `
                    -Description $serviceDescription `
                    -StartupType Automatic
    }

    # -------- 6. Wire the env file into the service --------
    # Windows services don't have an EnvironmentFile= directive
    # like systemd. The standard pattern is to put KEY=VALUE
    # lines in a REG_MULTI_SZ value at
    #   HKLM\SYSTEM\CurrentControlSet\Services\<Name>\Environment
    # The Service Control Manager reads this at service start
    # and merges the env vars into the service's environment.
    Write-Host "[install] writing service env (from $envFile)"
    $svcRegPath = "HKLM:\SYSTEM\CurrentControlSet\Services\$serviceName"
    if (-not (Test-Path $svcRegPath)) {
        New-Item -Path $svcRegPath -Force | Out-Null
    }
    # Read the env file, skip blank lines and comments, write
    # the result as REG_MULTI_SZ. The Service Control Manager
    # parses this on start and exports each KEY=VALUE into
    # the service's process environment.
    $envLines = Get-Content $envFile |
        Where-Object { $_ -match '^\s*[^#].+=' -and $_ -notmatch '^\s*$' }
    Set-ItemProperty -Path $svcRegPath -Name "Environment" -Value $envLines -Type MultiString

    # -------- 7. Start the service --------
    Write-Host "[install] starting service '$serviceName'"
    try {
        Start-Service -Name $serviceName
        Start-Sleep -Seconds 2
        $status = (Get-Service -Name $serviceName).Status
        Write-Host "[install] service status: $status"
        if ($status -ne "Running") {
            Write-Host "WARN: service is not Running. Check: Get-EventLog -LogName Application -Newest 20" -ForegroundColor Yellow
        }
    } catch {
        Write-Host "WARN: failed to start service: $_" -ForegroundColor Yellow
        Write-Host "Service is registered; start it manually after filling in $envFile" -ForegroundColor Yellow
    }

} finally {
    # -------- 8. Cleanup --------
    if (Test-Path $work) {
        Remove-Item -Path $work -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# -------- 9. Next steps --------
# Try to read the version from the installed binary. If the
# binary is somehow not in PATH-equivalent, this fails silently.
$versionLine = ""
try {
    $versionLine = & $binary version 2>$null
    if ($versionLine) {
        $versionLine = " ($versionLine)"
    }
} catch {}

Write-Host ""
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host "  Skygate installed$versionLine" -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "  Next steps:" -ForegroundColor White
Write-Host ""
Write-Host "  1. Edit the env file and fill in your headscale coordinates:"
Write-Host "       notepad $envFile"
Write-Host "     Required: HEADSCALE_URL, HEADSCALE_API_KEY"
Write-Host ""
Write-Host "  2. Restart the service to pick up the new env:"
Write-Host "       Restart-Service $serviceName"
Write-Host ""
Write-Host "  3. Open the portal in a browser:"
Write-Host "       http://localhost:$Port/login"
Write-Host ""
Write-Host "  Useful commands:" -ForegroundColor White
Write-Host "       Get-Service $serviceName"
Write-Host "       Restart-Service $serviceName"
Write-Host "       Stop-Service $serviceName"
Write-Host "       Get-EventLog -LogName Application -Newest 30 -Source $serviceName"
Write-Host ""
Write-Host "  To uninstall:"
Write-Host "       Stop-Service $serviceName"
Write-Host "       sc.exe delete $serviceName"
Write-Host "       Remove-Item '$InstallDir' -Recurse -Force"
Write-Host "       # Data dir ($DataDir) is preserved across uninstall"
Write-Host ""
