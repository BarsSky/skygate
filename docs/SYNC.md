# Skygate Sync — между agent (192.168.13.69) и knaga (192.168.13.20)

## Текущая конфигурация
- **agent** (192.168.13.69) — Linux, skygate работает в Docker, bind-mount `/home/skyadmin/skygate`
- **knaga** (192.168.13.20) — Windows, Tailscale client, MagicDNS
- **bare repo** на Synology: `\\192.168.13.13\docker\git\skygate.git` (или `/mnt/synya/git/skygate.git`)

## Workflow

### 1. На agent: редактировать и тестировать
```bash
# All edits happen on agent — bind-mount to live skygate container
cd /home/skyadmin/skygate
# Edit Go/template files
sudo python3 /tmp/sg_patch*.py

# Build + test
sudo sed -i 's/^go 1.23$/go 1.22/' go.mod
GOTOOLCHAIN=local go build -o /tmp/skygate-test ./cmd/skygate
sudo sed -i 's/^go 1.22$/go 1.23/' go.mod
docker restart 0c8931e2a82a
# Verify with curl
```

### 2. Commit + push
```bash
cd /home/skyadmin/skygate
git add -A
git commit -m "Issue #X: description"
git push origin main
```

### 3. На knaga: pull
В **PowerShell** на knaga:
```powershell
cd C:\Projects\skygate
git pull origin main
```

### Если skygate на knaga ещё не клонирован:
```powershell
# One-time setup
mkdir C:\Projects
cd C:\Projects
git clone \\192.168.13.13\docker\git\skygate.git skygate
cd skygate
git checkout main
```

## ENV конфигурация (Skygate)
В `entrypoint.sh` или `docker-compose.yml`:
```bash
SKYGATE_MAX_RULES_PER_DEVICE=200
SKYGATE_MAX_TOTAL_RULES=10000
SKYGATE_STAGGER_SYNC=true
SKYGATE_STAGGER_BATCH_SIZE=20
SKYGATE_STAGGER_INTERVAL=30s
SKYGATE_DNS_AUTO_CHECK=5m
# Per-user: username:max_rules
SKYGATE_USER_MAX_RULES=skyadmin:1000,admin:500
```

## Что синхронизируется между agent и knaga
✅ Go source (cmd/, internal/)
✅ HTML templates
✅ Deploy scripts (deploy/, backup.sh)
✅ Docs (PROJECT.md, README.md)
✅ Static assets (static/css/, static/webfonts/)

❌ НЕ синхронизируется:
- skygate.db (per-host)
- .env (per-host, secret keys)
- *.bak* (backup files)
- Build artifacts
