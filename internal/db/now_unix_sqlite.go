//go:build !postgres

// SQLite variant (default build).
package db

const nowUnix = "strftime('%s', 'now')"
