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
	}

	if v := os.Getenv("SKYGATE_DNS_AUTO_CHECK"); v != "" {
		if v == "off" || v == "0" {
			c.DNSAutoCheck = 0
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
