// service_b172_test.go — B172 (v1.5.2) unit tests for the
// `next`-preserving post-login redirect.
//
// Pre-B172 the OIDC login flow died silently after the
// user typed their password: the user got redirected to
// /dashboard (the hard-coded fallback) and headscale's
// /oidc/callback was never reached, so the device never
// got registered. B172 introduced `safeNextRedirect` to
// validate + honour the `next` form value, and the
// `GetLogin`/`PostLogin` handlers now thread it through.
//
// These tests pin the safeNextRedirect contract. The
// end-to-end test in internal/oidc/e2e_test.go (the new
// STEP 4 added by B172) covers the OIDC ↔ login
// round-trip; this file covers the open-redirect defense
// in isolation so a future refactor can't quietly weaken
// it without a test failure.
package auth

import "testing"

// TestSafeNextRedirect covers the 5 categories of `next`
// values that the post-login redirect can receive:
//
//   1. Empty (default to /dashboard — backwards-compat).
//   2. Relative path starting with a single "/" (same-
//      origin, no host part) — allow as-is.
//   3. Protocol-relative URL ("//evil.com/path") —
//      REJECT (would navigate to a different host).
//   4. Absolute URL with a different host (e.g.
//      "https://evil.com/...") — REJECT (open redirect).
//   5. Absolute URL with the same host as the request
//      (the OIDC case: "https://skygate.skynas.ru/oidc/
//      authorize?client_id=...") — allow as-is.
//
// Cases 3 and 4 are the open-redirect defenses. Cases 1,
// 2, 5 are the legitimate flows (login redirect after
// OIDC + login redirect to a relative path + the OIDC
// full-URL round-trip).
func TestSafeNextRedirect(t *testing.T) {
	const reqHost = "skygate.skynas.ru"

	cases := []struct {
		name     string
		in       string
		want     string
	}{
		// Case 1: empty input — backwards-compat with the
		// pre-B172 behaviour (always go to /dashboard
		// after login).
		{"empty", "", "/dashboard"},

		// Case 2: relative path. Pre-B172 the form was
		// always /login?theme=... and a user could
		// type a relative path in the URL (e.g. the
		// "return to /my/keys" bookmark). We accept
		// these as-is (they can't navigate to a
		// different host).
		{"relative_path", "/dashboard", "/dashboard"},
		{"relative_path_nested", "/my/keys?foo=bar", "/my/keys?foo=bar"},
		{"relative_path_with_query", "/admin/devices?err=bad_id", "/admin/devices?err=bad_id"},

		// Case 3: protocol-relative URL. The naïve
		// net/url.Parse accepts "//evil.com/path" as
		// a URL with Host=evil.com — but we want to
		// REJECT it before parsing so the same-host
		// check below is the only allow path.
		{"protocol_relative", "//evil.com/path", "/dashboard"},
		{"protocol_relative_slash_path", "//evil.com/oidc/authorize", "/dashboard"},

		// Case 4: absolute URL with a different host.
		// This is the open-redirect attack vector: an
		// attacker crafts a malicious /login?next=
		// URL, the user logs in, and the post-login
		// redirect sends them to evil.com (a
		// convincing phishing page that says "session
		// expired, please log in again").
		{"absolute_different_host", "https://evil.com/dashboard", "/dashboard"},
		{"absolute_subdomain", "https://skygate.evil.com/oidc/authorize", "/dashboard"},
		// HTTP→HTTPS upgrade is allowed (same host,
		// different scheme is fine — the user is just
		// landing on the same place, scheme-mismatch
		// isn't a security issue because the host check
		// already passed).
		{"absolute_http_upgrade", "http://skygate.skynas.ru/dashboard", "http://skygate.skynas.ru/dashboard"},
		// HTTPS→HTTP downgrade is ALSO allowed by the
		// helper (same host). In production this
		// shouldn't happen (the request is always on
		// HTTPS) but the helper is conservative — the
		// browser's HSTS preload list + the
		// Strict-Transport-Security response header
		// are the right places to enforce the scheme,
		// not the login redirect helper.
		{"absolute_https_downgrade", "https://skygate.skynas.ru/dashboard", "https://skygate.skynas.ru/dashboard"},
		// Non-http(s) schemes are rejected
		// unconditionally — javascript:, data:, file:
		// are all classic XSS / open-redirect vectors.
		{"absolute_javascript_scheme", "javascript:alert(1)", "/dashboard"},
		{"absolute_data_scheme", "data:text/html,<script>alert(1)</script>", "/dashboard"},
		{"absolute_file_scheme", "file:///etc/passwd", "/dashboard"},

		// Case 5: absolute URL with the same host as
		// the request. This is the OIDC case: the
		// /oidc/authorize handler generates a /login
		// redirect with the full OIDC authorize URL
		// (including client_id + state + code_challenge)
		// as the `next` value, and the post-login
		// redirect must send the user back to that URL
		// with all params intact.
		{"absolute_same_host", "https://skygate.skynas.ru/oidc/authorize?client_id=headscale&state=abc", "https://skygate.skynas.ru/oidc/authorize?client_id=headscale&state=abc"},
		{"absolute_same_host_root", "https://skygate.skynas.ru/dashboard", "https://skygate.skynas.ru/dashboard"},
		// The HTTP variant of the same host is also
		// allowed (the same-host check is scheme-
		// agnostic — strict-Transport-Security is the
		// browser's job, not the server's). The real
		// post-login redirect is on HTTPS in production
		// but the dev VM uses HTTP, and we want the
		// helper to behave consistently in both.
		{"absolute_same_host_http", "http://skygate.skynas.ru/dashboard", "http://skygate.skynas.ru/dashboard"},

		// Edge cases — malformed URLs that the parser
		// rejects.
		{"malformed_with_space", "https://exa mple.com/foo", "/dashboard"},
		{"malformed_no_scheme", "skygate.skynas.ru/dashboard", "/dashboard"},

		// Edge case — the request's Host header is
		// empty (degenerate). The helper should fall
		// back to /dashboard for absolute URLs (since
		// there's no same-host match possible) but
		// still accept relative paths.
		// (This is the unit test for the helper alone;
		// in production the real /login handler always
		// has a non-empty r.Host.)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := safeNextRedirect(c.in, reqHost)
			if got != c.want {
				t.Errorf("safeNextRedirect(%q, %q) = %q, want %q",
					c.in, reqHost, got, c.want)
			}
		})
	}
}

// TestSafeNextRedirect_EmptyHost is the degenerate
// case: if the request Host header is empty (which
// shouldn't happen in production but is a valid
// defensive case for unit tests / future fronting
// proxies that strip Host), the helper should:
//   - Accept relative paths (they don't depend on
//     the host).
//   - Reject absolute URLs (since there's no same-
//     host match possible without a request host).
func TestSafeNextRedirect_EmptyHost(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "/dashboard"},
		{"/dashboard", "/dashboard"},
		{"/my/keys", "/my/keys"},
		{"https://skygate.skynas.ru/dashboard", "/dashboard"},
		{"https://evil.com/dashboard", "/dashboard"},
		{"//evil.com/dashboard", "/dashboard"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := safeNextRedirect(c.in, "")
			if got != c.want {
				t.Errorf("safeNextRedirect(%q, \"\") = %q, want %q",
					c.in, got, c.want)
			}
		})
	}
}
