// PostgreSQL variant (v1.3.0+; no build tag, always compiled).
// OnConflictDoNothing returns the `ON CONFLICT (<cols>)
// DO NOTHING` suffix for use after a plain
// `INSERT ...` statement. The `cols` argument is the
// unique/PRIMARY KEY column list for the target table;
// PG will error on unknown conflict targets.
//
// InsertIgnorePrefix returns `INSERT` (the plain form);
// the IGNORE semantics are expressed by the
// `ON CONFLICT(...) DO NOTHING` suffix.
package db

func onConflict(cols string) string {
	return " ON CONFLICT (" + cols + ") DO NOTHING"
}

func insertIgnoreVerb() string {
	return "INSERT"
}
