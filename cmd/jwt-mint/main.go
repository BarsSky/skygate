// jwt-mint is a small helper binary that mints a
// skygate session JWT for the live-verify scripts.
// It lives in cmd/ (a real Go command, in the
// skygate module) so it can import the internal
// auth package (the `internal/` rule: only packages
// within the same module can import internal/*).
//
// Usage:
//   SKYGATE_JWT_SECRET=<hex> go run ./cmd/jwt-mint \
//     -uid=1 -username=skyadmin -admin=true -ttl-hours=1
//
// The script writes the JWT to stdout. The
// /tmp/skygate_b215_liveverify-style scripts save
// the output and use it as the skygate_session
// cookie for subsequent curl calls.
//
// Why not just curl /login?
// ------------------------------
// The admin password reset issue from B214 is still
// unresolved (SKYGATE_ADMIN_PASS in .env doesn't
// match the stored bcrypt hash). Using a minted JWT
// sidesteps the login flow entirely — we trust the
// SKYGATE_JWT_SECRET to sign a valid session for
// uid=1, which the authMW + CurrentUser() will
// accept (the JWT is HMAC-verified, not bcrypt-
// compared against a password row).
package main

import (
	"flag"
	"fmt"
	"os"

	"skygate/internal/auth"
)

func main() {
	fs := flag.NewFlagSet("jwt-mint", flag.ExitOnError)
	uid := fs.Int64("uid", 1, "user id to embed in the JWT")
	username := fs.String("username", "skyadmin", "username to embed in the JWT")
	isAdmin := fs.Bool("admin", true, "is_admin claim")
	ttlHours := fs.Int("ttl-hours", 1, "TTL in hours (default 1h)")
	_ = fs.Parse(os.Args[1:])

	secret := os.Getenv("SKYGATE_JWT_SECRET")
	if secret == "" {
		// Fall back to SKYGATE_SECRET_KEY (some
		// deployments use the same key for both JWT
		// and cluster invitation signing).
		secret = os.Getenv("SKYGATE_SECRET_KEY")
	}
	if secret == "" {
		fmt.Fprintln(os.Stderr, "FATAL: SKYGATE_JWT_SECRET (or SKYGATE_SECRET_KEY) not set")
		os.Exit(1)
	}
	tok, err := auth.IssueJWT(secret, *uid, *username, *isAdmin, *ttlHours)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: IssueJWT: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(tok)
}
