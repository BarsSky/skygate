// limits_settings_b328_test.go — B328, the panel control for the rule caps.
//
// WHY THIS EXISTS. The caps were reachable only through `.env`, and under docker the
// container environment is frozen at CREATION — so an operator whose device had hit the
// per-device ceiling had to edit `.env` and recreate the container (a service
// interruption, and the documented trap #3 in AGENTS.md) to change one number, while the
// panel was already showing them `x/500`. That was the situation the operator report
// came from.
//
// The layering is the one internal/derpcfg proved (B296): global_settings row > .env >
// built-in default, with the SOURCE reported so the panel can say whether editing .env
// would change anything at all.
package exit_rules

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	"skygate/internal/config"
	skygatedb "skygate/internal/db"
)

// TestB328_LimitLayeringIsDBThenEnvThenDefault is the property the card promises.
func TestB328_LimitLayeringIsDBThenEnvThenDefault(t *testing.T) {
	d := newB328DB(t)

	t.Run("no db row, no env → built-in default", func(t *testing.T) {
		got := resolveMaxPerDevice(d, nil)
		if got.Value != 200 || got.Source != limitSourceDefault {
			t.Errorf("per-device = %+v, want {200 default}", got)
		}
	})
	t.Run("env wins over the default", func(t *testing.T) {
		got := resolveMaxPerDevice(d, &config.Config{MaxRulesPerDevice: 500})
		if got.Value != 500 || got.Source != limitSourceEnv {
			t.Errorf("per-device = %+v, want {500 env}", got)
		}
	})
	t.Run("the db row wins over env", func(t *testing.T) {
		if err := skygatedb.SetGlobalSetting(d, SettingKeyMaxPerDevice, "2000"); err != nil {
			t.Fatalf("seed setting: %v", err)
		}
		got := resolveMaxPerDevice(d, &config.Config{MaxRulesPerDevice: 500})
		if got.Value != 2000 || got.Source != limitSourceDB {
			t.Errorf("per-device = %+v, want {2000 db}", got)
		}
	})
	t.Run("an explicit 0 in the db row DISABLES the level", func(t *testing.T) {
		if err := skygatedb.SetGlobalSetting(d, SettingKeyMaxPerDevice, "0"); err != nil {
			t.Fatalf("seed setting: %v", err)
		}
		got := resolveMaxPerDevice(d, &config.Config{MaxRulesPerDevice: 500})
		if got.Value != 0 || got.Source != limitSourceDB {
			t.Errorf("per-device = %+v, want {0 db} — a stored 0 is a decision, an absent row is not", got)
		}
	})
	t.Run("the total level falls to 10000 by default", func(t *testing.T) {
		got := resolveMaxTotal(d, nil)
		if got.Value != 10000 || got.Source != limitSourceDefault {
			t.Errorf("total = %+v, want {10000 default}", got)
		}
	})
}

// TestB328_SaveValidatesAndClearingInherits: an empty field hands the level back to
// `.env`; junk is refused and writes nothing.
func TestB328_SaveValidatesAndClearingInherits(t *testing.T) {
	d := newB328DB(t)

	if err := SaveRuleLimits(d, "2000", "5000"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := dbLimitOverride(d, SettingKeyMaxPerDevice); got != "2000" {
		t.Errorf("stored per-device = %q, want 2000", got)
	}
	if got := dbLimitOverride(d, SettingKeyMaxTotal); got != "5000" {
		t.Errorf("stored total = %q, want 5000", got)
	}

	// Clearing (empty inputs) must DELETE the rows so `.env` takes over again.
	if err := SaveRuleLimits(d, "", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := dbLimitOverride(d, SettingKeyMaxPerDevice); got != "" {
		t.Errorf("per-device override = %q after clearing, want empty (inherit)", got)
	}
	if got := dbLimitOverride(d, SettingKeyMaxTotal); got != "" {
		t.Errorf("total override = %q after clearing, want empty (inherit)", got)
	}

	for _, bad := range []string{"abc", "-5", "1.5", "  ", "2000x"} {
		if bad == "  " {
			continue // whitespace IS "inherit", not an error
		}
		if err := SaveRuleLimits(d, bad, ""); err == nil {
			t.Errorf("SaveRuleLimits(%q) accepted a value it should refuse", bad)
		} else if !strings.Contains(err.Error(), "max_rules_per_device") {
			t.Errorf("SaveRuleLimits(%q) error does not name the field: %v", bad, err)
		}
	}
	if got := dbLimitOverride(d, SettingKeyMaxPerDevice); got != "" {
		t.Errorf("a refused save wrote %q — it must change nothing", got)
	}
}

// TestB328_TheOverrideChangesTheDecision is the operator's actual need: the cap was
// blocking their device, and raising it from the panel must lift the block without a
// container recreate.
func TestB328_TheOverrideChangesTheDecision(t *testing.T) {
	d := newB328DB(t)
	seedB328LiveShape(t, d, 56, 4) // device 56 has 1 counted row

	// The env layer alone: cap 1 → this device is AT the cap, refused.
	cfg := &config.Config{MaxRulesPerDevice: 1, MaxTotalRules: 10000}
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, Cfg: cfg}
	limits, sources := s.effectiveRuleLimits("skyadmin")
	if sources["per_device"].Source != limitSourceEnv {
		t.Fatalf("precondition: per-device source = %s, want env", sources["per_device"].Source)
	}
	if reason := limits.ExceedReason(measureRuleLimits(d, limits, 1, 56, "skyadmin")); reason == "" {
		t.Fatalf("with the cap at 1 the device should be at its limit")
	}

	// Raise it from the panel. No restart, no .env edit, no container recreate.
	if err := SaveRuleLimits(d, "2000", ""); err != nil {
		t.Fatalf("save: %v", err)
	}
	limits, sources = s.effectiveRuleLimits("skyadmin")
	if sources["per_device"].Source != limitSourceDB {
		t.Errorf("per-device source = %s after the panel save, want db", sources["per_device"].Source)
	}
	if limits.MaxPerDevice != 2000 {
		t.Errorf("per-device cap = %d, want 2000", limits.MaxPerDevice)
	}
	if reason := limits.ExceedReason(measureRuleLimits(d, limits, 1, 56, "skyadmin")); reason != "" {
		t.Errorf("the raised cap did not lift the block: %q — the service object was NOT rebuilt, so this must have come from the resolver", reason)
	}

	// Clearing hands control back to `.env`, so the block returns.
	if err := SaveRuleLimits(d, "", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	limits, sources = s.effectiveRuleLimits("skyadmin")
	if sources["per_device"].Source != limitSourceEnv || limits.MaxPerDevice != 1 {
		t.Errorf("after clearing: cap=%d source=%s, want 1/env", limits.MaxPerDevice, sources["per_device"].Source)
	}
	if reason := limits.ExceedReason(measureRuleLimits(d, limits, 1, 56, "skyadmin")); reason == "" {
		t.Errorf("clearing the override should restore the .env cap and the block")
	}
}

// TestB328_LimitsHandlerIsAdminOnlyAndReportsRefusals: the endpoint must not be a way
// for a non-admin to widen their own quota, and a bad value must surface on the page
// instead of being swallowed.
func TestB328_LimitsHandlerIsAdminOnlyAndReportsRefusals(t *testing.T) {
	d := newB328DB(t)
	b328Exec(t, d, `INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	                VALUES (1, 'skyadmin', 'x', 1, 1)`)
	b328Exec(t, d, `INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	                VALUES (3, 'user3', 'x', 0, 3)`)

	post := func(admin bool, perDevice string) *httptest.ResponseRecorder {
		s := &Service{
			Backend: &b328Backend{claims: &auth.Claims{UserID: 1, Username: "skyadmin", IsAdmin: admin}},
			DB:      skygatedb.FixedDBSource{DB: d},
		}
		form := url.Values{"max_rules_per_device": {perDevice}, "max_total_rules": {""}}
		req := httptest.NewRequest(http.MethodPost, "/admin/exit-rules/limits", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		s.PostAdminExitRuleLimits(rec, req)
		return rec
	}

	if rec := post(false, "999999"); rec.Code != http.StatusForbidden {
		t.Errorf("a non-admin got %d, want 403 — the endpoint must not widen anyone's own quota", rec.Code)
	}
	if got := dbLimitOverride(d, SettingKeyMaxPerDevice); got != "" {
		t.Fatalf("the non-admin request stored %q", got)
	}

	if rec := post(true, "1500"); rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "limits_saved=1") {
		t.Errorf("admin save: %d %s, want a 302 to ?limits_saved=1", rec.Code, rec.Header().Get("Location"))
	}
	if got := dbLimitOverride(d, SettingKeyMaxPerDevice); got != "1500" {
		t.Errorf("stored per-device = %q, want 1500", got)
	}

	if rec := post(true, "nonsense"); rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "limits_err=") {
		t.Errorf("a bad value produced %d %s, want a 302 carrying ?limits_err=", rec.Code, rec.Header().Get("Location"))
	}
	if got := dbLimitOverride(d, SettingKeyMaxPerDevice); got != "1500" {
		t.Errorf("a refused save changed the stored value to %q", got)
	}
}
