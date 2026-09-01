// v1.5.0+ / B202 — unit tests for the dump/restore/cleanup
// helpers (no DB needed).

package steps

import (
	"os"
	"testing"
)

func TestParseLibpqDSN(t *testing.T) {
	cases := []struct {
		name     string
		dsn      string
		host     string
		port     string
		dbname   string
		user     string
		sslmode  string
		wantOK   bool
	}{
		{
			name:    "standard",
			dsn:     "postgres://skyadmin:secret@127.0.0.1:5433/skygate_staging?sslmode=disable",
			host:    "127.0.0.1",
			port:    "5433",
			dbname:  "skygate_staging",
			user:    "skyadmin",
			sslmode: "disable",
			wantOK:  true,
		},
		{
			name:    "no password",
			dsn:     "postgres://skyadmin@host/db",
			host:    "host",
			dbname:  "db",
			user:    "skyadmin",
			wantOK:  true,
		},
		{
			name:    "no port",
			dsn:     "postgres://u@h/d",
			host:    "h",
			dbname:  "d",
			user:    "u",
			wantOK:  true,
		},
		{
			name:   "no scheme",
			dsn:    "127.0.0.1:5432/db",
			wantOK: false,
		},
		{
			name:   "empty",
			dsn:    "",
			wantOK: false,
		},
		{
			name:   "no db",
			dsn:    "postgres://u@h",
			wantOK: false,
		},
		{
			name:    "postgresql scheme",
			dsn:     "postgresql://u@h/d?sslmode=require",
			host:    "h",
			dbname:  "d",
			user:    "u",
			sslmode: "require",
			wantOK:  true,
		},
		{
			name:    "trailing slash + params",
			dsn:     "postgres://u@h:5432/d?sslmode=prefer&application_name=test",
			host:    "h",
			port:    "5432",
			dbname:  "d",
			user:    "u",
			sslmode: "prefer",
			wantOK:  true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host, port, dbname, user, sslmode, ok := parseLibpqDSN(c.dsn)
			if ok != c.wantOK {
				t.Errorf("parseLibpqDSN(%q) ok = %v, want %v", c.dsn, ok, c.wantOK)
				return
			}
			if !ok {
				return // fields are not meaningful when ok=false
			}
			if host != c.host {
				t.Errorf("host = %q, want %q", host, c.host)
			}
			if port != c.port {
				t.Errorf("port = %q, want %q", port, c.port)
			}
			if dbname != c.dbname {
				t.Errorf("dbname = %q, want %q", dbname, c.dbname)
			}
			if user != c.user {
				t.Errorf("user = %q, want %q", user, c.user)
			}
			if sslmode != c.sslmode {
				t.Errorf("sslmode = %q, want %q", sslmode, c.sslmode)
			}
		})
	}
}

func TestQuoteIdent(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"skygate_staging", `"skygate_staging"`},
		{"my-staging-db", `"my-staging-db"`},
		{`weird"quote`, `"weird""quote"`},
		{"", `""`},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := quoteIdent(c.in); got != c.want {
				t.Errorf("quoteIdent(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestPgDumpMagic(t *testing.T) {
	// Sanity check: the magic bytes are what we expect.
	want := [4]byte{0x50, 0x47, 0x44, 0x0a}
	if pgDumpMagic != want {
		t.Errorf("pgDumpMagic = %x, want %x", pgDumpMagic, want)
	}
}

func TestSumCounts(t *testing.T) {
	if got := sumCounts(map[string]int64{"a": 1, "b": 2, "c": 3}); got != 6 {
		t.Errorf("sumCounts = %d, want 6", got)
	}
	if got := sumCounts(map[string]int64{}); got != 0 {
		t.Errorf("sumCounts empty = %d, want 0", got)
	}
}

func TestMapsEqual(t *testing.T) {
	if !mapsEqual(map[string]int64{"a": 1}, map[string]int64{"a": 1}) {
		t.Error("expected equal")
	}
	if mapsEqual(map[string]int64{"a": 1}, map[string]int64{"a": 2}) {
		t.Error("expected different (values differ)")
	}
	if mapsEqual(map[string]int64{"a": 1}, map[string]int64{"b": 1}) {
		t.Error("expected different (keys differ)")
	}
	if mapsEqual(map[string]int64{"a": 1, "b": 2}, map[string]int64{"a": 1}) {
		t.Error("expected different (lengths differ)")
	}
}

func TestUnionKeys(t *testing.T) {
	got := unionKeys(map[string]int64{"a": 1, "b": 2}, map[string]int64{"b": 3, "c": 4})
	if len(got) != 3 {
		t.Errorf("unionKeys len = %d, want 3 (got %v)", len(got), got)
	}
	// The order is map-iteration order, which is
	// non-deterministic in Go. We only check the SET
	// here, not the order — the caller in verify.go
	// does sort.Strings before using it.
	seen := map[string]bool{}
	for _, k := range got {
		seen[k] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !seen[want] {
			t.Errorf("missing key %q in unionKeys result %v", want, got)
		}
	}
}

func TestKeyTables(t *testing.T) {
	// Pin the table set so a refactor that changes
	// what we count gets caught at test time.
	want := []string{
		"portal_users",
		"device_rules",
		"node_owner_map",
		"preauth_keys",
		"user_exit_node_prefs",
		"device_exit_node_prefs",
	}
	if len(keyTables) != len(want) {
		t.Errorf("keyTables len = %d, want %d", len(keyTables), len(want))
	}
	for i, wantT := range want {
		if i >= len(keyTables) || keyTables[i] != wantT {
			t.Errorf("keyTables[%d] = %q, want %q", i, keyTables[i], wantT)
		}
	}
}

func TestReadFirstBytes(t *testing.T) {
	// Write a known byte sequence and read the first 4.
	// We use t.TempDir() for the test file.
	dir := t.TempDir()
	path := dir + "/test.bin"
	// PGD magic
	content := []byte{0x50, 0x47, 0x44, 0x0a, 0x00, 0x01, 0x02}
	if err := writeFileBytes(path, content); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readFirstBytes(path, 4)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var want [4]byte
	copy(want[:], content)
	var gotArr [4]byte
	copy(gotArr[:], got)
	if gotArr != want {
		t.Errorf("readFirstBytes = %x, want %x", gotArr, want)
	}
}

func TestReadFirstBytes_ShortFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/short.bin"
	if err := writeFileBytes(path, []byte{0x50, 0x47}); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := readFirstBytes(path, 4)
	if err == nil {
		t.Error("expected error for short file")
	}
}

func TestReadFirstBytes_Missing(t *testing.T) {
	_, err := readFirstBytes("/nonexistent/path/file.dump", 4)
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// writeFileBytes is a tiny test helper.
func writeFileBytes(path string, content []byte) error {
	return os.WriteFile(path, content, 0o644)
}
