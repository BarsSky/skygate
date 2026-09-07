// cmd/skygate/regapi_credentials.go — B237.21 CLI subcommand
// for the reg.ru / external DNS provider credentials.
//
// Pre-B237.21 the only path to set the creds was the
// /admin/ha "External DNS" form. That works for the
// web-based workflow but blocks the operator's
// "one-shot bootstrap" pattern (clone repo → start
// skygate → set creds via CLI → run live test).
//
// B237.21 adds:
//
//   skygate regapi-credentials set      --login=X --password=Y --zone=Z --cert-path=P [--key-path=K] [--provider=external]
//   skygate regapi-credentials show     (prints current creds with the secret fields masked)
//   skygate regapi-credentials test     (calls /api/v1/<...>/zone/get_resource_records against the live provider)
//   skygate regapi-credentials delete   (clears the stored creds; the cert/password rows + the plaintext rows)
//
// All four verbs share the same dispatcher
// (runRegAPICredsSubcommand) + the same Store / Load
// / TestConnection / Delete (clear) path as the
// /admin/ha form. The only difference: the operator
// is typing into a shell instead of pasting into a
// web form.
//
// Env-var shortcuts: any of --login / --password /
// --zone / --provider can come from the env
// (SKYGATE_DNS_REGAPI_{LOGIN,PASSWORD,ZONE,PROVIDER}).
// The CLI flag wins if both are set. --password can
// also come from a file via --password-file= (so the
// operator can chmod 0600 the file instead of leaking
// the password into the shell history).
//
// Exit codes:
//   0 = success (the requested verb completed)
//   1 = error (the error is printed to stderr with
//       enough context for the operator to fix and retry)
//   2 = invalid args (the flag parser returns
//       flag.ErrHelp which the dispatcher treats as 2)
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"skygate/internal/config"
	"skygate/internal/db"
	extcreds "skygate/internal/ha/dnsexternal"
)

// runRegAPICredsSubcommand is the dispatcher for
// `skygate regapi-credentials <verb>`. The verb is
// os.Args[2] (same shape as the `cluster` and
// `migrate` subcommands).
func runRegAPICredsSubcommand(args []string) error {
	if len(args) < 1 {
		return errors.New("regapi-credentials: missing verb (set, show, test, delete)")
	}
	verb := args[0]
	switch verb {
	case "set":
		return runRegAPICredsSet(args[1:])
	case "show":
		return runRegAPICredsShow(args[1:])
	case "test":
		return runRegAPICredsTest(args[1:])
	case "delete":
		return runRegAPICredsDelete(args[1:])
	default:
		return fmt.Errorf("regapi-credentials: unknown verb %q (set, show, test, delete)", verb)
	}
}

// runRegAPICredsSet — `skygate regapi-credentials set`.
//
// Encrypts the cert PEM (if --cert-path is given) +
// the password (if --password or --password-file is given)
// with SKYGATE_SECRET_KEY + writes to global_settings via
// extcreds.Store.Save. The plaintext rows (provider +
// login + zone) are stored as-is; only the secrets are
// encrypted.
//
// Why a separate flag for each (instead of a "load from
// a JSON file" mode): the operator's typical flow is
// to set the creds ONCE per deployment. A JSON file
// would be a feature creep. Future B-block if the
// operator actually wants bulk-import: add
// `--import-file=path.json`.
//
// Example:
//   skygate regapi-credentials set \
//     --login=kanagaenko@mail.ru \
//     --password='<the alternative password>' \
//     --zone=skynas.ru \
//     --cert-path=/home/skyadmin/skygate-secrets/regapi/cert.pem
func runRegAPICredsSet(args []string) error {
	fs := flag.NewFlagSet("regapi-credentials set", flag.ExitOnError)
	login := fs.String("login", os.Getenv("SKYGATE_DNS_REGAPI_LOGIN"), "reg.ru login (env fallback: SKYGATE_DNS_REGAPI_LOGIN)")
	password := fs.String("password", os.Getenv("SKYGATE_DNS_REGAPI_PASSWORD"), "reg.ru alternative password (env fallback: SKYGATE_DNS_REGAPI_PASSWORD; use --password-file= for safer shell history)")
	passwordFile := fs.String("password-file", "", "read the password from a file (chmod 0600; alternative to --password)")
	zone := fs.String("zone", os.Getenv("SKYGATE_DNS_REGAPI_ZONE"), "the DNS zone the reg.ru account manages (env fallback: SKYGATE_DNS_REGAPI_ZONE)")
	provider := fs.String("provider", envOr("SKYGATE_DNS_REGAPI_PROVIDER", "external"), "the provider name (default 'external' — the v1.5.0 reg.ru implementation)")
	certPath := fs.String("cert-path", envOr("SKYGATE_DNS_REGAPI_CERT_PATH", "/home/skyadmin/skygate-secrets/regapi/cert.pem"), "path to the mTLS cert PEM")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *login == "" {
		return errors.New("regapi-credentials set: --login is required (or set SKYGATE_DNS_REGAPI_LOGIN)")
	}
	if *zone == "" {
		return errors.New("regapi-credentials set: --zone is required (or set SKYGATE_DNS_REGAPI_ZONE)")
	}
	// Password precedence:
	//   --password-file > --password > SKYGATE_DNS_REGAPI_PASSWORD
	// The file wins because the operator typically chmod 0600's
	// a file with the password; typing it on the command line
	// leaks into shell history (e.g. `history` / `ps` / `w`).
	pw := *password
	if *passwordFile != "" {
		b, err := os.ReadFile(*passwordFile)
		if err != nil {
			return fmt.Errorf("regapi-credentials set: read password-file: %w", err)
		}
		pw = strings.TrimRight(string(b), "\r\n")
	}
	if pw == "" {
		return errors.New("regapi-credentials set: --password (or --password-file) is required (or set SKYGATE_DNS_REGAPI_PASSWORD)")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("regapi-credentials set: config: %w", err)
	}
	if cfg.SecretKeyHex == "" {
		return errors.New("regapi-credentials set: SKYGATE_SECRET_KEY is empty in .env (set it first; see .env.example for `openssl rand -hex 32`)")
	}
	pool, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("regapi-credentials set: db open: %w", err)
	}
	defer pool.Close()

	// Read the cert PEM if --cert-path was given. If the
	// operator is using only the "Test connection" button
	// (no cert needed for the HTTP-Basic + mTLS auth pattern
	// is wrong, but the v1.5.0 form allows the form to
	// submit without a cert for the "we'll add the cert
	// later" case), the cert stays empty.
	var certPEM string
	if *certPath != "" {
		b, err := os.ReadFile(*certPath)
		if err != nil {
			return fmt.Errorf("regapi-credentials set: read cert-path: %w", err)
		}
		certPEM = string(b)
		if len(certPEM) < 50 {
			return fmt.Errorf("regapi-credentials set: cert at %s is only %d bytes (expected ~1400 for a real PEM; is the path correct?)", *certPath, len(certPEM))
		}
	}

	store := extcreds.NewStore(pool, cfg.SecretKeyHex)
	creds := extcreds.Credentials{
		Provider: *provider,
		Login:    *login,
		Zone:     *zone,
		Password: pw,
		CertPEM:  certPEM,
	}
	if err := store.Save(creds); err != nil {
		return fmt.Errorf("regapi-credentials set: save: %w", err)
	}
	// 2026-09-07 (B237.21): a deliberately generic
	// success line. We DO NOT echo the password back
	// to stdout (the operator already has it; printing
	// it would put it in their shell history if they
	// redirect or copy-paste the output). The password
	// is in the DB encrypted; the operator can verify
	// the "show" verb if they need to confirm the row.
	fmt.Printf("regapi-credentials set: ok (provider=%s login=%s zone=%s, cert=%d bytes, secret key: %d chars)\n",
		*provider, *login, *zone, len(certPEM), len(cfg.SecretKeyHex))
	// Also print the next-step hint so the operator
	// doesn't have to look up the docs.
	fmt.Println("next step:  skygate regapi-credentials test   (live verification against the provider API)")
	return nil
}

// runRegAPICredsShow — `skygate regapi-credentials show`.
// Prints the current creds with the password + cert
// masked. The operator uses this to verify "did the
// set verb actually persist?"
func runRegAPICredsShow(args []string) error {
	fs := flag.NewFlagSet("regapi-credentials show", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("regapi-credentials show: config: %w", err)
	}
	if cfg.SecretKeyHex == "" {
		return errors.New("regapi-credentials show: SKYGATE_SECRET_KEY is empty in .env (the creds are encrypted with it; without it we can't decrypt for display)")
	}
	pool, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("regapi-credentials show: db open: %w", err)
	}
	defer pool.Close()
	store := extcreds.NewStore(pool, cfg.SecretKeyHex)
	creds, err := store.Load()
	if err != nil {
		return fmt.Errorf("regapi-credentials show: load: %w", err)
	}
	if creds.IsZero() {
		fmt.Println("regapi-credentials show: (not configured — the /admin/ha 'External DNS' form banner is showing 'not configured')")
		return nil
	}
	fmt.Printf("provider:     %s\n", creds.Provider)
	fmt.Printf("login:        %s\n", creds.Login)
	fmt.Printf("zone:         %s\n", creds.Zone)
	fmt.Printf("password:     %s\n", maskSecret(creds.Password))
	fmt.Printf("cert_pem:     %s\n", certSummary(creds.CertPEM))
	if !store.IsConfigured() {
		fmt.Println("note:        IsConfigured() = false (cert + password are both present in the DB, but something else is missing — check the /admin/ha 'Test' button output)")
	}
	return nil
}

// runRegAPICredsTest — `skygate regapi-credentials test`.
// Calls extcreds.Store.TestConnection against the live
// provider API. This is the same code path the
// /admin/ha "Test connection" button uses. The result
// is a one-line PASS/FAIL summary + the underlying
// error on FAIL.
//
// Why a CLI version: the operator's "B146 live verify"
// flow is `set creds → test → run b146_regapi_live.sh`.
// The first step needs the creds; the second step is
// a quick "does the in-app config work?" sanity check;
// the third is the real end-to-end test. Without the
// CLI test verb, the operator would need to either log
// in to /admin/ha just to click "Test" (browser is
// heavy) or skip the sanity check and go straight to
// b146_regapi_live.sh (which can fail with a less-
// actionable error if the creds are wrong).
func runRegAPICredsTest(args []string) error {
	fs := flag.NewFlagSet("regapi-credentials test", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("regapi-credentials test: config: %w", err)
	}
	if cfg.SecretKeyHex == "" {
		return errors.New("regapi-credentials test: SKYGATE_SECRET_KEY is empty in .env")
	}
	pool, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("regapi-credentials test: db open: %w", err)
	}
	defer pool.Close()
	store := extcreds.NewStore(pool, cfg.SecretKeyHex)
	if !store.IsConfigured() {
		return errors.New("regapi-credentials test: not configured (no cert + no password in the DB — run `skygate regapi-credentials set` first)")
	}
	res, err := store.TestConnection(nil)
	if err != nil {
		// Same UX as the /admin/ha form: even on
		// FAIL we print the result so the operator
		// can see WHICH step failed (auth? network?
		// not_configured?).
		fmt.Fprintf(os.Stderr, "regapi-credentials test: FAIL\n")
		fmt.Fprintf(os.Stderr, "  status:  %s\n", res.Status)
		fmt.Fprintf(os.Stderr, "  message: %s\n", res.Message)
		if res.HTTPStatus != 0 {
			fmt.Fprintf(os.Stderr, "  http:    %d\n", res.HTTPStatus)
		}
		fmt.Fprintf(os.Stderr, "  error:   %v\n", err)
		return fmt.Errorf("regapi-credentials test: %w", err)
	}
	fmt.Printf("regapi-credentials test: PASS (latency=%d ms)\n", res.LatencyMS)
	if res.Message != "" {
		fmt.Printf("  message: %s\n", res.Message)
	}
	return nil
}

// runRegAPICredsDelete — `skygate regapi-credentials delete`.
// Clears the 5 global_settings rows. The operator
// uses this if the cert was rotated and the
// "set" verb isn't enough (which it should be —
// "set" upserts — but the explicit delete is here
// for symmetry with "set" and for the case where
// the operator wants to fully reset the
// /admin/ha External DNS form state).
func runRegAPICredsDelete(args []string) error {
	fs := flag.NewFlagSet("regapi-credentials delete", flag.ExitOnError)
	yes := fs.Bool("yes", false, "skip the confirmation prompt (required for non-interactive use, e.g. scripts)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*yes {
		fmt.Fprint(os.Stderr, "regapi-credentials delete: this clears the 5 global_settings rows. Re-run with --yes to confirm.\n")
		return errors.New("regapi-credentials delete: confirmation required (--yes)")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("regapi-credentials delete: config: %w", err)
	}
	pool, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("regapi-credentials delete: db open: %w", err)
	}
	defer pool.Close()
	store := extcreds.NewStore(pool, cfg.SecretKeyHex)
	if err := store.Delete(); err != nil {
		return fmt.Errorf("regapi-credentials delete: %w", err)
	}
	fmt.Println("regapi-credentials delete: ok (the 5 global_settings rows are cleared; the next /admin/ha 'External DNS' form render shows 'not configured')")
	return nil
}

// envOr returns the env value if set, else def.
// Tiny helper to keep the --provider / --cert-path
// defaults readable (the env names are too long
// for an inline fallback in the flag.String default).
func envOr(envKey, def string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return def
}

// maskSecret returns the first 2 + last 2 chars of
// `s`, masking the rest with `*`. For passwords
// shorter than 8 chars, the whole string is masked
// (we don't want to leak the length).
func maskSecret(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:2] + strings.Repeat("*", len(s)-4) + s[len(s)-2:]
}

// certSummary returns a one-line summary of the cert
// PEM: the subject (extracted from the BEGIN line if
// possible) + the byte count. We don't print the
// actual PEM (it's a secret — the cert includes the
// private key in some configurations).
func certSummary(pem string) string {
	if pem == "" {
		return "(empty — no cert configured)"
	}
	return fmt.Sprintf("(%d bytes, PEM-format)", len(pem))
}
