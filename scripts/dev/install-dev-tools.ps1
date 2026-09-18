# install-dev-tools.ps1 — установка инструментов разработки/отладки skygate.
#
# Выбрано оператором 2026-09-17 (см.
# docs/ROADMAP.md §11).
#
# Идемпотентен: повторный запуск не ломает уже установленное.
# Каждый шаг обёрнут в try/catch — падение одного инструмента не мешает
# остальным, в конце печатается сводка «что стоит / чего нет».
#
# Запуск:
#   pwsh -File scripts/dev/install-dev-tools.ps1
#   pwsh -File scripts/dev/install-dev-tools.ps1 -SkipMCP      # без MCP-плагинов
#   pwsh -File scripts/dev/install-dev-tools.ps1 -Only sqlite3  # один инструмент

[CmdletBinding()]
param(
    [switch]$SkipMCP,
    [string[]]$Only = @()
)

$ErrorActionPreference = 'Continue'
$results = [ordered]@{}

function Test-Want([string]$name) {
    if ($Only.Count -eq 0) { return $true }
    return $Only -contains $name
}

function Get-Tool([string]$name) {
    $c = Get-Command $name -ErrorAction SilentlyContinue
    if ($c) { return $c.Source }
    return $null
}

function Report([string]$name, [string]$status, [string]$detail = '') {
    $results[$name] = "$status $detail"
    $color = switch -Wildcard ($status) {
        'OK*'      { 'Green' }
        'SKIP*'    { 'DarkGray' }
        'MISSING*' { 'Yellow' }
        default    { 'Red' }
    }
    Write-Host ("  [{0,-8}] {1} {2}" -f $status, $name, $detail) -ForegroundColor $color
}

Write-Host "`n=== skygate dev tools ===" -ForegroundColor Cyan
$goPath = Get-Tool go
if (-not $goPath) { $goPath = 'НЕ НАЙДЕН' }
Write-Host "Go: $goPath"
Write-Host ""

# ── 1. sqlite3 CLI — без него нельзя проверить схему SQLite ────────────────
if (Test-Want 'sqlite3') {
    if (Get-Tool sqlite3) {
        Report 'sqlite3' 'OK' (Get-Tool sqlite3)
    } else {
        Write-Host "  sqlite3: ставим через winget..." -ForegroundColor Cyan
        try {
            winget install --id SQLite.SQLite --accept-source-agreements --accept-package-agreements --silent
            Report 'sqlite3' ($(if (Get-Tool sqlite3) { 'OK' } else { 'MISSING' })) 'перезапустите терминал, чтобы PATH обновился'
        } catch {
            Report 'sqlite3' 'MISSING' "winget не сработал: $($_.Exception.Message). Вручную: https://sqlite.org/download.html"
        }
    }
}

# ── 2. psql / pgcli ───────────────────────────────────────────────────────
if (Test-Want 'psql') {
    if (Get-Tool psql) {
        Report 'psql' 'OK' (Get-Tool psql)
    } else {
        try {
            winget install --id PostgreSQL.PostgreSQL.15 --accept-source-agreements --accept-package-agreements --silent
            Report 'psql' ($(if (Get-Tool psql) { 'OK' } else { 'MISSING' })) 'или используйте postgres внутри docker compose'
        } catch {
            Report 'psql' 'MISSING' "альтернатива: docker run --rm -it postgres:15 psql"
        }
    }
}

# ── 3. Go-инструменты (golangci-lint, gosec, govulncheck, dlv) ────────────
$goTools = @(
    @{ Name = 'golangci-lint'; Pkg = 'github.com/golangci/golangci-lint/cmd/golangci-lint@latest'; Why = 'staticcheck/sqlclosecheck/rowserrcheck — ловит append без значений, утечки rows' },
    @{ Name = 'gosec';         Pkg = 'github.com/securego/gosec/v2/cmd/gosec@latest';                 Why = 'http.Error(err.Error()), инъекции в exec, weak crypto' },
    @{ Name = 'govulncheck';   Pkg = 'golang.org/x/vuln/cmd/govulncheck@latest';                      Why = 'уязвимости modernc.org/sqlite, pgx' },
    @{ Name = 'dlv';           Pkg = 'github.com/go-delve/delve/cmd/dlv@latest';                      Why = 'отладка http-хендлеров и ssh-вызовов' },
    @{ Name = 'sqlc';          Pkg = 'github.com/sqlc-dev/sqlc/cmd/sqlc@latest';                      Why = 'пилот: типобезопасная генерация запросов, структурно убивает диалект-протечки' }
)
foreach ($t in $goTools) {
    if (-not (Test-Want $t.Name)) { continue }
    if (Get-Tool $t.Name) { Report $t.Name 'OK' (Get-Tool $t.Name); continue }
    if (-not (Get-Tool go)) { Report $t.Name 'SKIP' 'нет go в PATH'; continue }
    Write-Host "  $($t.Name): go install..." -ForegroundColor Cyan
    try {
        & go install $t.Pkg 2>&1 | Out-Null
        $bin = Join-Path (& go env GOPATH) "bin\$($t.Name).exe"
        if (Test-Path $bin) { Report $t.Name 'OK' $bin }
        else { Report $t.Name 'MISSING' "установлен, но не в PATH: добавьте $((& go env GOPATH))\bin" }
    } catch {
        Report $t.Name 'MISSING' $_.Exception.Message
    }
    Write-Host "      зачем: $($t.Why)" -ForegroundColor DarkGray
}

# ── 4. shellcheck — 434 shell-скрипта в репозитории ───────────────────────
if (Test-Want 'shellcheck') {
    if (Get-Tool shellcheck) {
        Report 'shellcheck' 'OK' (Get-Tool shellcheck)
    } else {
        try {
            winget install --id koalaman.shellcheck --accept-source-agreements --accept-package-agreements --silent
            Report 'shellcheck' ($(if (Get-Tool shellcheck) { 'OK' } else { 'MISSING' })) ''
        } catch {
            Report 'shellcheck' 'MISSING' 'https://github.com/koalaman/shellcheck/releases'
        }
    }
}

# ── 5. git-bash (нужен для B-чеков на Windows) ────────────────────────────
if (Test-Want 'bash') {
    if (Get-Tool bash) { Report 'bash' 'OK' (Get-Tool bash) }
    else { Report 'bash' 'MISSING' 'поставьте Git for Windows (git-bash) — scripts/check_b*.sh требуют bash' }
}

# ── 6. MCP-плагины (PostgreSQL / SQLite / GitHub) ────────────────────────
if (-not $SkipMCP -and (Test-Want 'mcp')) {
    if (-not (Get-Tool npm)) {
        Report 'MCP' 'SKIP' 'нет npm — MCP-серверы ставятся через npx/npm'
    } else {
        $mcps = @(
            @{ Name = 'mcp-postgres'; Pkg = '@modelcontextprotocol/server-postgres'; Why = 'инспекция схемы/данных PG напрямую' },
            @{ Name = 'mcp-sqlite';   Pkg = '@modelcontextprotocol/server-sqlite';   Why = 'инспекция skygate.db напрямую' },
            @{ Name = 'mcp-github';   Pkg = '@modelcontextprotocol/server-github';   Why = 'issues/PR проекта' }
        )
        foreach ($m in $mcps) {
            Write-Host "  $($m.Name): npm install -g $($m.Pkg)" -ForegroundColor Cyan
            try {
                & npm install -g $m.Pkg 2>&1 | Out-Null
                if ($LASTEXITCODE -eq 0) { Report $m.Name 'OK' "зачем: $($m.Why)" }
                else { Report $m.Name 'MISSING' "npm exit $LASTEXITCODE" }
            } catch {
                Report $m.Name 'MISSING' $_.Exception.Message
            }
        }
        Write-Host "`n  MCP-серверы нужно прописать в конфиге DSH (см. ~/.dsh/settings.yaml) с" -ForegroundColor DarkGray
        Write-Host "  реальными DSN/токенами — скрипт их не подставляет намеренно." -ForegroundColor DarkGray
    }
}

# ── Сводка ───────────────────────────────────────────────────────────────
Write-Host "`n=== Сводка ===" -ForegroundColor Cyan
foreach ($k in $results.Keys) { Write-Host ("  {0,-16} {1}" -f $k, $results[$k]) }

Write-Host "`nДальше:" -ForegroundColor Cyan
Write-Host "  1. go vet ./...            # СЕЙЧАС КРАСНЫЙ: internal/feature/admin/derp.go:810"
Write-Host "  2. go test ./internal/db/... -count=1"
Write-Host "  3. golangci-lint run ./... # после починки vet"
Write-Host "  4. Скиллы: .dsh/skills/ (skygate-db-dialect-audit и др.)"
Write-Host ""
