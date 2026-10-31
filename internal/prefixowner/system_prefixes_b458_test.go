package prefixowner

// system_prefixes_b458_test.go — B458: infrastructure prefixes must be carried by the same
// table (and therefore the same advertisement, ACL pin and page) as everything else, and
// must not fight the operator's own decisions.
//
// The live defect behind this: the tailnet's DNS resolvers were carried only because a user
// rule happened to claim one of them; `1.1.1.1` was carried by nobody, so half of every
// client's DNS left the tailnet. Whoever deleted that rule would have taken DNS away from
// the whole tailnet.

import (
	"testing"

	skygatedb "skygate/internal/db"
)

func newB458DB(t *testing.T) *skygatedb.FixedDBSource {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("file::memory:?cache=shared")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.ApplyMigrations(d, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	return &skygatedb.FixedDBSource{DB: d}
}

func ownerOf(t *testing.T, src *skygatedb.FixedDBSource, prefix string) (owner, source string) {
	t.Helper()
	if err := src.DB.QueryRow(`SELECT COALESCE(exit_node_id,''), COALESCE(source,'') FROM prefix_owner WHERE prefix = $1`, prefix).
		Scan(&owner, &source); err != nil {
		t.Fatalf("read %s: %v", prefix, err)
	}
	return owner, source
}

// TestB458_EnsureSystemPrefixesCarriesTheResolvers — the core: a resolver prefix with NO
// rule behind it gets an owner and source='system', and a second pass is a no-op.
func TestB458_EnsureSystemPrefixesCarriesTheResolvers(t *testing.T) {
	src := newB458DB(t)
	added, removed, err := EnsureSystemPrefixes(src.DB, []string{"1.1.1.1/32", "8.8.8.8/32"}, []string{"emilia", "karolina"}, nil)
	if err != nil {
		t.Fatalf("EnsureSystemPrefixes: %v", err)
	}
	if len(added) != 2 || len(removed) != 0 {
		t.Fatalf("added=%v removed=%v, want both prefixes added", added, removed)
	}
	for _, p := range []string{"1.1.1.1/32", "8.8.8.8/32"} {
		owner, source := ownerOf(t, src, p)
		if owner == "" || source != SystemSource {
			t.Errorf("%s: owner=%q source=%q, want an owner and source=%s", p, owner, source, SystemSource)
		}
	}
	// Idempotent: a second pass must not re-insert, re-owner or churn the row.
	added2, removed2, err := EnsureSystemPrefixes(src.DB, []string{"1.1.1.1/32", "8.8.8.8/32"}, []string{"emilia", "karolina"}, nil)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(added2) != 0 || len(removed2) != 0 {
		t.Fatalf("second pass changed the table: added=%v removed=%v", added2, removed2)
	}
}

// TestB458_SystemPrefixNeverOverridesTheOperatorsChoice — a prefix a rule already claims (or
// the operator pinned) is the stronger statement and must survive unchanged.
func TestB458_SystemPrefixNeverOverridesTheOperatorsChoice(t *testing.T) {
	src := newB458DB(t)
	if _, err := src.DB.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
		VALUES ('8.8.8.8/32', 'karolina', 'manual', 1, 1, 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	added, _, err := EnsureSystemPrefixes(src.DB, []string{"8.8.8.8/32", "1.1.1.1/32"}, []string{"emilia"}, nil)
	if err != nil {
		t.Fatalf("EnsureSystemPrefixes: %v", err)
	}
	if len(added) != 1 || added[0] != "1.1.1.1/32" {
		t.Fatalf("added=%v, want only the prefix nobody owned", added)
	}
	owner, source := ownerOf(t, src, "8.8.8.8/32")
	if owner != "karolina" || source != "manual" {
		t.Fatalf("the operator's pin was overwritten: owner=%q source=%q", owner, source)
	}
}

// TestB458_RemovingAResolverReleasesThePrefix — the ONE place a system row may die: the
// reason is gone. Without this, a resolver removed from headscale's config would keep being
// advertised for ever.
func TestB458_RemovingAResolverReleasesThePrefix(t *testing.T) {
	src := newB458DB(t)
	if _, _, err := EnsureSystemPrefixes(src.DB, []string{"1.1.1.1/32", "8.8.8.8/32"}, []string{"emilia"}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	added, removed, err := EnsureSystemPrefixes(src.DB, []string{"8.8.8.8/32"}, []string{"emilia"}, nil)
	if err != nil {
		t.Fatalf("EnsureSystemPrefixes: %v", err)
	}
	if len(added) != 0 || len(removed) != 1 || removed[0] != "1.1.1.1/32" {
		t.Fatalf("added=%v removed=%v, want 1.1.1.1/32 released", added, removed)
	}
	if len(SystemRows(src.DB)) != 1 {
		t.Fatalf("SystemRows = %v, want only 8.8.8.8/32", SystemRows(src.DB))
	}
}

// TestB458_PruneKeepsSystemRowsAndDoesNotKeepUnknownOnes — Prune runs on every maintenance
// pass and knows nothing about DNS: if it treated a system row like an auto row, the route
// would be deleted and re-added on a five-minute cycle and flap on the wire.
func TestB458_PruneKeepsSystemRowsAndDoesNotKeepUnknownOnes(t *testing.T) {
	src := newB458DB(t)
	if _, _, err := EnsureSystemPrefixes(src.DB, []string{"1.1.1.1/32"}, []string{"emilia"}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// An auto row with no claim must still be pruned — the guard must not become a
	// catch-all for "somebody will decide later".
	if _, err := src.DB.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
		VALUES ('203.0.113.0/24', 'emilia', 'auto', 1, 1, 0)`); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	n, err := Prune(src.DB, map[string]bool{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1 (the stale auto row)", n)
	}
	if got := SystemRows(src.DB); len(got) != 1 || got[0] != "1.1.1.1/32" {
		t.Fatalf("the system row did not survive Prune: %v", got)
	}
}
