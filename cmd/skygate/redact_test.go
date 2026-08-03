// redact_test.go — regression test for the v0.32.22 startup-banner
// password redaction. The redaction is in cmd/skygate/main.go
// (so it's in package main); this test is also in package main.
package main

import "testing"

func TestRedactPGPassword(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Standard DSN — password between ":" and "@".
		{
			in:   "postgres://skygate:supersecret@host:5432/skygate?sslmode=disable",
			want: "postgres://skygate:***@host:5432/skygate?sslmode=disable",
		},
		// Short form (no port).
		{
			in:   "postgres://user:pass@db/skygate",
			want: "postgres://user:***@db/skygate",
		},
		// postgresql:// (not just postgres://).
		{
			in:   "postgresql://u:p@h/d",
			want: "postgresql://u:***@h/d",
		},
		// No password (trust auth) — returned as-is.
		{
			in:   "postgres://skygate@host/skygate",
			want: "postgres://skygate@host/skygate",
		},
		// No "://" prefix — returned as-is.
		{
			in:   "not a dsn at all",
			want: "not a dsn at all",
		},
		// No "@" — returned as-is.
		{
			in:   "postgres://skygate:nohostmarker",
			want: "postgres://skygate:nohostmarker",
		},
		// Empty input.
		{
			in:   "",
			want: "",
		},
		// Password with special characters (no @ in password).
		{
			in:   "postgres://u:p%40ss@host:5432/d",
			want: "postgres://u:***@host:5432/d",
		},
	}
	for i, c := range cases {
		got := redactPGPassword(c.in)
		if got != c.want {
			t.Errorf("case %d: redactPGPassword(%q) = %q, want %q", i, c.in, got, c.want)
		}
	}
}
