// Package derpcfg — the operator's "where can THIS container reach the relay"
// hint (B296).
//
// # WHY THIS PACKAGE EXISTS
//
// The reachability guard, the /admin/derp status probes, the STUN probe and the
// derp_health cron all have to answer one question: which ADDRESS do we dial for
// a relay whose public hostname we must still SPEAK (TLS SNI — see B289.1)?
// Inside the skygate container the relay's own name can resolve to 127.0.0.1
// (the host's /etc/hosts leaks in — AGENTS deployment trap #2), so the answer is
// not always "the hostname".
//
// B289.1 introduced `SKYGATE_DERP_PROBE_HOST` for exactly that, but the knob had
// one usability defect the live host exposed: it is a *container environment
// variable*, and the skygate container is created from `env_file: .env`
// (docker-compose.yml). Docker freezes that environment at container CREATION —
// `docker compose restart` re-runs the entrypoint but keeps the old environment —
// so changing the value meant a `--force-recreate` (AGENTS trap #3), i.e. an SSH
// session and a compose command for something the operator can see is wrong right
// on /admin/derp/relays.
//
// So the value now has two layers, resolved at every probe (not once at boot):
//
//	db    the `global_settings` row this package owns — written by the
//	      /admin/derp/relays form, effective IMMEDIATELY, no restart, no
//	      recreate, no compose edit;
//	env   `SKYGATE_DERP_PROBE_HOST` from .env — the bootstrap/unattended
//	      default (deploy.sh writes it, .env.example documents it);
//	default "" — no hint: every probe keeps B289's behaviour (try the
//	      row's own name first, then the other known names).
//
// The DB layer wins over the env layer on purpose: an operator who typed an
// address on the page must not have it silently ignored because .env still
// carries an older value. Clearing the field clears the row, and the env value
// takes over again (GetGlobalSetting treats "" exactly like a missing row).
package derpcfg

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"

	"skygate/internal/db"
)

const (
	// SettingKey is the global_settings row behind the admin form.
	SettingKey = "derp.probe_host"

	// EnvKey is the .env/bootstrap variable (deploy.sh writes it when it
	// detects that the relay's own name resolves to loopback on the host).
	EnvKey = "SKYGATE_DERP_PROBE_HOST"
)

// Source names where an effective value came from, so the page can tell the
// operator whether editing .env would change anything at all.
type Source string

const (
	SourceDefault Source = "default"
	SourceEnv     Source = "env"
	SourceDB      Source = "db"
)

// Resolution is one answer to "which address do the probes dial".
type Resolution struct {
	Host   string
	Source Source
}

// EnvHost returns the .env/bootstrap value, trimmed ("" when unset).
func EnvHost() string {
	return strings.TrimSpace(os.Getenv(EnvKey))
}

// Resolve returns the effective hint plus its source.
//
// Order: DB override > env > default (""). A nil *sql.DB is legal — cron and
// test callers without a handle simply fall through to the env layer.
func Resolve(d *sql.DB) Resolution {
	if d != nil {
		if v, err := db.GetGlobalSetting(d, SettingKey, ""); err == nil {
			if v = strings.TrimSpace(v); v != "" {
				return Resolution{Host: v, Source: SourceDB}
			}
		}
	}
	if v := EnvHost(); v != "" {
		return Resolution{Host: v, Source: SourceEnv}
	}
	return Resolution{Host: "", Source: SourceDefault}
}

// DialHost is Resolve(d).Host — the shape every probe call site wants.
func DialHost(d *sql.DB) string {
	return Resolve(d).Host
}

// Save validates and stores the override. An EMPTY value clears the row so the
// env/default layer takes over again (that is the "Очистить" button).
func Save(d *sql.DB, raw string) error {
	if d == nil {
		return fmt.Errorf("derpcfg: no database handle")
	}
	v, err := Validate(raw)
	if err != nil {
		return err
	}
	return db.SetGlobalSetting(d, SettingKey, v)
}

// ValidationError carries a machine-readable Code so the handler can render a
// translated message instead of a Go error string in the operator's face.
type ValidationError struct {
	Code string // empty | port | scheme | path | spaces | invalid
	Raw  string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("derpcfg: invalid probe host %q (%s)", e.Raw, e.Code)
}

var hostnameRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)

// Validate normalises an operator-entered dial address.
//
// Accepted: an IPv4 literal, an IPv6 literal (bare or [bracketed]) or a plain
// hostname. Refused, each with its own Code so the page can say WHY:
//
//	scheme  "https://relay.example.com" — the probe builds the URL itself
//	port    "192.0.2.10:443"            — the port comes from the relay row's URL
//	path    "relay.example.com/derp"    — not a dial address
//	spaces  "192.0.2.10 192.0.2.11"     — one address, not a list
//	invalid anything else (a bad IP/hostname)
//
// An empty string is VALID and means "clear the override".
func Validate(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return "", &ValidationError{Code: "spaces", Raw: raw}
	}
	if strings.Contains(v, "://") || strings.HasPrefix(v, "//") {
		return "", &ValidationError{Code: "scheme", Raw: raw}
	}
	if strings.ContainsAny(v, "/?#") {
		return "", &ValidationError{Code: "path", Raw: raw}
	}
	// IPv6 in brackets (the form net.JoinHostPort produces) — unwrap first so
	// the literal checks below see the bare address.
	bare := v
	if strings.HasPrefix(bare, "[") && strings.HasSuffix(bare, "]") {
		bare = bare[1 : len(bare)-1]
	}
	if ip := net.ParseIP(bare); ip != nil {
		return ip.String(), nil
	}
	// A bare IPv6 literal is full of colons, so a colon can only mean "the
	// operator pasted host:port" once the IP parse above has failed.
	if strings.Contains(bare, ":") {
		return "", &ValidationError{Code: "port", Raw: raw}
	}
	if len(bare) > 253 || !hostnameRE.MatchString(bare) {
		return "", &ValidationError{Code: "invalid", Raw: raw}
	}
	// All-numeric labels that failed the IP parse above are a typo'd IPv4
	// ("999.0.2.10"): accepting them as a hostname would turn an obvious typo
	// into an unresolvable name and a confusing probe error.
	if allNumericLabels(bare) {
		return "", &ValidationError{Code: "invalid", Raw: raw}
	}
	for _, label := range strings.Split(bare, ".") {
		if len(label) > 63 {
			return "", &ValidationError{Code: "invalid", Raw: raw}
		}
	}
	return strings.ToLower(bare), nil
}

// allNumericLabels reports whether every dot-separated label is digits only
// (so the value is meant to be an IPv4 address).
func allNumericLabels(host string) bool {
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return false
		}
		for _, r := range label {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
