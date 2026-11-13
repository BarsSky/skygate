// cmd/skygate/dns_cli_b468.go — the DNS half of a MANUAL switch between two copies.
//
// WHY THESE VERBS EXIST. The intended switch is a deliberate, human-driven operation: one copy
// is stopped and the other started, and the public A record must follow. Two things make that
// dangerous to do by hand in a terminal:
//
//  1. the record name and zone have to be right (a typo points the service at nothing);
//  2. "the API accepted the write" is not "DNS now answers the new IP" — the registrar's own
//     cache lags, and the operator needs to see WHICH value is still served before walking away.
//
// So the verbs are read/verify first and write second, they print the registrar's own answer,
// and `--dry-run` shows the exact record that would be written without touching anything.
//
//	skygate dns-records  [--zone=Z] [--name=N]                  # read-only; prints the zone + the A record
//	skygate dns-set-a    --ip=1.2.3.4 [--zone=Z] [--name=N] [--dry-run]
//
// The credentials are the ones already stored (`skygate regapi-credentials set …` or
// /admin/ha) — this block made that store usable (real endpoint, login+password, no mTLS).
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"skygate/internal/config"
	"skygate/internal/db"
	dnsexternal "skygate/internal/dnsexternal"
	extcreds "skygate/internal/ha/dnsexternal"
	"skygate/internal/localtime"
)

// dnsCLITimeout bounds one invocation: a switch script must not hang on a slow registrar.
const dnsCLITimeout = 60 * time.Second

// runDNSCLI dispatches `dns-records` / `dns-set-a`.
func runDNSCLI(verb string, args []string) error {
	switch verb {
	case "dns-records":
		return runDNSRecords(args)
	case "dns-set-a":
		return runDNSSetA(args)
	}
	return fmt.Errorf("dns: unknown verb %q (dns-records, dns-set-a)", verb)
}

// openDNSCreds opens the DB and returns the credential store plus the client bound to it.
//
// The env fallbacks matter for a switch script: `SKYGATE_DNS_REGAPI_LOGIN/PASSWORD/ZONE` let an
// operator (or an automation host) run the flip without first writing rows through the API.
func openDNSCreds() (*sql.DB, *extcreds.Store, *dnsexternal.Client, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("config: %w", err)
	}
	pool, err := openCLIDB(cfg)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("db open: %w", err)
	}
	store := extcreds.NewStore(pool, cfg.SecretKeyHex)
	client := dnsexternal.NewClient(store)
	return pool, store, client, func() { _ = pool.Close() }, nil
}

// applyEnvCreds fills the blanks from SKYGATE_DNS_REGAPI_* so a scripted switch needs no DB rows.
func applyEnvCreds(c *extcreds.Credentials) {
	if strings.TrimSpace(c.Login) == "" {
		c.Login = os.Getenv("SKYGATE_DNS_REGAPI_LOGIN")
	}
	if strings.TrimSpace(c.Password) == "" {
		c.Password = os.Getenv("SKYGATE_DNS_REGAPI_PASSWORD")
	}
	if strings.TrimSpace(c.Zone) == "" {
		c.Zone = os.Getenv("SKYGATE_DNS_REGAPI_ZONE")
	}
	if strings.TrimSpace(c.Provider) == "" {
		c.Provider = extcreds.ProviderRegru
	}
}

// loadEffectiveCreds merges the stored credentials with the env fallbacks.
func loadEffectiveCreds(store *extcreds.Store) (extcreds.Credentials, error) {
	c, err := store.Load()
	if err != nil {
		return extcreds.Credentials{}, err
	}
	applyEnvCreds(&c)
	if c.Provider == "" {
		c.Provider = extcreds.ProviderRegru
	}
	if c.Login == "" || c.Password == "" || c.Zone == "" {
		return c, fmt.Errorf("credentials are incomplete (login=%t password=%t zone=%t) — run `skygate regapi-credentials set --login=… --password=… --zone=…` or set SKYGATE_DNS_REGAPI_LOGIN/PASSWORD/ZONE",
			c.Login != "", c.Password != "", c.Zone != "")
	}
	return c, nil
}

// runDNSRecords prints what the registrar currently serves (read-only, no writes at all).
func runDNSRecords(args []string) error {
	fs := flag.NewFlagSet("dns-records", flag.ContinueOnError)
	zone := fs.String("zone", os.Getenv("SKYGATE_DNS_REGAPI_ZONE"), "DNS zone (env: SKYGATE_DNS_REGAPI_ZONE)")
	name := fs.String("name", envOr("SKYGATE_DNS_REGAPI_NAME", "skygate"), "the subdomain whose A record is reported (\"@\" = zone apex)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	d, store, client, closeFn, err := openDNSCreds()
	if err != nil {
		return err
	}
	defer closeFn()
	_ = d

	creds, err := loadEffectiveCreds(store)
	if err != nil {
		return err
	}
	if *zone != "" {
		creds.Zone = *zone
	}
	client.BaseURL = "" // shipped endpoint (or SKYGATE_REGAPI_BASE_URL)
	ctx, cancel := context.WithTimeout(context.Background(), dnsCLITimeout)
	defer cancel()

	records, ip, found, err := client.RecordsFor(ctx, creds.Zone, *name)
	if err != nil {
		return err
	}
	fmt.Printf("zone: %s (endpoint %s)\n", creds.Zone, extcreds.RegAPIBase())
	fmt.Printf("records: %d\n", len(records))
	for _, r := range records {
		fmt.Printf("  %-6s %-24s %s\n", recordTypeOf(r), recordNameOf(r), r.Content)
	}
	if found {
		fmt.Printf("A record for %q: %s\n", *name, ip)
	} else {
		fmt.Printf("A record for %q: NOT PRESENT\n", *name)
	}
	return nil
}

// runDNSSetA points the A record(s) at --ip and verifies each by reading the zone back.
//
// --name accepts a comma-separated list because a switch usually moves more than one public
// name (gateway, headscale, DERP): one command, one before/after report, one audit row.
//
// --ttl exists because a DNS switch is bounded by TTL, not by the API call: if the record is
// cached for 24 h, half the clients keep the OLD address for 24 h after the flip. Lower it to
// 300 s a day BEFORE the switch, then this command with --ttl=0 (keep) does the flip.
func runDNSSetA(args []string) error {
	fs := flag.NewFlagSet("dns-set-a", flag.ContinueOnError)
	zone := fs.String("zone", os.Getenv("SKYGATE_DNS_REGAPI_ZONE"), "DNS zone (env: SKYGATE_DNS_REGAPI_ZONE)")
	name := fs.String("name", envOr("SKYGATE_DNS_REGAPI_NAME", "skygate"), "subdomain(s), comma separated, whose A record is switched")
	ip := fs.String("ip", "", "the IPv4 address the record must point at (required)")
	ttl := fs.Int("ttl", 0, "set the record TTL to this many seconds (0 = keep the current TTL)")
	dryRun := fs.Bool("dry-run", false, "print what would change and exit without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*ip) == "" {
		return fmt.Errorf("--ip is required (the address of the host being brought up)")
	}
	names := splitNames(*name)
	if len(names) == 0 {
		return fmt.Errorf("--name is required")
	}
	d, store, client, closeFn, err := openDNSCreds()
	if err != nil {
		return err
	}
	defer closeFn()

	creds, err := loadEffectiveCreds(store)
	if err != nil {
		return err
	}
	if *zone != "" {
		creds.Zone = *zone
	}
	ctx, cancel := context.WithTimeout(context.Background(), dnsCLITimeout)
	defer cancel()

	changed := 0
	for _, n := range names {
		// 1. What does DNS answer right now? A switch that "changed" an already-correct record is
		//    a report the operator must be able to believe.
		_, current, found, lerr := client.RecordsFor(ctx, creds.Zone, n)
		if lerr != nil {
			return lerr
		}
		if !found {
			current = "(none)"
		}
		fmt.Printf("before: %s %s -> %s\n", creds.Zone, n, current)
		fmt.Printf("after:  %s %s -> %s\n", creds.Zone, n, *ip)
		if strings.TrimSpace(current) == strings.TrimSpace(*ip) && *ttl == 0 {
			fmt.Printf("  %s: nothing to do, the record already points there\n", n)
			continue
		}
		if *dryRun {
			fmt.Printf("  %s: dry-run, no write performed\n", n)
			continue
		}
		// 2. Write, then read back inside UpdateRecord (it refuses to report success on a value
		//    it cannot see).
		if uerr := client.UpdateRecord(ctx, creds.Zone, n, *ip); uerr != nil {
			return fmt.Errorf("%s: %w", n, uerr)
		}
		if *ttl > 0 {
			if terr := client.SetTTL(ctx, creds.Zone, n, *ttl); terr != nil {
				return fmt.Errorf("%s: the address was switched but the TTL was not: %w", n, terr)
			}
			fmt.Printf("  %s: TTL set to %d s\n", n, *ttl)
		}
		fmt.Printf("  %s: written and verified by reading the zone back\n", n)
		changed++
	}
	if *dryRun || changed == 0 {
		return nil
	}

	// 3. Audit row: a manual switch must be explainable afterwards without a shell history.
	if _, err := d.ExecContext(ctx,
		`INSERT INTO audit_log (user_id, username, action, detail, created_at)
		 VALUES (0, 'cli', 'dns.manual_switch', $1, `+db.NowUnixSQL()+`)`,
		fmt.Sprintf("zone=%s names=%s to=%s ttl=%d at=%s", creds.Zone, strings.Join(names, ","), *ip, *ttl,
			localtime.Format(time.Now()))); err != nil {
		fmt.Fprintf(os.Stderr, "warning: the DNS change succeeded but the audit row failed: %v\n", err)
	}
	return nil
}

// splitNames parses a comma/space separated list of subdomains.
func splitNames(raw string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if v := strings.TrimSpace(f); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func recordTypeOf(r extcreds.RegRecord) string {
	if r.Rectype != "" {
		return strings.ToUpper(r.Rectype)
	}
	return strings.ToUpper(r.Type)
}

func recordNameOf(r extcreds.RegRecord) string {
	if r.Subname != "" {
		return r.Subname
	}
	return r.Name
}
