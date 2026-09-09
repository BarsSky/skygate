# Plugin API + Tailscale как Module #1
**Дата:** 2026-09-09
**Контекст:** Архитектурный сдвиг от "монолит с in-tree фичами" к "ядро + plugin API + модули". Tailscale — первый модуль, доказательство концепции plugin API.
**Scope:** Внутренняя архитектура. Не меняет public API skygate для пользователей.

---

## 1. Цель

Зафиксировать **принцип** (Tailscale = канал, не контроль) в коде через **plugin API**:
- Модули — **opt-in** (default = не установлен)
- 3 install modes (in-container / OS-level / none) — выбираются detection'ом
- Sub-features внутри модуля (cluster / telegram / derp / exit) — отдельные feature flags
- Lifecycle управляется **Manager** (Init → Start → periodic Health → Stop)
- State persistence в `/var/lib/skygate/modules/<name>/state.json` (audit + crash recovery)
- Admin panel `/admin/modules` — единая control surface для всех модулей

## 2. Архитектурный принцип

> **Tailscale = канал связи для модулей, не контроль над системой.**

Отсюда:
- skygate НЕ требует root на хосте для базовой работы
- Tailscale поднимается только когда хотя бы один sub-feature реально нужен
- Внутри Docker — изоляция (отдельный sidecar контейнер)
- На ОС — только когда модуль критически зависит (HA standby → OS-level обязателен)
- Admin panel = control surface (вижу статус, жму кнопку, получаю результат)

## 3. Module interface (Go)

```go
// internal/module/module.go

type Module interface {
    // Name returns a stable identifier ("tailscale", "headscale_bot", "derp", ...).
    Name() string

    // Init validates prerequisites + loads persistent state.
    // Does NOT start the module — that's Start().
    Init(ctx context.Context, cfg ModuleConfig) error

    // Start brings the module up (e.g. spawn tailscaled, write authkey, run tailscale up).
    // Idempotent: calling Start on a running module is a no-op.
    Start(ctx context.Context) error

    // Stop brings the module down gracefully.
    // Idempotent: calling Stop on a stopped module is a no-op.
    Stop(ctx context.Context) error

    // Status returns a snapshot of the module's runtime state.
    Status() ModuleStatus

    // Health returns a structured health report (for /healthz + per-module health).
    Health() HealthStatus

    // SubFeatures lists the optional sub-features the module can enable.
    // Empty for modules without sub-features.
    SubFeatures() []SubFeature

    // EnableSubFeature / DisableSubFeature — module-specific side effects.
    EnableSubFeature(ctx context.Context, name string) error
    DisableSubFeature(ctx context.Context, name string) error
}

type ModuleConfig struct {
    DataDir    string            // /var/lib/skygate/modules/<name>/
    SocketDir  string            // /var/run/skygate/modules/<name>/
    Env        map[string]string // SKYGATE_TS_* env vars (already loaded)
    DBC        func() *sql.DB    // for modules that need DB access
    AuditLog   func(action, detail string) // for action audit
}

type ModuleStatus struct {
    State       string            // "installed" | "running" | "stopped" | "error" | "not_installed"
    InstalledAt time.Time
    StartedAt   time.Time
    LastError   string
    Info        map[string]string // module-specific status (e.g. "tailscale_ip": "100.64.0.22")
}

type HealthStatus struct {
    Healthy   bool
    LastCheck time.Time
    Checks    map[string]bool   // "auth_ok", "interface_up", "peers_visible", ...
    LastError string
}

type SubFeature struct {
    Name        string // "cluster", "telegram", "derp", "exit"
    Description string
    Enabled     bool   // current state
    Requires    []string // other sub-features this depends on (e.g. "exit" requires "cluster")
    Impact      string // "OS-level install required", "in-container only", "no extra cost"
}
```

## 4. Manager (lifecycle orchestration)

```go
// internal/module/manager.go

type Manager struct {
    modules   map[string]Module
    configs   map[string]ModuleConfig
    state     *ManagerState
    auditLog  func(action, detail string)
    mu        sync.RWMutex
}

func NewManager(auditLog func(string, string)) *Manager

// Register adds a module to the manager. Must be called before InitAll.
func (m *Manager) Register(mod Module) error

// InitAll initializes every registered module.
// Modules that fail Init are marked as "error" but don't block other modules.
func (m *Manager) InitAll(ctx context.Context) error

// StartEnabled starts modules whose state.Enabled=true.
// Reads the persistent state to decide what's "enabled".
func (m *Manager) StartEnabled(ctx context.Context) error

// StopAll gracefully stops every running module.
func (m *Manager) StopAll(ctx context.Context) error

// Get returns a module by name (for HTTP handlers).
func (m *Manager) Get(name string) (Module, bool)

// List returns all registered modules + their current status.
func (m *Manager) List() []ModuleInfo

// EnableSubFeature / DisableSubFeature — proxy to the named module.
func (m *Manager) EnableSubFeature(ctx context.Context, moduleName, subName string) error
```

**Lifecycle порядок** (boot):
1. `Register()` всех модулей (compile-time, в `init()` или explicit в `main.go`)
2. `InitAll()` — validate prerequisites, load state
3. `StartEnabled()` — start модули с `state.Enabled=true` (persisted)
4. Periodic `Health()` checks (каждые 30s) → update state + audit log if health changes

**Shutdown порядок**:
1. `StopAll()` — graceful shutdown (timeout 30s)
2. Persist final state

## 5. State persistence

Per-module state: `/var/lib/skygate/modules/<name>/state.json`
```json
{
    "name": "tailscale",
    "enabled": true,
    "installed_at": "2026-09-09T14:30:00Z",
    "started_at": "2026-09-09T14:30:05Z",
    "install_mode": "in_container",
    "sub_features": {
        "cluster": false,
        "telegram": true,
        "derp": false,
        "exit": false
    },
    "last_health": {
        "ts": "2026-09-09T14:35:00Z",
        "healthy": true,
        "checks": {"auth_ok": true, "interface_up": true}
    },
    "last_error": ""
}
```

Atomic write (write to .tmp, fsync, rename) — same pattern as B-new ha-state.

## 6. Sub-features pattern

Внутри Tailscale модуля 4 sub-features:

| Sub-feature | Что делает | Обязательный install mode | Зависимости |
|---|---|---|---|
| `cluster` | HA mesh между primary/standby | OS-level на standby, in-container на primary | — |
| `telegram` | Telegram API relay через Tailscale | in-container | — |
| `derp` | Свой DERP relay (отдельный контейнер) | отдельный `derper` контейнер, не клиент | — |
| `exit` | Exit-node / subnet router | OS-level (нужен IP forwarding) | `cluster` |

Каждый sub-feature — bool flag в state.json + env var override (`SKYGATE_TS_CLUSTER=true`).

## 7. Admin panel

Новые страницы:
- `/admin/modules` — список всех модулей + статус (table view)
- `/admin/modules/{name}` — детальный view одного модуля
- `/admin/modules/{name}/install` — POST endpoint для install
- `/admin/modules/{name}/enable` — POST endpoint для enable/disable
- `/admin/modules/{name}/subfeatures` — POST endpoint для sub-feature toggle

**Существующий** `/admin/tailscale` (low-level control: paste key, start/stop tailscaled) — **сохраняется** без изменений. Это low-level escape hatch, модуль — это layer сверху.

## 8. Detection (3 install modes)

В `deploy/install-common.sh` (новый helper):
```bash
detect_install_mode() {
    # Mode B: in-container (Docker path, default для primary)
    if [ -S /var/run/docker.sock ] && [ -f /var/lib/skygate/modules/tailscale/state.json ]; then
        echo "in_container"; return
    fi
    # Mode C: OS-level (только для HA standby)
    if [ "${SKYGATE_HA_ROLE:-primary}" = "standby" ]; then
        echo "os_level"; return
    fi
    # Mode A: none (default, если ничего не нужно)
    echo "none"
}
```

В `cmd/skygate/main.go`:
```go
installMode := detectInstallMode()  // "in_container" | "os_level" | "none"
tailscaleMod := tailscale.NewModule(installMode)
manager.Register(tailscaleMod)
```

## 9. Tailscale как Module #1

`internal/module/tailscale/module.go`:
- Wraps существующий in-container Tailscale admin UI
- Добавляет: install modes (3), sub-features (4), state persistence
- Не ломает `/admin/tailscale` (он остаётся как low-level control)

Install logic:
- `in_container`: создать sidecar контейнер `skygate-tailscale` (через `docker compose -f docker-compose.tailscale.yml up -d`)
- `os_level`: запустить `deploy/install-tailscale.sh` (apt install + systemd)
- `none`: ничего (default)

## 10. Roadmap (B-блоки)

| B-блок | Описание | Размер | Commit |
|---|---|---|---|
| **B-mod-core** | `internal/module/` (interface, manager, registry, state) | ~400 строк | TBD |
| **B-mod-tailscale** | Tailscale как Module #1 (3 install modes, 4 sub-features) | ~600 строк | TBD |
| **B-mod-admin** | `/admin/modules` + `/admin/modules/tailscale` + templates + i18n | ~500 строк | TBD |
| **B-mod-install** | `deploy/install-tailscale.sh` + integration в install-debian.sh + bootstrap_standby.sh fallback + docker-compose.tailscale.yml | ~250 строк | TBD |
| **B-mod-bcheck** | `check_b_module_core.sh` (12 contracts) + `check_b_tailscale_module.sh` (16 contracts) + register в verify_pre_deploy.sh | ~350 строк | TBD |
| **B-mod-cluster** | cluster sub-feature: использует tailscale module для HA mesh | ~200 строк | deferred |
| **B-mod-telegram** | telegram sub-feature: relay для Telegram API | ~200 строк | deferred |
| **B-mod-derp** | derp sub-feature: отдельный `derper` контейнер | ~200 строк | deferred |
| **B-mod-exit** | exit sub-feature: subnet router | ~200 строк | deferred |

**Phase order:** core → tailscale → install → admin → bcheck → sub-features (cluster → telegram → derp → exit).

## 11. B-check coverage (target)

`check_b_module_core.sh` — 12 contracts:
- Module interface существует (Name/Init/Start/Stop/Status/Health/SubFeatures)
- Manager: NewManager, Register, InitAll, StartEnabled, StopAll, Get, List
- State persistence: atomic write (fsync), JSON parse validation, crash recovery
- Audit log вызывается на Init/Start/Stop/Enable/Disable
- SubFeatures dependency validation (не enable `exit` без `cluster`)

`check_b_tailscale_module.sh` — 16 contracts:
- `TailscaleModule` реализует `Module` interface
- 3 install modes: in_container / os_level / none
- 4 sub-features: cluster / telegram / derp / exit
- State.json schema: name, enabled, install_mode, sub_features
- Refactor: `internal/feature/admin/tailscale.go` использует `module/tailscale.Module`
- B179 safety: `--netfilter-mode=nodir` (не `off`) в os-level install
- `--netfilter-mode=off` НЕ используется (regression guard)
- Audit log: `module.tailscale.install`, `module.tailscale.start`, `module.tailscale.subfeature.enable`

## 12. Что НЕ делаем (deferred)

- Динамические плагины (`.so` загрузка) — только static registration через `Register()`
- Multi-region / multi-DC
- Module marketplace
- Cross-module dependency resolution (кроме sub-feature deps внутри одного модуля)
- Module-level metrics (Prometheus) — пока только audit log

## 13. Known issues

- `internal/feature/admin/tailscale.go` уже 63KB — refactor на module API будет большой diff
- Sidecar `docker-compose.tailscale.yml` нужно проектировать (TUN device, capabilities, network)
- `--netfilter-mode=nodir` (B179) — обязательно для OS-level install
- Audit log при boot может быть шумным (Start каждого модуля) — нужно фильтровать только state changes
