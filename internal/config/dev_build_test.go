// 2026-08-17 (B124): SKYGATE_DEV_BUILD env var drives the
// "dev build" banner on /admin/update. Default false. The
// /admin/update page renders the banner when DevBuild=true and
// suppresses the "update available" alert + auto-apply button.
//
// This test pins the env-var contract so a future refactor of
// config.go doesn't drop the field by accident. The Service
// passthrough (admin.Service.DevBuild) is exercised by the
// integration /admin/update test in internal/feature/admin/.

package config

import (
	"testing"
)

func TestDevBuild_DefaultFalse(t *testing.T) {
	t.Setenv("SKYGATE_DEV_BUILD", "")
	t.Setenv("SKYGATE_DB_DSN", "postgres://x:y@127.0.0.1:5432/z?sslmode=disable")
	t.Setenv("HEADSCALE_API_KEY", "test-key")
	t.Setenv("SKYGATE_JWT_SECRET", "test-secret-for-unit-test-only")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DevBuild {
		t.Errorf("DevBuild default = true, want false (SKYGATE_DEV_BUILD unset)")
	}
}

func TestDevBuild_TrueWhenEnvTrue(t *testing.T) {
	t.Setenv("SKYGATE_DEV_BUILD", "true")
	t.Setenv("SKYGATE_DB_DSN", "postgres://x:y@127.0.0.1:5432/z?sslmode=disable")
	t.Setenv("HEADSCALE_API_KEY", "test-key")
	t.Setenv("SKYGATE_JWT_SECRET", "test-secret-for-unit-test-only")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.DevBuild {
		t.Errorf("DevBuild = false, want true (SKYGATE_DEV_BUILD=true)")
	}
}

func TestDevBuild_FalseOnOtherTruthyValues(t *testing.T) {
	// The contract is strict: only literal "true" enables dev
	// mode. "yes" / "1" / "True" / "TRUE" are all false. This
	// matches the SKYGATE_AUTO_UPDATE_ENABLED contract (the
	// other binary env var in the same family) and avoids
	// the historical 1/yes/true confusion.
	cases := []string{"", "false", "0", "no", "TRUE", "True", "yes", "1"}
	for _, v := range cases {
		t.Run("value="+v, func(t *testing.T) {
			t.Setenv("SKYGATE_DEV_BUILD", v)
			t.Setenv("SKYGATE_DB_DSN", "postgres://x:y@127.0.0.1:5432/z?sslmode=disable")
			t.Setenv("HEADSCALE_API_KEY", "test-key")
			t.Setenv("SKYGATE_JWT_SECRET", "test-secret-for-unit-test-only")
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.DevBuild {
				t.Errorf("DevBuild = true for SKYGATE_DEV_BUILD=%q, want false (strict literal \"true\")", v)
			}
		})
	}
}
