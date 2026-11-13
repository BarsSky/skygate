// cmd/skygate/prefix_owner_cli_b470.go — the operator CLI for "which relay serves this prefix",
// and for moving the RULES that claim it.
//
// WHY THIS EXISTS (B470, measured 2026-11-14). The operator reported that Discord routing on
// device `basic` did not work and did not cover everything. The measurement found a disagreement
// between the two halves of one decision:
//
//   - the DATA plane said the Cloudflare ranges (104.16.0.0/12, 162.158.0.0/15, 172.64.0.0/13 …)
//     belong to `emilia`, so every per-CIDR grant for them carried via=[emilia];
//   - the DEVICE `basic` exits through `karolina`;
//
// and `via` is a permission FILTER (B276), so a device on the other relay loses those prefixes
// silently. The ownership itself was a TIE (one device claiming karolina = basic, one claiming
// emilia = skyworker), broken alphabetically by the hostname tie-break — and skyworker's rules had
// drifted to emilia in the first place.
//
// The panel can do both of these one row at a time (/admin/exit-nodes per-prefix pin, /admin/exit-rules
// per-rule relay). A decision like "all Cloudflare ranges go to karolina" is 21 prefixes and 60
// rule rows, so it needs one command that is auditable and repeatable — which is what this file is.
// It deliberately REUSES the same two primitives the panel uses (prefixowner.SetManual and
// db.UpdateDeviceRuleExitNode) instead of re-deriving anything:
//
//	skygate prefix-owner --list [--relay=R] [--prefix=C1,C2]
//	skygate prefix-owner --relay=karolina --prefix=C1,C2 [--apply] [--no-acl]
//	skygate prefix-owner --rules-to=karolina --device=skyworker --target=C1,C2 [--apply]
//
// Writes happen ONLY with --apply; without it the command prints the plan. Every write is audited
// with the same action names the panel uses (prefix_owner_pin / device_rule_relay_move), and a pin
// run finishes by re-applying the ACL — because prefixowner.Assign keeps a manual row it finds, so
// no "change" is reported on the next sync and, without the explicit re-apply, the pin would sit in
// the database while headscale kept serving the old relay (B277's own lesson).
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"skygate/internal/acl"
	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/headscale"
	"skygate/internal/prefixowner"
)

// runOpsCLI dispatches the operator verbs that share the "act on the live installation" shape.
func runOpsCLI(verb string, args []string) error {
	switch verb {
	case "dns-records", "dns-set-a":
		return runDNSCLI(verb, args)
	case "prefix-owner":
		return runPrefixOwner(args)
	}
	return fmt.Errorf("unknown operator verb %q", verb)
}

// runPrefixOwner is the `prefix-owner` verb.
func runPrefixOwner(args []string) error {
	fs := flag.NewFlagSet("prefix-owner", flag.ContinueOnError)
	list := fs.Bool("list", false, "list the current ownership (also the default when no write flag is given)")
	prefixes := fs.String("prefix", "", "prefix(es), comma separated (e.g. 104.16.0.0/12,162.158.0.0/15)")
	relay := fs.String("relay", "", "pin the listed prefixes to this relay hostname (empty = hand them back to the engine)")
	rulesTo := fs.String("rules-to", "", "with --device/--target: rewrite those device_rules rows to this relay")
	auto := fs.Bool("auto", false, "clear the manual pin for --prefix and hand the decision back to the engine")
	device := fs.String("device", "", "device hostname(s) for --rules-to, comma separated")
	targets := fs.String("target", "", "target_value(s) for --rules-to (CIDR or domain), comma separated")
	apply := fs.Bool("apply", false, "perform the write; without it the command only prints the plan")
	noACL := fs.Bool("no-acl", false, "skip the ACL re-apply after a pin (the next sync will not do it for you)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	pool, err := openCLIDB(cfg)
	if err != nil {
		return fmt.Errorf("db open: %w", err)
	}
	defer pool.Close()

	write := *relay != "" || *rulesTo != "" || *auto
	if !write || *list {
		return printPrefixOwnership(pool, splitCSV(*prefixes), *relay)
	}
	if *prefixes == "" && *rulesTo == "" {
		return fmt.Errorf("nothing to do: give --prefix (to pin) or --rules-to with --device/--target (to move rules)")
	}

	if *rulesTo != "" {
		if err := moveRuleRelays(pool, *device, *targets, *rulesTo, *apply); err != nil {
			return err
		}
	}
	if *auto {
		*prefixes = strings.TrimSpace(*prefixes)
		if *prefixes == "" {
			return fmt.Errorf("--auto needs --prefix")
		}
		return pinPrefixes(pool, splitCSV(*prefixes), "", *apply)
	}
	if *prefixes != "" {
		if err := pinPrefixes(pool, splitCSV(*prefixes), *relay, *apply); err != nil {
			return err
		}
		if *apply && !*noACL {
			return reapplyACL(pool, cfg)
		}
	}
	return nil
}

// splitCSV splits a comma/space separated list, dropping empties.
func splitCSV(raw string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		if v := strings.TrimSpace(f); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// printPrefixOwnership prints the current assignment table, optionally filtered.
func printPrefixOwnership(pool *sql.DB, prefixes []string, onlyRelay string) error {
	rows, err := pool.Query(`
		SELECT prefix, exit_node_id, source, claims, devices, updated_at
		  FROM prefix_owner
		 ORDER BY prefix`)
	if err != nil {
		return fmt.Errorf("read prefix_owner: %w", err)
	}
	defer rows.Close()
	want := map[string]bool{}
	for _, p := range prefixes {
		want[p] = true
	}
	n := 0
	fmt.Printf("%-24s %-12s %-10s %s\n", "prefix", "relay", "source", "claims/devices")
	for rows.Next() {
		var prefix, owner, source string
		var claims, devices int64
		var at int64
		if err := rows.Scan(&prefix, &owner, &source, &claims, &devices, &at); err != nil {
			return err
		}
		if len(want) > 0 && !want[prefix] {
			continue
		}
		if onlyRelay != "" && owner != onlyRelay {
			continue
		}
		fmt.Printf("%-24s %-12s %-10s %d/%d\n", prefix, owner, source, claims, devices)
		n++
	}
	fmt.Printf("\n%d prefix(es)\n", n)
	return rows.Err()
}

// pinPrefixes writes the manual ownership rows (source='manual'), the same call the panel's
// per-prefix action makes.
func pinPrefixes(pool *sql.DB, prefixes []string, relay string, apply bool) error {
	if len(prefixes) == 0 {
		return fmt.Errorf("--prefix is empty")
	}
	sort.Strings(prefixes)
	for _, p := range prefixes {
		if !apply {
			fmt.Printf("would pin %-24s -> %s\n", p, relay)
			continue
		}
		if err := prefixowner.SetManual(pool, p, relay); err != nil {
			return fmt.Errorf("pin %s -> %s: %w", p, relay, err)
		}
		auditOps(pool, "prefix_owner_pin", fmt.Sprintf("prefix=%s relay=%s (cli)", p, relay))
		fmt.Printf("pinned  %-24s -> %s\n", p, relay)
	}
	if !apply {
		fmt.Println("\ndry run: nothing written (add --apply)")
	}
	return nil
}

// moveRuleRelays rewrites device_rules.exit_node_id for the selected rows — the operator's declared
// intent, which the assignment engine reads on its next pass.
func moveRuleRelays(pool *sql.DB, devices, targets, relay string, apply bool) error {
	devList := splitCSV(devices)
	tgtList := splitCSV(targets)
	if len(devList) == 0 || len(tgtList) == 0 {
		return fmt.Errorf("--rules-to needs --device and --target")
	}
	// B265's lesson: the hostname a node carries is lower-cased, so a `--device=Basic` typed from
	// memory must match the row, not report "no rows match" on a typo that looks right.
	lowerDev := make([]string, len(devList))
	for i, v := range devList {
		lowerDev[i] = strings.ToLower(v)
	}
	// B470: the row selection must speak the DATABASE'S dialect, not PostgreSQL's. The
	// shipped form was `device_hostname = ANY($1) AND target_value = ANY($2)`, which a
	// PostgreSQL-only reading of "the CLI talks to the live database" made look fine — but
	// the operator's OTHER install runs SQLite (no `ANY(array)`), so the command would have
	// died on exactly the host where a relay has to be moved by hand. Two IN lists built from
	// db.PlaceholdersRange are portable: `$N` works on pgx AND modernc/sqlite.
	q := `SELECT id, device_hostname, target_type, target_value, exit_node_id
	        FROM device_rules
	       WHERE lower(device_hostname) IN (` + db.PlaceholdersRange(1, len(devList)) + `)
	         AND target_value IN (` + db.PlaceholdersRange(len(devList)+1, len(devList)+len(tgtList)) + `)
	       ORDER BY device_hostname, target_value`
	args := make([]any, 0, len(devList)+len(tgtList))
	for _, v := range lowerDev {
		args = append(args, v)
	}
	for _, v := range tgtList {
		args = append(args, v)
	}
	rows, err := pool.Query(q, args...)
	if err != nil {
		return fmt.Errorf("read device_rules: %w", err)
	}
	type hit struct {
		id      int
		device  string
		typ     string
		target  string
		current string
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.id, &h.device, &h.typ, &h.target, &h.current); err != nil {
			rows.Close()
			return err
		}
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(hits) == 0 {
		return fmt.Errorf("no device_rules rows match device=%v target=%v", devList, tgtList)
	}
	moved := 0
	for _, h := range hits {
		if h.current == relay {
			continue
		}
		if !apply {
			fmt.Printf("would move rule %-7d %-10s %-24s %s -> %s\n", h.id, h.device, h.target, h.current, relay)
			continue
		}
		if err := db.UpdateDeviceRuleExitNode(pool, h.id, relay); err != nil {
			return fmt.Errorf("rule %d: %w", h.id, err)
		}
		moved++
	}
	if !apply {
		fmt.Println("\ndry run: nothing written (add --apply)")
		return nil
	}
	auditOps(pool, "device_rule_relay_move", fmt.Sprintf("devices=%v targets=%d to=%s rows=%d (cli)", devList, len(tgtList), relay, moved))
	fmt.Printf("\nmoved %d rule row(s) to %s\n", moved, relay)
	// The ownership table reads these rules, and B276 regenerates + pushes the policy when an
	// owner CHANGES — so this verb does not re-apply the ACL itself. Say it, so nobody waits for
	// a "pushed" line that will never come or wonders whether the move took effect.
	fmt.Println("the ownership engine re-reads these rules on its next pass; if the move changes the " +
		"owner of a prefix, the ACL follows automatically (B276)")
	return nil
}

// reapplyACL regenerates and pushes the policy from the current database state — the same call the
// /admin/exit-nodes "resync" button makes.
//
// B470 (2026-11-14): the SCHEMA is taken from the LIVE policy, not from this process's
// SKYGATE_ACL_VIA_ENABLED. The first live run of this command pinned 21 prefixes and then failed
// the push with
//
//	headscale PUT /api/v1/policy: 500 … cannot unmarshal JSON string into Go v2.AliasWithPorts
//	within "/acls/5/dst/0": … invalid port range format
//
// because a bare shell has no SKYGATE_ACL_VIA_ENABLED, so the CLI generated the legacy `acls`
// schema against a deployment that runs `grants[]`+`via` — and the fix for that alone would have
// been worse than the bug: a VALID legacy policy would have applied (dismantling every `via` pin
// until the app's own next pass). Mirroring what headscale is serving keeps the command faithful to
// the installation it is editing, and an explicit disagreement between the env and the live policy
// is reported instead of silently resolved.
func reapplyACL(pool *sql.DB, cfg *config.Config) error {
	hs := headscale.New(cfg.HeadscaleURL, cfg.HeadscaleKey)
	useVia := cfg.ACLWithViaEnabled
	if detected, why, err := livePolicyUsesVia(hs); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read the live policy (%v) — regenerating the schema "+
			"SKYGATE_ACL_VIA_ENABLED=%v asks for\n", err, cfg.ACLWithViaEnabled)
	} else {
		useVia = detected
		fmt.Printf("live policy: %s — regenerating in the same schema\n", why)
		if detected != cfg.ACLWithViaEnabled {
			fmt.Fprintf(os.Stderr, "note: SKYGATE_ACL_VIA_ENABLED=%v in THIS shell disagrees with the "+
				"live policy (%s); the live schema wins so the push cannot silently drop via pins\n",
				cfg.ACLWithViaEnabled, why)
		}
	}
	results := acl.ApplyACLForAllPlanes(pool,
		func(string) *headscale.Client { return hs },
		nil,
		"cli",
		"acl resync from `skygate prefix-owner` (prefix ownership pins)",
		useVia,
	)
	for _, res := range results {
		if res.Err != nil {
			return fmt.Errorf("ACL re-apply (plane %s): %w", res.PlaneURL, res.Err)
		}
	}
	fmt.Printf("ACL regenerated and pushed (%d plane(s)) — the new via pins are live\n", len(results))
	return nil
}

// livePolicyUsesVia answers "which schema is headscale serving RIGHT NOW?" and names the evidence.
// A policy whose top level carries a non-empty `grants[]` is the via-enabled form; anything else is
// the legacy `acls[]` form. headscale may serve HuJSON-adjacent text, so an unparsable document
// falls back to a key probe rather than guessing from the error.
func livePolicyUsesVia(hs *headscale.Client) (useVia bool, why string, err error) {
	live, err := hs.GetACL()
	if err != nil {
		return false, "", err
	}
	var doc struct {
		Grants []json.RawMessage `json:"grants"`
		ACLs   []json.RawMessage `json:"acls"`
	}
	if uerr := json.Unmarshal([]byte(live), &doc); uerr != nil {
		if strings.Contains(live, `"grants"`) {
			return true, "unparsable policy that mentions grants[]", nil
		}
		return false, "unparsable policy with no grants[] key", nil
	}
	if len(doc.Grants) > 0 {
		return true, fmt.Sprintf("grants[] with %d grant(s)", len(doc.Grants)), nil
	}
	return false, fmt.Sprintf("acls[] with %d rule(s)", len(doc.ACLs)), nil
}

// auditOps writes one audit row. A CLI action on the live installation must be explainable later
// without a shell history.
func auditOps(pool *sql.DB, action, detail string) {
	if _, err := pool.Exec(
		`INSERT INTO audit_log (user_id, username, action, detail, created_at)
		 VALUES (0, 'cli', $1, $2, `+db.NowUnixSQL()+`)`, action, detail); err != nil {
		fmt.Fprintf(os.Stderr, "warning: audit row %s failed: %v\n", action, err)
	}
}
