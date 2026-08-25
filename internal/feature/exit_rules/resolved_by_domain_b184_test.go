// Package exit_rules — resolved_by_domain_b184_test.go.
// The 7 B184 unit tests in form_admin_b184_test.go cover the
// consumer (ruleApprovedInHeadscale) + key-format
// (ResolvedKeyForTuple) behaviour. The SQL-level integration
// for the producer (LoadResolvedByDomain) is exercised live
// on the VM by scripts/check_b184.sh contract J (live DB
// check) — a SQL-only SELECT is too simple to warrant its
// own table-level test, and the project's test build doesn't
// have github.com/mattn/go-sqlite3 in go.mod, so an in-
// memory SQLite test would require adding a build-time
// dependency just for this one assertion.
package exit_rules
