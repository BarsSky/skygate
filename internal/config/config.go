package config

import (
	"fmt"
	"os"
)

type Config struct {
	Port               string
	DBPath             string
	HeadscaleURL       string
	HeadscaleKey       string
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
