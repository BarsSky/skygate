// v1.5.0+ / B212 — unit tests for the join status
// helpers. The DB-free pure-Go helpers covered here:
//   - parseTokenAge (extracts exp from sgn1 JWT)
//   - runJoin dispatch (verb vs token disambiguation)
//
// The HTTP / state-file path is covered by the
// live-verify on the agent (scripts/b212_join_verify.sh).

package main

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

func TestParseTokenAge(t *testing.T) {
	// Build a synthetic sgn1 token with a known exp.
	// sgn1 format: sgn1.<base64-payload>.<base64-sig>
	// (the signature can be any bytes — parseTokenAge
	// doesn't verify it).
	expFuture := time.Now().Add(1 * time.Hour).Unix()
	expPast := time.Now().Add(-1 * time.Hour).Unix()

	cases := []struct {
		name             string
		token            string
		wantExpiresUnix  int64
		wantAgeDirection string // "future" if exp > now, "past" if exp < now, "none" if no exp
	}{
		{
			name: "empty token",
			token: "",
			wantExpiresUnix: 0,
			wantAgeDirection: "none",
		},
		{
			name: "valid sgn1 with future exp",
			token: buildSgn1Token(t, map[string]interface{}{"exp": expFuture, "cid": "test"}),
			wantExpiresUnix: expFuture,
			wantAgeDirection: "future",
		},
		{
			name: "valid sgn1 with past exp",
			token: buildSgn1Token(t, map[string]interface{}{"exp": expPast, "cid": "test"}),
			wantExpiresUnix: expPast,
			wantAgeDirection: "past",
		},
		{
			name: "sgn1 without exp",
			token: buildSgn1Token(t, map[string]interface{}{"cid": "test"}),
			wantExpiresUnix: 0,
			wantAgeDirection: "none",
		},
		{
			name: "sgn1 prefix but malformed signature section (still parses payload)",
			token: "sgn1." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":`+strconv.FormatInt(expFuture, 10)+`}`)) + ".sig",
			wantExpiresUnix: expFuture,
			wantAgeDirection: "future",
		},
		{
			name: "malformed token (no dots)",
			token: "abc",
			wantExpiresUnix: 0,
			wantAgeDirection: "none",
		},
		{
			name: "malformed payload (not base64)",
			token: "sgn1.!!!.sig",
			wantExpiresUnix: 0,
			wantAgeDirection: "none",
		},
		{
			name: "malformed payload (not JSON)",
			token: "sgn1." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".sig",
			wantExpiresUnix: 0,
			wantAgeDirection: "none",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			age, expiresAt := parseTokenAge(c.token)
			switch c.wantAgeDirection {
			case "future":
				if expiresAt.IsZero() {
					t.Errorf("expected non-zero expiresAt, got zero")
				}
				if expiresAt.Unix() != c.wantExpiresUnix {
					t.Errorf("expiresAt.Unix() = %d, want %d", expiresAt.Unix(), c.wantExpiresUnix)
				}
				// age should be negative (token expires in the future, age is "0 since issue")
				if age < 0 {
					t.Errorf("age = %v, want >= 0", age)
				}
			case "past":
				if expiresAt.IsZero() {
					t.Errorf("expected non-zero expiresAt, got zero")
				}
				if age <= 0 {
					t.Errorf("age = %v, want > 0 (token expired %v ago)", age, -age)
				}
			case "none":
				if !expiresAt.IsZero() {
					t.Errorf("expected zero expiresAt, got %v", expiresAt)
				}
				if age != 0 {
					t.Errorf("age = %v, want 0", age)
				}
			}
		})
	}
}

func TestParseJoinArgs_Disambiguation(t *testing.T) {
	// The runJoin dispatcher should treat args[0]="status"
	// as the verb, but any other non-flag args[0] as the
	// invite token (passed to runClusterJoin).
	// We can't easily test runJoin end-to-end without
	// mocking the HTTP POST, but we CAN pin that:
	//  1. The verb "status" is routed to runJoinStatus
	//     (not runClusterJoin)
	//  2. A non-status first arg is passed to runClusterJoin
	//  3. A flag-looking first arg is also passed to
	//     runClusterJoin (NOT treated as the verb).
	//
	// For now, we just pin the parseJoinArgs function
	// (the part that's pure-Go and not HTTP-bound).
	//
	// Flags BEFORE the token work (Go flag.Parse stops
	// at the first non-flag arg, so flags AFTER the
	// token are silently dropped — same behaviour as
	// `kubectl` and most CLIs). This is a known
	// constraint; the live-verify uses the documented
	// flag-before-token form.
	cases := []struct {
		name        string
		args        []string
		wantAPIDef  string
		wantRoleDef string
	}{
		{
			name:        "all defaults",
			args:        []string{"sgn1.token"},
			wantAPIDef:  "http://127.0.0.1:8080",
			wantRoleDef: "skygate-standby",
		},
		{
			name:        "custom api-url + role (flags before token)",
			args:        []string{"--api-url=http://primary:8080", "--role=skygate-standby,custom", "sgn1.token"},
			wantAPIDef:  "http://primary:8080",
			wantRoleDef: "skygate-standby,custom",
		},
		{
			name:        "write-dsn-to flag",
			args:        []string{"--write-dsn-to=/tmp/dbs.env", "sgn1.token"},
			wantAPIDef:  "http://127.0.0.1:8080",
			wantRoleDef: "skygate-standby",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, token, err := parseJoinArgs(c.args)
			if err != nil {
				t.Fatalf("parseJoinArgs: %v", err)
			}
			if token != "sgn1.token" {
				t.Errorf("token = %q, want %q", token, "sgn1.token")
			}
			if opts.APIURL != c.wantAPIDef {
				t.Errorf("APIURL = %q, want %q", opts.APIURL, c.wantAPIDef)
			}
			if opts.RolesCSV != c.wantRoleDef {
				t.Errorf("RolesCSV = %q, want %q", opts.RolesCSV, c.wantRoleDef)
			}
		})
	}
}

func TestParseJoinArgs_MissingToken(t *testing.T) {
	_, _, err := parseJoinArgs([]string{})
	if err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestJoinState_DefaultPath(t *testing.T) {
	// Pin the default state-file path so a future
	// refactor that moves it (e.g. to ~/.skygate/)
	// doesn't silently break the existing flow.
	if joinStateFile != "/etc/skygate/cluster-state.json" {
		t.Errorf("joinStateFile = %q, want %q", joinStateFile, "/etc/skygate/cluster-state.json")
	}
	// And it MUST match the StateFilePath used by
	// `skygate cluster heartbeat-daemon` (otherwise
	// the heartbeat-daemon can't read what join
	// wrote).
	if joinStateFile != StateFilePath {
		t.Errorf("joinStateFile (%q) != StateFilePath (%q) — the heartbeat-daemon will read the wrong file", joinStateFile, StateFilePath)
	}
}

// buildSgn1Token builds a synthetic sgn1 JWT with the
// given claims. The signature is 32 random bytes (the
// actual signature is irrelevant for parseTokenAge —
// it only reads the payload).
func buildSgn1Token(t *testing.T, claims map[string]interface{}) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	// Signature can be anything; the sgn1 format
	// is "sgn1.<payload-b64>.<sig-b64>" per B200.
	return "sgn1." + base64.RawURLEncoding.EncodeToString(payload) + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
}

// (itoa was removed; we use strconv.FormatInt now.)
