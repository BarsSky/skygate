// 2026-08-20 (B155, v1.5.0) — unit tests for the
// preauth key UX handlers. Covers the pure-function
// helpers (resolvePreauthTTL, humanizeTTL,
// durationFromSeconds) and the custom_ttl_value +
// custom_ttl_unit + reusable form parsing logic.
//
// The full POST handlers (PostMyPreauth,
// PostMyKeyReissue) require a live DB + a live
// headscale client, so they're covered by the B155
// B-check + the VM's live verify run, not by these
// unit tests.

package my

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestResolvePreauthTTL_CustomValid(t *testing.T) {
	cases := []struct {
		value           string
		unit            string
		wantExp         string
		wantSec         int64
		wantUsed        string
	}{
		{"1", "h", "1h", 3600, "custom:1h"},
		{"24", "h", "24h", 24 * 3600, "custom:24h"},
		{"1", "d", "24h", 24 * 3600, "custom:1d"},
		{"7", "d", "168h", 7 * 24 * 3600, "custom:7d"},
		{"1", "w", "168h", 7 * 24 * 3600, "custom:1w"},
		{"1", "y", "8760h", 365 * 24 * 3600, "custom:1y"},
		{"0", "d", "never", 0, "never"},
		// Unit defaults to "d" when blank.
		{"3", "", "72h", 3 * 24 * 3600, "custom:3d"},
	}
	for _, c := range cases {
		t.Run(c.value+c.unit, func(t *testing.T) {
			form := url.Values{}
			form.Set("custom_ttl_value", c.value)
			form.Set("custom_ttl_unit", c.unit)
			r := httptest.NewRequest("POST", "/my/preauth", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.ParseForm()

			gotExp, gotSec, gotUsed := resolvePreauthTTL(r)
			if gotExp != c.wantExp {
				t.Errorf("expiration: got %q, want %q", gotExp, c.wantExp)
			}
			if gotSec != c.wantSec {
				t.Errorf("seconds: got %d, want %d", gotSec, c.wantSec)
			}
			if gotUsed != c.wantUsed {
				t.Errorf("used: got %q, want %q", gotUsed, c.wantUsed)
			}
		})
	}
}

// TestResolvePreauthTTL_OutOfRangeFallsThrough confirms
// that custom TTLs outside the 1h..5y range fall
// through to the legacy dropdown rather than 400 —
// the page stays usable for an operator mid-typing.
func TestResolvePreauthTTL_OutOfRangeFallsThrough(t *testing.T) {
	// 0 years, with unit=y → 0h. The "v == 0" branch
	// returns "never" first.
	{
		form := url.Values{}
		form.Set("custom_ttl_value", "0")
		form.Set("custom_ttl_unit", "y")
		r := httptest.NewRequest("POST", "/my/preauth", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.ParseForm()
		exp, sec, used := resolvePreauthTTL(r)
		if exp != "never" || sec != 0 || used != "never" {
			t.Errorf("0y should be 'never': got exp=%q sec=%d used=%q", exp, sec, used)
		}
	}

	// 6 years → too large, fall through to dropdown.
	// The form has no ttl= field, so we get the
	// 1h default.
	{
		form := url.Values{}
		form.Set("custom_ttl_value", "6")
		form.Set("custom_ttl_unit", "y")
		r := httptest.NewRequest("POST", "/my/preauth", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.ParseForm()
		exp, sec, used := resolvePreauthTTL(r)
		if exp != "1h" || sec != 3600 || used != "default-1h" {
			t.Errorf("6y should fall through to default 1h: got exp=%q sec=%d used=%q", exp, sec, used)
		}
	}

	// Invalid value (non-numeric) → fall through to
	// 1h default.
	{
		form := url.Values{}
		form.Set("custom_ttl_value", "abc")
		form.Set("custom_ttl_unit", "d")
		r := httptest.NewRequest("POST", "/my/preauth", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.ParseForm()
		exp, sec, used := resolvePreauthTTL(r)
		if exp != "1h" || sec != 3600 || used != "default-1h" {
			t.Errorf("non-numeric should fall through: got exp=%q sec=%d used=%q", exp, sec, used)
		}
	}
}

// TestResolvePreauthTTL_LegacyDropdown confirms the
// back-compat path: the legacy ttl= field still works
// when the new custom_ttl_value is not provided.
func TestResolvePreauthTTL_LegacyDropdown(t *testing.T) {
	cases := []struct {
		ttl     string
		wantExp string
		wantSec int64
		wantUsed string
	}{
		{"1h", "1h", 3600, "1h"},
		{"1d", "24h", 86400, "1d"},
		{"1w", "168h", 7 * 86400, "1w"},
		{"never", "never", 0, "never"},
		// Unknown values fall through to 1h default
		// (the pre-B155 behaviour: an old / buggy
		// form can't lock the user out).
		{"garbage", "1h", 3600, "default-1h"},
	}
	for _, c := range cases {
		t.Run(c.ttl, func(t *testing.T) {
			form := url.Values{}
			form.Set("ttl", c.ttl)
			r := httptest.NewRequest("POST", "/my/preauth", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.ParseForm()
			exp, sec, used := resolvePreauthTTL(r)
			if exp != c.wantExp {
				t.Errorf("expiration: got %q, want %q", exp, c.wantExp)
			}
			if sec != c.wantSec {
				t.Errorf("seconds: got %d, want %d", sec, c.wantSec)
			}
			if used != c.wantUsed {
				t.Errorf("used: got %q, want %q", used, c.wantUsed)
			}
		})
	}
}

// TestResolvePreauthTTL_CustomOverridesLegacy confirms
// the precedence rule: custom_ttl_value beats ttl=.
// Without this, a form that fills BOTH fields would
// pick the wrong one and the user's explicit choice
// would be ignored.
func TestResolvePreauthTTL_CustomOverridesLegacy(t *testing.T) {
	form := url.Values{}
	form.Set("custom_ttl_value", "3")
	form.Set("custom_ttl_unit", "d")
	form.Set("ttl", "1h") // legacy dropdown says 1h, but custom wins
	r := httptest.NewRequest("POST", "/my/preauth", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ParseForm()
	exp, sec, used := resolvePreauthTTL(r)
	if exp != "72h" || sec != 3*24*3600 || used != "custom:3d" {
		t.Errorf("custom should override legacy: got exp=%q sec=%d used=%q", exp, sec, used)
	}
}

func TestHumanizeTTL(t *testing.T) {
	cases := []struct {
		sec  int64
		want string
	}{
		{0, "never"},
		{-1, "never"},
		{60, "1m"},
		{30 * 60, "30m"},
		{3600, "1h"},
		{2 * 3600, "2h"},
		{24 * 3600, "1d"},
		{3 * 24 * 3600, "3d"},
		{7 * 24 * 3600, "1w"},
		{14 * 24 * 3600, "2w"},
		{365 * 24 * 3600, "1y"},
		{2 * 365 * 24 * 3600, "2y"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := humanizeTTL(c.sec); got != c.want {
				t.Errorf("humanizeTTL(%d) = %q, want %q", c.sec, got, c.want)
			}
		})
	}
}

func TestDurationFromSeconds(t *testing.T) {
	cases := []struct {
		sec  int64
		want string
	}{
		{0, "1h"},  // never-TTL fallback
		{-1, "1h"}, // negative fallback
		{60, "1h"}, // sub-hour rounds up to 1h
		{3600, "1h"},
		{86400, "24h"},
		{7 * 86400, "168h"},
		{30 * 86400, "720h"},
		{365 * 86400, "8760h"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := durationFromSeconds(c.sec); got != c.want {
				t.Errorf("durationFromSeconds(%d) = %q, want %q", c.sec, got, c.want)
			}
		})
	}
}
