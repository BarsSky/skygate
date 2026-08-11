//go:build postgres

// PostgreSQL variant (compiled with -tags postgres).
package db

const nowUnix = "EXTRACT(EPOCH FROM now())::bigint"
