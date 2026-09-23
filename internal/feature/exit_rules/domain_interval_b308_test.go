// B308 (v1.5.73) — the per-domain re-resolve interval.
//
// Live cause of the permanent red «политика headscale УСТАРЕЛА» banner and the
// repeated policy rewrites (agent VM, 2026-09-23): every domain rule was
// re-resolved on EVERY five-minute tick, so a domain whose A records rotate
// produced ±20 derived rule rows per tick and the generated ACL never stopped
// changing. These tests pin the gate that stops it.
package exit_rules

import (
	"testing"
	"time"
)

func TestParseDomainResolveInterval_B308(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", DefaultDomainResolveInterval},          // never set → default 6h
		{"   ", DefaultDomainResolveInterval},       // whitespace tolerated
		{"21600", 6 * time.Hour},                    // the default, explicit
		{"3600", time.Hour},                         // an hour is fine
		{"0", 0},                                    // explicit "every tick"
		{"-5", 0},                                   // negative = unlimited too
		{"60", MinDomainResolveInterval},            // below the floor → clamped up
		{"604800", MaxDomainResolveInterval},        // exactly a week
		{"99999999", MaxDomainResolveInterval},      // above the ceiling → clamped down
		{"garbage", DefaultDomainResolveInterval},   // unparseable → default
		{"not-a-number", DefaultDomainResolveInterval},
	}
	for _, tc := range cases {
		if got := ParseDomainResolveInterval(tc.raw); got != tc.want {
			t.Errorf("ParseDomainResolveInterval(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestClampDomainResolveInterval_B308(t *testing.T) {
	if got := ClampDomainResolveInterval(2 * time.Second); got != MinDomainResolveInterval {
		t.Errorf("clamp(2s) = %v, want the %v floor", got, MinDomainResolveInterval)
	}
	if got := ClampDomainResolveInterval(30 * 24 * time.Hour); got != MaxDomainResolveInterval {
		t.Errorf("clamp(30d) = %v, want the %v ceiling", got, MaxDomainResolveInterval)
	}
	if got := ClampDomainResolveInterval(0); got != 0 {
		t.Errorf("clamp(0) = %v, want 0 (gate disabled)", got)
	}
	if got := ClampDomainResolveInterval(90 * time.Minute); got != 90*time.Minute {
		t.Errorf("clamp(90m) = %v, want it unchanged", got)
	}
}

func TestDomainResolveDue_B308(t *testing.T) {
	const now = int64(1_700_000_000)
	sixHours := 6 * time.Hour

	cases := []struct {
		name     string
		last     int64
		interval time.Duration
		want     bool
	}{
		// A domain that was never resolved must be resolved immediately — the
		// gate must never starve a freshly created rule.
		{"never-resolved-is-due", 0, sixHours, true},
		// interval <= 0 disables the gate (the pre-B308 behaviour).
		{"gate-disabled-is-always-due", now - 10, 0, true},
		{"gate-disabled-negative", now - 10, -time.Minute, true},
		// Freshly resolved: not due.
		{"resolved-now", now, sixHours, false},
		{"resolved-one-hour-ago", now - 3600, sixHours, false},
		// Interval elapsed: due.
		{"resolved-six-hours-ago", now - 21600, sixHours, true},
		{"resolved-long-ago", now - 86400, sixHours, true},
		// A clock that moved backwards (NTP step / restart with a skewed clock)
		// must not freeze the domain for hours.
		{"clock-went-backwards", now + 3600, sixHours, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := domainResolveDue(tc.last, now, tc.interval); got != tc.want {
				t.Errorf("domainResolveDue(last=%d, now=%d, interval=%v) = %v, want %v",
					tc.last, now, tc.interval, got, tc.want)
			}
		})
	}
}

func TestDomainResolveIntervalLabel_B308(t *testing.T) {
	if got := DomainResolveIntervalLabel(0); got == "" || got == "0s" {
		t.Errorf("label for the disabled gate = %q, want a sentence explaining it", got)
	}
	if got := DomainResolveIntervalLabel(6 * time.Hour); got != "6h0m0s" {
		t.Errorf("label(6h) = %q, want 6h0m0s", got)
	}
}

// TestDomainResolveIntervalSettingKey_B308: the key is the contract between the
// updater and the admin page — a rename on one side only would silently restore
// the churn (the page would write a value nobody reads).
func TestDomainResolveIntervalSettingKey_B308(t *testing.T) {
	if SettingDomainResolveIntervalSec != "dns_domain_resolve_interval_sec" {
		t.Errorf("setting key = %q, want the documented dns_domain_resolve_interval_sec", SettingDomainResolveIntervalSec)
	}
	if SettingDomainResolveAtPrefix == "" || SettingDomainResolveAtPrefix[len(SettingDomainResolveAtPrefix)-1] != ':' {
		t.Errorf("per-domain key prefix = %q, want it to end in ':'", SettingDomainResolveAtPrefix)
	}
}
