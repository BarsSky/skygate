// system_tests_domain_rows_b406.go — B406 (2026-10-21).
//
// THE TEST MUST LOOK FOR A COVERING PREFIX, NOT FOR AN EXACT /32.
//
// Measured on the operator's installation: `exit_rules.domain_prefix` reported
// 8 of 12 addresses "stale or missing", and the same tree wrote **215** derived
// subnet rows that very day with `domain_resolve_at:*` keys refreshed minutes
// ago. Both facts were true because the writer and the reader disagreed about
// what a derived row IS:
//
//   - the CDN path (B183/B274/B298) writes the provider's RANGE —
//     `cdn:cloudflare:registry.npmjs.org` → `104.16.0.0/12`, 15 rows per domain;
//   - the plain path writes the resolved address as `/32`.
//
// The test asked "is there a row whose target_value is exactly `ip/32`?" — which
// is false for every range-covered domain, so a healthy rule looked rotten. The
// operator's only remedy would have been pressing Re-sync forever, and the real
// staleness (a domain whose row set no longer contains its address at all) was
// buried in the same message.
//
// So the question becomes the one the ACL actually asks: does any enabled row
// of THIS device CONTAIN the address the domain resolves to today? Containment
// is computed on netip.Prefix values, in a pure helper, because a silent
// containment bug would be indistinguishable from "everything is stale".
package admin

import (
	"context"
	"database/sql"
	"net/netip"
)

// b406Prefixes turns stored target values into prefixes, ignoring anything that
// is not a CIDR (a domain row, an empty value, a malformed legacy entry). The
// caller compares prefixes with addresses, so a skipped value can only cause a
// false "stale" — never a false "covered".
func b406Prefixes(values []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(values))
	for _, v := range values {
		if p, err := netip.ParsePrefix(v); err == nil {
			out = append(out, p.Masked())
		}
	}
	return out
}

// b406Covers reports whether any prefix contains the address.
func b406Covers(prefixes []netip.Prefix, addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// b406DevicePrefixes loads the enabled subnet rows of one device. Errors are
// returned rather than swallowed: "I could not read the rows" and "the rows do
// not cover this address" are different facts and must not share a message
// (AGENTS trap #15).
func b406DevicePrefixes(ctx context.Context, d *sql.DB, deviceID int64) ([]netip.Prefix, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT target_value FROM device_rules
		 WHERE device_id = $1 AND enabled = 1 AND target_type = 'subnet'`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var vals []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		vals = append(vals, v)
	}
	return b406Prefixes(vals), rows.Err()
}
