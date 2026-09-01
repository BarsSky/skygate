// v1.5.0+ / B203 — unit tests for the watchdog's
// DSNReader closure + redactDSN. We don't test the
// full goroutine loop here (that needs a real DB
// and is covered by the live B203 verify script);
// just the pure helpers.

package watchdog

import (
	"strings"
	"testing"
	"time"
)

func TestRedactDSN(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "standard",
			in:   "postgres://skyadmin:secret@127.0.0.1:5433/skygate_staging?sslmode=disable",
			want: "postgres://skyadmin:***@127.0.0.1:5433/skygate_staging?sslmode=disable",
		},
		{
			name: "no password",
			in:   "postgres://skyadmin@host/db",
			want: "postgres://skyadmin@host/db",
		},
		{
			name: "not postgres",
			in:   "http://user:pass@example.com",
			want: "http://user:pass@example.com",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "no @",
			in:   "postgres://host/db",
			want: "postgres://host/db",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactDSN(c.in)
			if got != c.want {
				t.Errorf("redactDSN(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Interval != 5*time.Second {
		t.Errorf("Interval = %v, want 5s", cfg.Interval)
	}
	if cfg.PingTimeout != 3*time.Second {
		t.Errorf("PingTimeout = %v, want 3s", cfg.PingTimeout)
	}
	if cfg.Logger == nil {
		t.Error("Logger is nil; should default to log.Printf")
	}
}

func TestNewDBSwap_DefaultsApplied(t *testing.T) {
	// nil config values should fall back to DefaultConfig.
	wd := NewDBSwap(Config{}, nil, nil)
	if wd.cfg.Interval != 5*time.Second {
		t.Errorf("Interval = %v, want 5s (default)", wd.cfg.Interval)
	}
	if wd.cfg.PingTimeout != 3*time.Second {
		t.Errorf("PingTimeout = %v, want 3s (default)", wd.cfg.PingTimeout)
	}
	if wd.cfg.Logger == nil {
		t.Error("Logger is nil; should default to log.Printf")
	}
}

func TestClusterDatabaseRow_FieldsPin(t *testing.T) {
	// Pinned to keep the public surface stable; the
	// watchdog uses every field via the reader closure.
	row := ClusterDatabaseRow{
		ID:         "skygate-staging",
		CurrentDSN: "postgres://x:y@h/d",
		DBName:     "skygate_staging",
		Username:   "admin",
		SSLMode:    "disable",
	}
	if row.ID != "skygate-staging" {
		t.Errorf("ID = %q", row.ID)
	}
	if !strings.Contains(row.CurrentDSN, "h/d") {
		t.Errorf("CurrentDSN lost host")
	}
}

// Smoke test that backendPID doesn't panic on nil.
func TestBackendPID_NoDB(t *testing.T) {
	got := backendPID(nil)
	if !strings.HasPrefix(got, "error:") {
		t.Errorf("backendPID(nil) = %q, want prefix 'error:'", got)
	}
}
