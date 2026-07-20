package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port               string
	DBPath             string
	HeadscaleURL       string
	HeadscaleKey       string
	// HeadplaneExternalURL is the public URL of an existing
	// Headplane instance the operator wants to use instead of
	// the bundled sidecar. Empty = use the bundled sidecar.
	// See docs/headplane.md "Use an existing Headplane" for
	// the full contract.
	HeadplaneExternalURL string
	ControlURL           string // human-facing URL clients connect to (e.g. https://head.skynas.ru)
	JWTSecret          string
	SessionHours       int
	BootstrapAdminUser string
	BootstrapAdminPass string
	SSHKeyPath         string // path to SSH key for exit node sync
	DNSAutoCheck       time.Duration // 0 = disabled, default 5m
	// 2026-07-07: issue #12 — limits & staggered sync
	MaxRulesPerDevice int           // 0 = no limit; default 200
	MaxTotalRules     int           // 0 = no limit; default 10000
	StaggerSync       bool          // split autoupdate work into batches
	StaggerBatchSize  int           // rules per batch (default 20)
	StaggerInterval   time.Duration // delay between batches (default 30s)
	// 2026-07-07: per-user rule limits. Map username -> max rules. Default = MaxRulesPerDevice.
	// Example: "SKYGATE_USER_MAX_RULES=skyadmin:1000,admin:500"
	UserMaxRules       map[string]int
	// 2026-07-15: v0.12.0 — per-user headscale control plane
	// keys are encrypted with this 32-byte hex key. Empty
	// means "encryption not configured"; the per-user router
	// falls through to the global client (operators who
	// haven't enabled per-user planes see no change).
	SecretKeyHex string
	// 2026-07-15: v0.13.0 — exit-node health monitor. The
	// monitor runs in a background goroutine and ticks every
	// ExitNodeCheckInterval (default 5 min). ExitNodeOnStartup
	// (default true) runs an immediate pre-tick at boot so a
	// fresh skygate that starts when all exit-nodes are down
	// sends the "0 healthy" alert right away. ExitNodeOfflineAfter
	// is the time window after last_seen beyond which a node
	// is considered "offline" even if headscale says online
	// (forgiving fallback for transient WireGuard session
	// drops). Set ExitNodeCheckInterval to 0 to disable the
	// monitor entirely (the deploy test still runs from
	// check_exit_nodes.py).
	ExitNodeCheckInterval time.Duration
	ExitNodeOnStartup     bool
	ExitNodeOfflineAfter  time.Duration
	// 2026-07-15: v0.14.1 — auto-heal node_owner_map sync.
	// When true, the monitor's per-tick path also calls
	// db.SyncNodesFromHeadscale (INSERTs missing rows,
	// UPDATEs drifted tags) before classifying exit-nodes.
	// Default false (opt-in) so an operator running a
	// multi-thousand-node tailnet doesn't pay a full
	// node_owner_map write per 5-min tick without asking
	// for it. Set to "true" once you're comfortable with
	// the per-tick cost.
	ExitNodeAutoSync bool
	// 2026-07-17: v0.16.7 — per-user subnet sidecar auto-approver
	// sync period. The Manager goroutine in cmd/skygate/main.go
	// polls headscale every SidecarSyncPeriod for tag:subnet-router
	// nodes, approves the user's CIDR when the sidecar
	// advertises it, and flips user_subnets.status to
	// active/disabled. Default 30s — long enough that the
	// headscale API doesn't get hammered, short enough that
	// the "click Provision → node appears → status flips" loop
	// feels responsive. Set to 0 to disable the auto-approver
	// (preauth-key issuance on /admin/users/{id}/subnet still
	// works, but the sidecar node won't get its route approved
	// automatically).
	SidecarSyncPeriod time.Duration
	// 2026-07-20: v0.20.0 — headscale-update-monitor.
	// HeadscaleVersionPin is the operator's currently-running
	// headscale version (e.g. "0.29.2"). The monitor
	// compares this against the latest GitHub release and
	// emits a Telegram alert when a newer version is
	// available. Empty string = monitor is in "observe only"
	// mode (no alerts, but the /admin/headscale page still
	// shows the latest known version and history).
	//
	// The pin is an env var (not auto-detected) because
	// skygate doesn't shell into the headscale container.
	// Auto-detect could come in a v0.21.0+ if we add a
	// /api/v1/version endpoint to headscale via a wrapper
	// — for now, the operator updates the env var when
	// they upgrade headscale.
	HeadscaleVersionPin string
	// HeadscalePollInterval is how often the monitor hits
	// the GitHub Releases API. Default 24h (matches the
	// headscale release cadence — they ship a minor or
	// patch every few weeks, not every hour). Set to 0
	// to disable the monitor entirely (the
	// /admin/headscale page still works as a manual
	// look-up; the bot /headscale command still works
	// against the cached snapshot).
	HeadscalePollInterval time.Duration
	// 2026-07-20: v0.20.0 — auto-allocate subnet on
	// user create. When true, PostAdminUserCreate
	// automatically calls subnet.Allocate(userID) after
	// the portal_users row is inserted. Default true
	// (matches the operator's stated preference: "I
	// want subnets allocated by default, not via a
	// separate button click"). Set to false to revert
	// to v0.16.0-v0.18.1 behaviour where the operator
	// must visit /admin/users/{id}/subnet and click
	// "Allocate" manually.
	AutoAllocateSubnetOnUserCreate bool
}

func Load() (*Config, error) {
	c := &Config{
		Port:               getenv("SKYGATE_PORT", "8080"),
		DBPath:             getenv("SKYGATE_DB", "/var/lib/skygate/skygate.db"),
		HeadscaleURL:       getenv("HEADSCALE_URL", "http://headscale:50444"),
		HeadscaleKey:       os.Getenv("HEADSCALE_API_KEY"),
		HeadplaneExternalURL: os.Getenv("HEADPLANE_EXTERNAL_URL"),
		ControlURL:         deriveControlURL(getenv("SKYGATE_CONTROL_URL", ""), getenv("HEADSCALE_URL", "http://headscale:50444")),
		JWTSecret:          os.Getenv("SKYGATE_JWT_SECRET"),
		SessionHours:       24,
		BootstrapAdminUser: getenv("SKYGATE_ADMIN_USER", "skyadmin"),
		BootstrapAdminPass: os.Getenv("SKYGATE_ADMIN_PASS"),
		SSHKeyPath:         getenv("SKYGATE_EXIT_SSH_KEY", "/home/skyadmin/.ssh/skygate_sync"),
		DNSAutoCheck:       getDuration("SKYGATE_DNS_AUTO_CHECK", 5*time.Minute),
		MaxRulesPerDevice:  getInt("SKYGATE_MAX_RULES_PER_DEVICE", 200),
		MaxTotalRules:      getInt("SKYGATE_MAX_TOTAL_RULES", 10000),
		StaggerSync:        getenv("SKYGATE_STAGGER_SYNC", "true") == "true",
		StaggerBatchSize:   getInt("SKYGATE_STAGGER_BATCH_SIZE", 20),
		StaggerInterval:    getDuration("SKYGATE_STAGGER_INTERVAL", 30*time.Second),
		UserMaxRules:       parseUserLimits(getenv("SKYGATE_USER_MAX_RULES", "")),
		SecretKeyHex:       os.Getenv("SKYGATE_SECRET_KEY"),
		// 2026-07-15: v0.13.0 — exit-node health monitor
		// knobs. "off" / "0" disables the monitor (default
		// is 5m; same shape as SKYGATE_DNS_AUTO_CHECK so an
		// operator's mental model carries over).
		ExitNodeCheckInterval: getDuration("SKYGATE_EXIT_NODE_CHECK_INTERVAL", 5*time.Minute),
		ExitNodeOnStartup:     getenv("SKYGATE_EXIT_NODE_CHECK_ON_STARTUP", "true") == "true",
		ExitNodeOfflineAfter:  getDuration("SKYGATE_EXIT_NODE_OFFLINE_AFTER", 2*time.Minute),
		// 2026-07-15: v0.14.1 — auto-heal sync (opt-in).
		// Operators who already use the admin "Sync from
		// headscale" button can leave this false; the
		// monitor's job is just the health check, and
		// /admin/devices is where the explicit sync lives.
		// Set SKYGATE_EXIT_NODE_AUTO_SYNC=true on a
		// single-tailnet deployment where the
		// always-current view matters more than the
		// per-tick write cost.
		ExitNodeAutoSync: getenv("SKYGATE_EXIT_NODE_AUTO_SYNC", "false") == "true",
		// 2026-07-17: v0.16.7 — per-user subnet sidecar
		// auto-approver. Set to 0 to disable (operator-driven
		// approve-routes only).
		SidecarSyncPeriod: getDuration("SKYGATE_SIDECAR_SYNC_PERIOD", 30*time.Second),
		// 2026-07-20: v0.20.0 — headscale-update-monitor.
		// Default pin is empty (operator must set
		// SKYGATE_HEADSCALE_VERSION_PIN to enable alerts);
		// default poll is 24h (one check per day is plenty
		// for headscale's release cadence). Set the
		// interval to "off" or "0" to disable.
		HeadscaleVersionPin:    os.Getenv("SKYGATE_HEADSCALE_VERSION_PIN"),
		HeadscalePollInterval:  getDuration("SKYGATE_HEADSCALE_POLL_INTERVAL", 24*time.Hour),
		// 2026-07-20: v0.20.0 — auto-allocate subnet on
		// user create. Default true. Set to "false" in
		// .env to revert to v0.16.0-v0.18.1 manual
		// allocation via /admin/users/{id}/subnet.
		AutoAllocateSubnetOnUserCreate: getenv("SKYGATE_AUTO_ALLOCATE_SUBNET", "true") == "true",
	}

	if v := os.Getenv("SKYGATE_DNS_AUTO_CHECK"); v != "" {
		if v == "off" || v == "0" {
			c.DNSAutoCheck = 0
		}
	}
	if v := os.Getenv("SKYGATE_EXIT_NODE_CHECK_INTERVAL"); v != "" {
		if v == "off" || v == "0" {
			c.ExitNodeCheckInterval = 0
		}
	}
	if v := os.Getenv("SKYGATE_HEADSCALE_POLL_INTERVAL"); v != "" {
		if v == "off" || v == "0" {
			c.HeadscalePollInterval = 0
		}
	}
	if c.HeadscaleKey == "" {
		return nil, fmt.Errorf("HEADSCALE_API_KEY is required")
	}
	if c.JWTSecret == "" {
		return nil, fmt.Errorf("SKYGATE_JWT_SECRET is required")
	}
	return c, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// deriveControlURL returns the URL clients (Tailscale) should be pointed at.
// Priority:
//  1. SKYGATE_CONTROL_URL (explicit human-facing URL)
//  2. HEADSCALE_URL with http:// replaced by https:// and trailing /api stripped
//     (best-effort fallback for self-hosted setups).
func deriveControlURL(explicit, hsAPI string) string {
	explicit = strings.TrimRight(explicit, "/")
	if explicit != "" {
		return explicit
	}
	// Fallback: take headscale API URL, strip /api path, force https
	u := strings.TrimRight(hsAPI, "/")
	u = strings.TrimSuffix(u, "/api/v1")
	u = strings.TrimSuffix(u, "/api")
	if strings.HasPrefix(u, "http://") {
		u = "https://" + strings.TrimPrefix(u, "http://")
	} else if !strings.HasPrefix(u, "https://") {
		u = "https://" + u
	}
	return u
}



func getInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return def
}

func parseUserLimits(s string) map[string]int {
	m := map[string]int{}
	if s == "" { return m }
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" { continue }
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 { continue }
		name := strings.TrimSpace(parts[0])
		n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || n <= 0 { continue }
		m[name] = n
	}
	return m
}
