// PostgreSQL variant (v1.3.0+; no build tag, always compiled).
package db

const nowUnix = "EXTRACT(EPOCH FROM now())::bigint"
