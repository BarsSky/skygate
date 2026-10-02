// internal/db/convert.go — bidirectional SQLite ↔ Postgres conversion
// tool (B-mod-sqlite-pg-bidi; rewritten so the operator's actual
// direction works).
//
// WHAT THIS FILE DOES
// -------------------
// Convert copies the whole skygate dataset from one database to the
// other, in EITHER direction (SQLite → PostgreSQL and
// PostgreSQL → SQLite), plus the same-dialect copy.
//
// WHY IT WAS REWRITTEN (measured, 2026-09-28)
// -------------------------------------------
// The v1.5.4 implementation this replaces could not do the job its
// own documentation promised:
//
//   - `readSourceSchema` refused every non-SQLite source outright
//     ("only SQLite source supported in v1.5.4"), so the direction the
//     operator actually needs — PostgreSQL → SQLite, i.e. scaling
//     down to a self-host — failed on the first statement.
//   - The target schema was built by regex-substituting six type
//     names inside the SOURCE's `CREATE TABLE` text. There is no
//     index, no trigger, no partial UNIQUE index and no view in that
//     path, and the SQLite connection has `foreign_keys=1`, so the
//     target was structurally incomplete.
//   - `topoSortTables` was an alphabetical sort whose own comment
//     admitted it was not a topological sort.
//   - The doc comment promised "Verifies row counts match
//     (source.count == target.count per table)"; the code did
//     `_ = n` and never compared anything.
//   - No transaction: a mid-copy failure left a half-filled target
//     and the only guidance was "drop the target and retry".
//
// THE ALGORITHM NOW
// -----------------
//  1. List the source's base tables.
//  2. Create the target schema by running the TARGET's OWN migration
//     chain (`ApplyMigrations`). The target therefore gets exactly the
//     schema skygate itself would have created on that dialect —
//     columns, indexes, triggers and partial UNIQUE indexes included.
//     This is what makes the two backends interchangeable rather than
//     merely similar.
//  3. Clear the rows the migrations seeded (global_settings defaults,
//     the id=99 reserved portal user) so the copy is exact.
//  4. Order the tables parents-before-children from the TARGET's own
//     FOREIGN KEY metadata (Kahn's algorithm, deterministic on name).
//     Both backends enforce FKs, so the order has to be real.
//  5. Copy every row inside ONE transaction, coercing each value from
//     the TARGET column's declared type (`coerceValue`). A failure
//     rolls the whole copy back.
//  6. Compare per-table row counts and re-seed the target's identity
//     sequences, then report everything.
//
// The column list for each table is the INTERSECTION of the source's
// and the target's columns, so a table that gained a column in one
// chain still converts — the extra column is reported, not fatal.
//
// MODES (ConvertOptions.Mode)
//   - "schema+data" (default): schema AND data, target cleared first.
//   - "schema-only": schema created, every table left empty.
//   - "data-only": no schema work, no clearing — the target is
//     expected to already carry the schema and be empty.
//
// DryRun performs no writes at all and reports the plan (table list +
// source row counts).
package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ConvertOptions controls Convert behavior.
type ConvertOptions struct {
	// Mode: "schema+data" (default), "schema-only", "data-only".
	Mode string
	// DryRun: when true, Convert reports the plan and performs no
	// writes (neither the target schema nor any row is touched).
	DryRun bool
}

// ConvertTableResult is the per-table outcome of one conversion.
type ConvertTableResult struct {
	Table      string   // table name
	SourceRows int64    // rows in the source at the time of the run
	TargetRows int64    // rows in the target after the run
	Copied     int64    // rows inserted by this run
	Columns    []string // columns actually copied (the intersection)
	// SkippedColumns are source columns the target does not have.
	// Additive drift: the value is dropped, the row is not.
	SkippedColumns []string
	// Skipped is true when the table was not copied at all; Reason
	// says why (missing on the target, no shared columns, ...).
	Skipped bool
	Reason  string
}

// ConvertReport is what a conversion actually did. It replaces the
// old "return the first error and hope" contract: the operator (and
// the /admin/database page) can now see the plan, the per-table row
// counts and whether the result verified.
type ConvertReport struct {
	FromKind DialectKind
	ToKind   DialectKind
	Mode     string
	DryRun   bool
	// SchemaCreated is true when this run created the target schema
	// by applying the target's migration chain.
	SchemaCreated bool
	Tables        []ConvertTableResult
	CopiedRows    int64
	// Verified is true when every copied table's source and target row
	// counts matched.
	Verified bool
	// Warnings are non-fatal findings (same-dialect copy, columns that
	// exist on one side only, a table the target lacks).
	Warnings []string
}

// ColumnNames returns the copied column list for a table, or nil.
func (r *ConvertReport) ColumnNames(table string) []string {
	for _, t := range r.Tables {
		if t.Table == table {
			return t.Columns
		}
	}
	return nil
}

// Convert migrates schema + data from one *sql.DB (any supported
// dialect) to another, discarding the report. Both handles must be
// pre-opened (typically via OpenWithDialect).
//
// Callers that want the row counts and the verification verdict should
// call ConvertWithReport.
func Convert(
	ctx context.Context,
	fromD *Dialect, fromDB *sql.DB,
	toD *Dialect, toDB *sql.DB,
	opts ConvertOptions,
) error {
	_, err := ConvertWithReport(ctx, fromD, fromDB, toD, toDB, opts)
	return err
}

// ConvertWithReport is Convert plus the report described above.
//
// On error the target may hold a schema (created in step 2) but no
// copied rows: the data copy is one transaction and is rolled back as
// a unit. Re-running the conversion is therefore always safe.
func ConvertWithReport(
	ctx context.Context,
	fromD *Dialect, fromDB *sql.DB,
	toD *Dialect, toDB *sql.DB,
	opts ConvertOptions,
) (*ConvertReport, error) {
	if fromD == nil || fromDB == nil || toD == nil {
		return nil, fmt.Errorf("convert: source and target dialects must both be supplied")
	}
	if !opts.DryRun && toDB == nil {
		return nil, fmt.Errorf("convert: the target connection must be open (a dry run needs only the source)")
	}
	if opts.Mode == "" {
		opts.Mode = "schema+data"
	}
	switch opts.Mode {
	case "schema+data", "schema-only", "data-only":
	default:
		return nil, fmt.Errorf("convert: unknown mode %q (want schema+data, schema-only or data-only)", opts.Mode)
	}

	rep := &ConvertReport{
		FromKind: fromD.Kind,
		ToKind:   toD.Kind,
		Mode:     opts.Mode,
		DryRun:   opts.DryRun,
	}
	if fromD.Kind == toD.Kind {
		rep.Warnings = append(rep.Warnings,
			fmt.Sprintf("source and target are both %s — this is a same-dialect copy, not a conversion", fromD.Kind))
	}

	// --- 1. source tables -------------------------------------------------
	srcTables, err := listTables(ctx, fromDB, fromD.Kind)
	if err != nil {
		return nil, fmt.Errorf("convert: list source tables: %w", err)
	}
	if len(srcTables) == 0 {
		return nil, fmt.Errorf("convert: the source database has no tables — is %q really a skygate database?", fromD.dsn)
	}

	if opts.DryRun {
		for _, t := range srcTables {
			n, err := countRows(ctx, fromDB, fromD.Kind, t)
			if err != nil {
				return nil, fmt.Errorf("convert: dry-run count %s: %w", t, err)
			}
			rep.Tables = append(rep.Tables, ConvertTableResult{Table: t, SourceRows: n})
		}
		return rep, nil
	}

	// --- 2. target schema, from the target's own migration chain ----------
	if opts.Mode != "data-only" {
		if err := ApplyMigrations(toDB, toD.Kind); err != nil {
			return nil, fmt.Errorf("convert: apply the %s migration chain to the target: %w", toD.Kind, err)
		}
		rep.SchemaCreated = true
	}

	tgtTables, err := listTables(ctx, toDB, toD.Kind)
	if err != nil {
		return nil, fmt.Errorf("convert: list target tables: %w", err)
	}
	tgtSet := make(map[string]bool, len(tgtTables))
	for _, t := range tgtTables {
		tgtSet[t] = true
	}

	// --- 3. clear the rows the migration chain seeded ---------------------
	// The chain inserts global_settings defaults and a reserved portal
	// user, so a copy into a freshly migrated target would collide on
	// the primary key — and "schema-only" would hand back a database
	// that is not actually empty. "data-only" deliberately does not
	// clear: there the operator asked to merge into an existing schema.
	if opts.Mode != "data-only" {
		for _, t := range srcTables {
			if !tgtSet[t] || isSchemaMetadataTable(t) {
				continue
			}
			if err := deleteAll(ctx, toDB, toD.Kind, t); err != nil {
				return nil, fmt.Errorf("convert: clear seeded rows in %s: %w", t, err)
			}
		}
	}

	if opts.Mode == "schema-only" {
		for _, t := range srcTables {
			res := ConvertTableResult{Table: t}
			if !tgtSet[t] {
				res.Skipped, res.Reason = true, "the target schema has no such table"
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("table %s is missing on the target", t))
			} else if isSchemaMetadataTable(t) {
				res.Skipped, res.Reason = true, "schema metadata — owned by the target's own migration chain"
			} else {
				n, err := countRows(ctx, toDB, toD.Kind, t)
				if err != nil {
					return nil, fmt.Errorf("convert: verify schema-only %s: %w", t, err)
				}
				res.TargetRows = n
				res.Reason = "schema-only: no rows copied"
			}
			rep.Tables = append(rep.Tables, res)
		}
		rep.Verified = true
		return rep, nil
	}

	// --- 4. column sets and the parent-first order ------------------------
	srcCols := make(map[string][]columnInfo, len(srcTables))
	tgtCols := make(map[string][]columnInfo, len(srcTables))
	for _, t := range srcTables {
		sc, err := tableColumns(ctx, fromDB, fromD.Kind, t)
		if err != nil {
			return nil, fmt.Errorf("convert: read source columns of %s: %w", t, err)
		}
		srcCols[t] = sc
		if !tgtSet[t] {
			continue
		}
		tc, err := tableColumns(ctx, toDB, toD.Kind, t)
		if err != nil {
			return nil, fmt.Errorf("convert: read target columns of %s: %w", t, err)
		}
		tgtCols[t] = tc
	}

	edges, err := foreignKeyEdges(ctx, toDB, toD.Kind, srcTables, tgtSet)
	if err != nil {
		return nil, fmt.Errorf("convert: read target foreign keys: %w", err)
	}
	order, err := topoOrder(srcTables, edges)
	if err != nil {
		return nil, err
	}

	// --- 5. copy, in one transaction --------------------------------------
	tx, err := toDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("convert: begin target transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, t := range order {
		res := ConvertTableResult{Table: t}

		srcN, err := countRows(ctx, fromDB, fromD.Kind, t)
		if err != nil {
			return nil, fmt.Errorf("convert: count source %s: %w", t, err)
		}
		res.SourceRows = srcN

		if !tgtSet[t] {
			res.Skipped, res.Reason = true, "the target schema has no such table"
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("table %s is missing on the target — its %d rows were not copied", t, srcN))
			rep.Tables = append(rep.Tables, res)
			continue
		}
		if isSchemaMetadataTable(t) {
			// applied_migrations is SCHEMA state, not operator data: the
			// target's own migration run already recorded it. Copying it
			// would overwrite the target's bookkeeping with the source's.
			res.Skipped, res.Reason = true, "schema metadata — owned by the target's own migration chain"
			rep.Tables = append(rep.Tables, res)
			continue
		}

		cols, skipped := sharedColumns(srcCols[t], tgtCols[t])
		res.Columns, res.SkippedColumns = cols, skipped
		if len(skipped) > 0 {
			rep.Warnings = append(rep.Warnings,
				fmt.Sprintf("table %s: the target has no column(s) %s — those values were dropped",
					t, strings.Join(skipped, ", ")))
		}
		if len(cols) == 0 {
			res.Skipped, res.Reason = true, "no column exists on both sides"
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("table %s has no shared column — skipped", t))
			rep.Tables = append(rep.Tables, res)
			continue
		}

		n, err := copyTable(ctx, tx, fromDB, toD.Kind, t, cols, tgtCols[t])
		if err != nil {
			return nil, fmt.Errorf("convert: copy %s: %w", t, err)
		}
		res.Copied = n
		rep.CopiedRows += n
		rep.Tables = append(rep.Tables, res)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("convert: commit the target transaction: %w", err)
	}
	committed = true

	// --- 6. verify and re-seed the identity sequences ---------------------
	rep.Verified = true
	for i := range rep.Tables {
		res := &rep.Tables[i]
		if res.Skipped {
			continue
		}
		n, err := countRows(ctx, toDB, toD.Kind, res.Table)
		if err != nil {
			return nil, fmt.Errorf("convert: verify %s: %w", res.Table, err)
		}
		res.TargetRows = n
		if n != res.SourceRows {
			rep.Verified = false
			rep.Warnings = append(rep.Warnings,
				fmt.Sprintf("table %s: target has %d rows, source had %d", res.Table, n, res.SourceRows))
		}
	}

	if err := resetSequences(ctx, toDB, toD.Kind, rep); err != nil {
		// A sequence that could not be re-seeded is a real hazard (the
		// next INSERT would collide with a copied id), so it is a
		// warning that names the table rather than a silent success.
		rep.Warnings = append(rep.Warnings, err.Error())
	}

	if !rep.Verified {
		return rep, fmt.Errorf("convert: row counts did not match after the copy — see the report warnings")
	}
	return rep, nil
}

// ---------- schema metadata ----------

// columnInfo is one column of one table, as the owning dialect
// declares it.
type columnInfo struct {
	Name string
	Type string // declared type verbatim ("BIGINT", "integer", "timestamp with time zone")
}

// listTables returns the base table names of a database, sorted.
// SQLite's internal tables (sqlite_sequence, sqlite_stat1, …) are
// excluded; so are PostgreSQL's own catalogs (current_schema only).
func listTables(ctx context.Context, db *sql.DB, kind DialectKind) ([]string, error) {
	var q string
	switch kind {
	case DialectSQLite:
		q = `SELECT name FROM sqlite_master
		     WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		     ORDER BY name`
	case DialectPostgres:
		q = `SELECT table_name FROM information_schema.tables
		     WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
		     ORDER BY table_name`
	default:
		return nil, fmt.Errorf("unsupported dialect %v", kind)
	}
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// tableColumns returns the columns of a table in ordinal order.
func tableColumns(ctx context.Context, db *sql.DB, kind DialectKind, table string) ([]columnInfo, error) {
	switch kind {
	case DialectSQLite:
		// PRAGMA cannot take a bind parameter; quoteIdentifier is the
		// only safe way to interpolate a catalog-sourced name.
		rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+quoteIdentifier(table)+`)`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []columnInfo
		for rows.Next() {
			var (
				cid, notNull, pk int
				name, ctype      string
				dflt             any
			)
			if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
				return nil, err
			}
			out = append(out, columnInfo{Name: name, Type: ctype})
		}
		return out, rows.Err()
	case DialectPostgres:
		rows, err := db.QueryContext(ctx, `
			SELECT column_name, data_type
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1
			ORDER BY ordinal_position`, table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []columnInfo
		for rows.Next() {
			var c columnInfo
			if err := rows.Scan(&c.Name, &c.Type); err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, rows.Err()
	default:
		return nil, fmt.Errorf("unsupported dialect %v", kind)
	}
}

// foreignKeyEdges returns child → parents for the given tables, read
// from the TARGET's own catalog. Tables absent from the target are
// ignored.
func foreignKeyEdges(ctx context.Context, db *sql.DB, kind DialectKind, tables []string, present map[string]bool) (map[string][]string, error) {
	edges := make(map[string][]string, len(tables))
	switch kind {
	case DialectSQLite:
		for _, t := range tables {
			if !present[t] {
				continue
			}
			rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list(`+quoteIdentifier(t)+`)`)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var (
					id, seq         int
					refTable, from  string
					to              any
					onUpdate, onDel string
					match           string
				)
				if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDel, &match); err != nil {
					rows.Close()
					return nil, err
				}
				if present[refTable] && refTable != t {
					edges[t] = append(edges[t], refTable)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			rows.Close()
		}
	case DialectPostgres:
		rows, err := db.QueryContext(ctx, `
			SELECT tc.table_name, ccu.table_name AS referenced
			FROM information_schema.table_constraints tc
			JOIN information_schema.constraint_column_usage ccu
			  ON tc.constraint_name = ccu.constraint_name
			 AND tc.table_schema = ccu.table_schema
			WHERE tc.constraint_type = 'FOREIGN KEY'
			  AND tc.table_schema = current_schema()`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var child, parent string
			if err := rows.Scan(&child, &parent); err != nil {
				return nil, err
			}
			if present[child] && present[parent] && child != parent {
				edges[child] = append(edges[child], parent)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported dialect %v", kind)
	}
	return edges, nil
}

// topoOrder returns the tables parents-before-children using Kahn's
// algorithm, breaking ties by name so two runs agree. A genuine cycle
// is an error naming the tables involved — silently copying them in
// catalogue order would fail the target's FK check halfway through.
func topoOrder(tables []string, edges map[string][]string) ([]string, error) {
	indegree := make(map[string]int, len(tables))
	children := make(map[string][]string, len(tables))
	for _, t := range tables {
		indegree[t] = 0
	}
	for child, parents := range edges {
		for _, p := range parents {
			if _, ok := indegree[p]; !ok {
				continue
			}
			children[p] = append(children[p], child)
			indegree[child]++
		}
	}

	ready := make([]string, 0, len(tables))
	for _, t := range tables {
		if indegree[t] == 0 {
			ready = append(ready, t)
		}
	}
	sort.Strings(ready)

	out := make([]string, 0, len(tables))
	for len(ready) > 0 {
		// Pop the lexicographically smallest ready table for determinism.
		t := ready[0]
		ready = ready[1:]
		out = append(out, t)
		sort.Strings(children[t])
		for _, c := range children[t] {
			indegree[c]--
			if indegree[c] == 0 {
				ready = append(ready, c)
			}
		}
		sort.Strings(ready)
	}

	if len(out) != len(tables) {
		var stuck []string
		for _, t := range tables {
			if indegree[t] > 0 {
				stuck = append(stuck, t)
			}
		}
		sort.Strings(stuck)
		return nil, fmt.Errorf("convert: the target schema has a FOREIGN KEY cycle among %s — "+
			"the tables cannot be filled parent-first", strings.Join(stuck, ", "))
	}
	return out, nil
}

// sharedColumns returns the source columns the target also has, in the
// source's order, plus the source columns the target lacks.
func sharedColumns(src, tgt []columnInfo) (shared, missing []string) {
	have := make(map[string]bool, len(tgt))
	for _, c := range tgt {
		have[strings.ToLower(c.Name)] = true
	}
	for _, c := range src {
		if have[strings.ToLower(c.Name)] {
			shared = append(shared, c.Name)
		} else {
			missing = append(missing, c.Name)
		}
	}
	return shared, missing
}

// ---------- data movement ----------

// countRows returns the row count of a table.
func countRows(ctx context.Context, db *sql.DB, kind DialectKind, table string) (int64, error) {
	_ = kind
	var n int64
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(table)).Scan(&n)
	return n, err
}

// deleteAll empties a table.
func deleteAll(ctx context.Context, db *sql.DB, kind DialectKind, table string) error {
	_ = kind
	_, err := db.ExecContext(ctx, `DELETE FROM `+quoteIdentifier(table))
	return err
}

// copyTable reads every row of the source table and inserts it into
// the target through the caller's transaction, coercing each value to
// the target column's declared type.
func copyTable(
	ctx context.Context,
	tx *sql.Tx,
	fromDB *sql.DB,
	toKind DialectKind,
	table string,
	cols []string,
	tgtColumns []columnInfo,
) (int64, error) {
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdentifier(c)
	}
	selectSQL := fmt.Sprintf("SELECT %s FROM %s", strings.Join(quoted, ", "), quoteIdentifier(table))
	// The placeholders belong to the TARGET: the INSERT runs there.
	insertSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		quoteIdentifier(table), strings.Join(quoted, ", "), toKind.Placeholders(len(cols)))

	typeByCol := make(map[string]columnInfo, len(tgtColumns))
	for _, c := range tgtColumns {
		typeByCol[strings.ToLower(c.Name)] = c
	}

	rows, err := fromDB.QueryContext(ctx, selectSQL)
	if err != nil {
		return 0, fmt.Errorf("select: %w", err)
	}
	defer rows.Close()

	var count int64
	for rows.Next() {
		holders := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range holders {
			ptrs[i] = &holders[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return count, fmt.Errorf("scan row %d: %w", count, err)
		}
		for i, c := range cols {
			v, err := coerceValue(holders[i], typeByCol[strings.ToLower(c)].Type, toKind)
			if err != nil {
				return count, fmt.Errorf("row %d column %s: %w", count, c, err)
			}
			holders[i] = v
		}
		if _, err := tx.ExecContext(ctx, insertSQL, holders...); err != nil {
			return count, fmt.Errorf("insert row %d: %w", count, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, nil
}

// resetSequences advances the target's identity sequences past the
// copied ids. Without this the first application INSERT after a
// conversion reuses id 1 and fails on the primary key — the classic
// silent data-migration trap.
//
// PostgreSQL only: SQLite maintains its own rowid sequence.
func resetSequences(ctx context.Context, db *sql.DB, kind DialectKind, rep *ConvertReport) error {
	if kind != DialectPostgres {
		return nil
	}
	var firstErr error
	for _, t := range rep.Tables {
		if t.Skipped {
			continue
		}
		// Only tables with an `id` column can have a serial/identity
		// sequence. Asking pg_get_serial_sequence about a missing column
		// RAISES (SQLSTATE 42703), it does not return NULL — measured
		// against PostgreSQL 15 on `derp_health`, which is keyed by
		// region_id and has no `id` at all. Checking first keeps a
		// perfectly normal table out of the report's warnings.
		var hasID bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
				  AND table_name = $1
				  AND column_name = 'id'
			)`, t.Table).Scan(&hasID); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("convert: could not check %s for an id column: %w", t.Table, err)
			}
			continue
		}
		if !hasID {
			continue
		}
		// pg_get_serial_sequence returns NULL for a table whose id is not
		// serial/identity; setval is then skipped.
		var seq sql.NullString
		if err := db.QueryRowContext(ctx,
			`SELECT pg_get_serial_sequence($1, 'id')`, t.Table).Scan(&seq); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("convert: could not read the identity sequence of %s: %w", t.Table, err)
			}
			continue
		}
		if !seq.Valid || seq.String == "" {
			continue
		}
		var maxID sql.NullInt64
		if err := db.QueryRowContext(ctx,
			`SELECT MAX(id) FROM `+quoteIdentifier(t.Table)).Scan(&maxID); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("convert: could not read MAX(id) of %s: %w", t.Table, err)
			}
			continue
		}
		if !maxID.Valid || maxID.Int64 <= 0 {
			continue
		}
		if _, err := db.ExecContext(ctx,
			`SELECT setval($1, $2, true)`, seq.String, maxID.Int64); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("convert: could not advance the identity sequence of %s to %d: %w",
					t.Table, maxID.Int64, err)
			}
		}
	}
	return firstErr
}

// ---------- type coercion ----------

// coerceValue converts one scanned source value into something the
// TARGET column accepts.
//
// The driver hands back Go values, and the two dialects disagree about
// what a timestamp, a boolean and a JSON document look like:
//
//	                             SQLite                     PostgreSQL
//	timestamp column declared    INTEGER (unix seconds)     timestamptz
//	scanned Go value             int64 / string             time.Time
//	boolean                      INTEGER 0/1                boolean
//	boolean scanned as           int64                      bool
//	json                         TEXT                       jsonb
//
// So the TARGET's declared type decides the shape, and the source
// value supplies the meaning. `toKind` is only consulted when the
// target type carries no information (an untyped SQLite column).
func coerceValue(v any, targetType string, toKind DialectKind) (any, error) {
	if v == nil {
		return nil, nil
	}
	base := typeBase(targetType)

	switch val := v.(type) {
	case time.Time:
		return timeFor(val, base, toKind), nil
	case bool:
		switch {
		case isBooleanType(base):
			return val, nil
		case isIntegerType(base):
			if val {
				return int64(1), nil
			}
			return int64(0), nil
		default:
			return strconv.FormatBool(val), nil
		}
	case []byte:
		if isBinaryType(base) {
			return val, nil
		}
		// pgx hands jsonb/json back as []byte; SQLite wants TEXT.
		return string(val), nil
	case int64:
		switch {
		case isBooleanType(base):
			return val != 0, nil
		case isTimeType(base):
			return time.Unix(val, 0).UTC(), nil
		case isTextType(base):
			return strconv.FormatInt(val, 10), nil
		default:
			return val, nil
		}
	case float64:
		switch {
		case isIntegerType(base):
			return int64(val), nil
		case isTextType(base):
			return strconv.FormatFloat(val, 'f', -1, 64), nil
		default:
			return val, nil
		}
	case string:
		switch {
		case isBooleanType(base):
			switch strings.ToLower(strings.TrimSpace(val)) {
			case "1", "t", "true", "yes", "on":
				return true, nil
			case "0", "f", "false", "no", "off":
				return false, nil
			default:
				return nil, fmt.Errorf("cannot read %q as a boolean", val)
			}
		case isIntegerType(base):
			if n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64); err == nil {
				return n, nil
			}
			return val, nil
		case isTimeType(base):
			t, err := parseDBTimestamp(val)
			if err != nil {
				return nil, err
			}
			return t, nil
		case isBinaryType(base):
			return []byte(val), nil
		default:
			return val, nil
		}
	}
	return v, nil
}

// timeFor renders a time.Time for the target column.
func timeFor(t time.Time, base string, toKind DialectKind) any {
	switch {
	case isIntegerType(base):
		return t.Unix()
	case isTextType(base) && !isTimeType(base):
		return t.UTC().Format(time.RFC3339Nano)
	case isTimeType(base):
		return t
	default:
		// Untyped / unknown target column: use the dialect's own
		// convention, exactly as DialectKind.TimeValue does.
		return toKind.TimeValue(t)
	}
}

// parseDBTimestamp accepts the timestamp shapes the two backends
// actually store. SQLite's cluster tables default to CURRENT_TIMESTAMP
// ("2006-01-02 15:04:05") while the rest of the schema stores unix
// seconds, and RFC3339 comes from the application's own writes.
func parseDBTimestamp(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot read %q as a timestamp", s)
}

// typeBase strips a length/precision suffix and upper-cases the type
// name: "VARCHAR(255)" → "VARCHAR", "numeric(10,2)" → "NUMERIC".
func typeBase(t string) string {
	t = strings.ToUpper(strings.TrimSpace(t))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	return t
}

func isIntegerType(base string) bool {
	switch base {
	case "INTEGER", "INT", "BIGINT", "SMALLINT", "TINYINT", "MEDIUMINT",
		"INT2", "INT4", "INT8", "SERIAL", "BIGSERIAL", "SMALLSERIAL":
		return true
	}
	return false
}

func isBooleanType(base string) bool {
	return base == "BOOLEAN" || base == "BOOL"
}

func isTimeType(base string) bool {
	switch base {
	case "TIMESTAMP", "TIMESTAMPTZ", "DATETIME", "DATE",
		"TIMESTAMP WITH TIME ZONE", "TIMESTAMP WITHOUT TIME ZONE",
		"TIME WITH TIME ZONE", "TIME WITHOUT TIME ZONE":
		return true
	}
	return false
}

func isBinaryType(base string) bool {
	switch base {
	case "BYTEA", "BLOB", "BINARY", "VARBINARY", "IMAGE":
		return true
	}
	return false
}

func isTextType(base string) bool {
	switch base {
	case "TEXT", "VARCHAR", "CHARACTER VARYING", "CHAR", "CHARACTER",
		"CLOB", "STRING", "NVARCHAR", "NCHAR", "UUID", "JSON", "JSONB", "XML":
		return true
	}
	return false
}

// quoteIdentifier quotes a catalog-sourced table/column name for both
// supported dialects (both accept ANSI double quotes; an embedded
// quote is doubled).
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// isSchemaMetadataTable reports whether a table holds schema state
// rather than operator data. `applied_migrations` is written by the
// migration chain that just ran on the target; copying it from the
// source would replace the target's own bookkeeping, and clearing it
// in schema-only mode would make a freshly created database look
// unmigrated.
func isSchemaMetadataTable(name string) bool {
	return strings.EqualFold(name, "applied_migrations")
}
