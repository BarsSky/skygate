# One-time setup: clone skygate from Synology bare repo
# Run as Administrator in PowerShell on knaga

$synology = "\\192.168.13.13\docker"
$projects = "C:\Projects"
$repo = "$projects\skygate"

Write-Host "=== Skygate Setup on knaga ===" -ForegroundColor Cyan

# Create C:\Projects
if (-not (Test-Path $projects)) {
    New-Item -ItemType Directory -Path $projects -Force
    Write-Host "Created $projects"
}

# Check if skygate already cloned
if (Test-Path $repo) {
    Write-Host "skygate already exists at $repo" -ForegroundColor Yellow
    Write-Host "Run 'cd $repo && git pull' to sync"
    exit 0
}

# Test Synology access
if (-not (Test-Path $synology)) {
    Write-Host "ERROR: Cannot access $synology" -ForegroundColor Red
    Write-Host "Make sure Synology SMB share is mapped or accessible"
    exit 1
}

# Clone
try {
    Set-Location $projects
    git clone "$synology\git\skygate.git" skygate
    Set-Location $repo
    git checkout main
    git log --oneline -3
    Write-Host ""
    Write-Host "=== SUCCESS ===" -ForegroundColor Green
    Write-Host "Skygate source cloned to $repo"
    Write-Host "Edit files, commit, and push:"
    Write-Host "  cd $repo"
    Write-Host "  git add ."
    Write-Host "  git commit -m 'description'"
    Write-Host "  git push origin main"
} catch {
    Write-Host "ERROR: $_" -ForegroundColor Red
    exit 1
}
