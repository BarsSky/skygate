// apply_pg_migrations — v0.32.24 one-shot binary that runs
// the v0.32.24 PG migrations on the production PG.
//
// Usage:
//   SKYGATE_TEST_PG_DSN=postgres://skygate:...@host:5432/skygate?sslmode=disable \
//     go run -tags postgres ./cmd/apply_pg_migrations
//
// The `postgres` build tag is required to enable the pgx driver.
// The binary applies all PG migrations, then exits 0.
package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"

	"skygate/internal/db"
)

func main() {
	dsn := os.Getenv("SKYGATE_TEST_PG_DSN")
	if dsn == "" {
		log.Fatal("SKYGATE_TEST_PG_DSN must be set")
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer conn.Close()
	if err := conn.Ping(); err != nil {
		log.Fatalf("ping: %v", err)
	}
	log.Printf("applying PG migrations to %s...", redactDSN(dsn))
	if err := db.MigratePostgres(conn); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Printf("migrations applied OK")
	rows, err := conn.Query("SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'")
	if err != nil {
		log.Fatalf("count: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		rows.Scan(&n)
		fmt.Printf("public schema: %d tables\n", n)
	}
}

func redactDSN(dsn string) string {
	const prefix = "://"
	prefixIdx := indexOf(dsn, prefix)
	if prefixIdx < 0 {
		return dsn
	}
	rest := dsn[prefixIdx+len(prefix):]
	atIdx := indexOf(rest, "@")
	if atIdx < 0 {
		return dsn
	}
	scheme := dsn[:prefixIdx+len(prefix)]
	creds := rest[:atIdx]
	host := rest[atIdx+1:]
	colonIdx := indexOf(creds, ":")
	if colonIdx < 0 {
		return dsn
	}
	return scheme + creds[:colonIdx+1] + "***@" + host
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
