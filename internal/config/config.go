package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Port               string
	DBPath             string
	HeadscaleURL       string
	HeadscaleKey       string
	ControlURL         string // human-facing URL clients connect to (e.g. https://head.skynas.ru)
	JWTSecret          string
	SessionHours       int
	BootstrapAdminUser string
	BootstrapAdminPass string
}

func Load() (*Config, error) {
	c := &Config{
		Port:               getenv("SKYGATE_PORT", "8080"),
		DBPath:             getenv("SKYGATE_DB", "/var/lib/skygate/skygate.db"),
		HeadscaleURL:       getenv("HEADSCALE_URL", "http://headscale:50444"),
		HeadscaleKey:       os.Getenv("HEADSCALE_API_KEY"),
		ControlURL:         deriveControlURL(getenv("SKYGATE_CONTROL_URL", ""), getenv("HEADSCALE_URL", "http://headscale:50444")),
		JWTSecret:          os.Getenv("SKYGATE_JWT_SECRET"),
		SessionHours:       24,
		BootstrapAdminUser: getenv("SKYGATE_ADMIN_USER", "skyadmin"),
		BootstrapAdminPass: os.Getenv("SKYGATE_ADMIN_PASS"),
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
