// limits_settings_b328.go — B328: the rule caps must be adjustable from the panel.
//
// WHY. The caps exist because a run-away rule set hurts everyone (the ACL grows, the
// control-plane policy gets huge, every client payload grows). SKYGATE_MAX_RULES_PER_DEVICE
// is the one an operator actually hits, and it was reachable ONLY through `.env` —
// which under docker is frozen at container CREATION, so changing it means
// `--force-recreate`, i.e. a service interruption, for a number. B328's operator needed
// exactly that: the per-device guard was refusing everything (see rule_limits_b328.go),
// and the only lever was an env var plus a container recreate.
//
// HOW. Same layering the DERP probe host proved (internal/derpcfg, B296), for the same
// reason — a value the panel must be able to change without a restart:
//
//	global_settings row  >  .env (config.Config)  >  built-in default
//
// Empty means "no override here", so clearing the row hands control back to `.env`, and
// an unset `.env` falls to the code default. 0 keeps its existing meaning everywhere
// (that level is DISABLED — the rule_limits ladder checks `> 0` before comparing).
//
// The page shows the EFFECTIVE value AND which layer it came from, because a setting
// whose origin is invisible is how an operator ends up editing the wrong file.
package exit_rules

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/config"
	"skygate/internal/db"
)

// global_settings keys. Versioned by prefix so a future change of meaning can use a new
// key instead of silently reinterpreting an old row.
const (
	SettingKeyMaxPerDevice = "exit_rules.max_rules_per_device"
	SettingKeyMaxTotal     = "exit_rules.max_total_rules"
)

// limitSource names where an effective cap came from, so the panel can say whether
// editing `.env` would change anything at all.
type limitSource string

const (
	limitSourceDefault limitSource = "default"
	limitSourceEnv     limitSource = "env"
	limitSourceDB      limitSource = "db"
)

// resolvedLimit is one cap plus its origin. Value 0 = that level is disabled.
type resolvedLimit struct {
	Value  int
	Source limitSource
}

// parseLimitValue accepts the shapes an operator types into a number field. An empty
// string is NOT an error here: it means "no override" at the DB layer and "fall through"
// at the env layer.
func parseLimitValue(raw string) (int, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// resolveLimit applies the layering for one key.
//
// `envValue` is what config.Config already parsed out of `.env` (0 = unset/disabled),
// and `def` is the built-in default. A nil handle is legal (tests, and any caller
// without a DB): it simply falls through to the env layer.
func resolveLimit(d *sql.DB, key string, envValue, def int) resolvedLimit {
	if d != nil {
		if v, err := db.GetGlobalSetting(d, key, ""); err == nil {
			if n, ok := parseLimitValue(v); ok && strings.TrimSpace(v) != "" {
				return resolvedLimit{Value: n, Source: limitSourceDB}
			}
		}
	}
	if envValue > 0 {
		return resolvedLimit{Value: envValue, Source: limitSourceEnv}
	}
	return resolvedLimit{Value: def, Source: limitSourceDefault}
}

// resolveMaxPerDevice is the per-device cap, DB > env > default(200).
func resolveMaxPerDevice(d *sql.DB, cfg *config.Config) resolvedLimit {
	env, def := 0, 200
	if cfg != nil {
		env = cfg.MaxRulesPerDevice
	}
	return resolveLimit(d, SettingKeyMaxPerDevice, env, def)
}

// resolveMaxTotal is the system-wide ceiling, DB > env > default(10000). Setting the
// DB value to an explicit "0" is the way to turn the ceiling OFF (the row is present and
// parses, so it wins over the default; an ABSENT row falls through).
func resolveMaxTotal(d *sql.DB, cfg *config.Config) resolvedLimit {
	env, def := 0, 10000
	if cfg != nil {
		env = cfg.MaxTotalRules
	}
	return resolveLimit(d, SettingKeyMaxTotal, env, def)
}

// effectiveRuleLimits assembles the ladder the limit decision uses. The per-user level
// keeps its `.env`-only source (SKYGATE_USER_MAX_RULES) but its FALLBACK is the resolved
// per-device cap, exactly as getMaxRulesForUser did before B328.
func (s *Service) effectiveRuleLimits(username string) (ruleLimits, map[string]resolvedLimit) {
	d := s.dbc()
	perDevice := resolveMaxPerDevice(d, s.Cfg)
	total := resolveMaxTotal(d, s.Cfg)

	perUser := perDevice
	if s.Cfg != nil {
		if v, ok := s.Cfg.UserMaxRules[username]; ok {
			perUser = resolvedLimit{Value: v, Source: limitSourceEnv}
		}
	}

	limits := ruleLimits{MaxPerUser: perUser.Value, MaxPerDevice: perDevice.Value, MaxTotal: total.Value}
	return limits, map[string]resolvedLimit{
		"per_user":   perUser,
		"per_device": perDevice,
		"total":      total,
	}
}

// SaveRuleLimits stores the panel's values. An empty string CLEARS that row, handing the
// level back to `.env`/default — the "Очистить" button, mirroring derpcfg.Save.
//
// A non-numeric or negative value is refused with a message naming the field, so a typo
// cannot silently disable a guard.
func SaveRuleLimits(d *sql.DB, perDeviceRaw, totalRaw string) error {
	if d == nil {
		return fmt.Errorf("exit_rules: no database handle")
	}
	save := func(key, raw, label string) error {
		if strings.TrimSpace(raw) == "" {
			return db.DeleteGlobalSetting(d, key)
		}
		n, ok := parseLimitValue(raw)
		if !ok {
			return fmt.Errorf("%s: expected a non-negative integer or an empty value (= inherit from .env), got %q", label, raw)
		}
		return db.SetGlobalSetting(d, key, strconv.Itoa(n))
	}
	if err := save(SettingKeyMaxPerDevice, perDeviceRaw, "max_rules_per_device"); err != nil {
		return err
	}
	return save(SettingKeyMaxTotal, totalRaw, "max_total_rules")
}

// PostAdminExitRuleLimits handles POST /admin/exit-rules/limits — the panel control for
// the caps themselves.
//
// This exists because the caps used to be env-only: under docker the container
// environment is frozen at CREATION, so an operator whose device had hit the ceiling had
// to edit `.env` and recreate the container (a service interruption) to change a number
// — while the panel was already showing them `x/500`.
//
// Admin-only, audited, and it reports the effective value with its SOURCE after saving,
// so "did my change take effect, and from which layer" is answerable from the page.
func (s *Service) PostAdminExitRuleLimits(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	perDevice := r.FormValue("max_rules_per_device")
	total := r.FormValue("max_total_rules")

	if err := SaveRuleLimits(s.dbc(), perDevice, total); err != nil {
		http.Redirect(w, r, "/admin/exit-rules?limits_err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}

	effective, sources := s.effectiveRuleLimits(c.Username)
	s.Backend.Audit(c.UserID, c.Username, "admin_exit_rule_limits",
		fmt.Sprintf("per_device=%d(%s) per_user=%d(%s) total=%d(%s) [submitted per_device=%q total=%q]",
			effective.MaxPerDevice, sources["per_device"].Source,
			effective.MaxPerUser, sources["per_user"].Source,
			effective.MaxTotal, sources["total"].Source,
			strings.TrimSpace(perDevice), strings.TrimSpace(total)))
	log.Printf("exit-rules: limits saved by %s → per_device=%d(%s) per_user=%d(%s) total=%d(%s)",
		c.Username, effective.MaxPerDevice, sources["per_device"].Source,
		effective.MaxPerUser, sources["per_user"].Source,
		effective.MaxTotal, sources["total"].Source)

	http.Redirect(w, r, "/admin/exit-rules?limits_saved=1", http.StatusFound)
}
