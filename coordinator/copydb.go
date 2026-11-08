package coordinator

import (
	"database/sql"
	"fmt"
	"strings"

	"sharetoken/internal/sqlite"
)

// copyTables lists every table of the shared database, parents before the
// tables whose foreign keys point at them.
var copyTables = []string{
	"meta", "users", "api_tokens", "devices", "relays",
	"ledger_accounts", "ledger_holds", "ledger_entries",
	"jobs", "nodes", "peers", "audits",
}

// CopyDatabase copies the whole shared database from src to dst, typically
// from a SQLite file to a new MySQL database (portal and coordinator moving to
// separate machines). Both are migrated first; dst must be empty. Run it with
// the portal and the coordinator stopped. progress, if not nil, is called
// once per table.
func CopyDatabase(src, dst *sql.DB, progress func(table string, rows int64)) error {
	if err := Migrate(src); err != nil {
		return fmt.Errorf("copy: migrate source: %w", err)
	}
	if err := Migrate(dst); err != nil {
		return fmt.Errorf("copy: migrate destination: %w", err)
	}
	for _, t := range copyTables {
		var n int64
		if err := dst.QueryRow(`SELECT COUNT(*) FROM ` + t).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("copy: destination table %s is not empty (%d rows)", t, n)
		}
	}
	tx, err := sqlite.Begin(dst)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range copyTables {
		n, err := copyTable(src, tx, t)
		if err != nil {
			return fmt.Errorf("copy %s: %w", t, err)
		}
		if progress != nil {
			progress(t, n)
		}
	}
	return tx.Commit()
}

func copyTable(src *sql.DB, dst *sql.Tx, table string) (int64, error) {
	// modernc's SQLite driver returns an empty BLOB (x'') as nil; restore it
	// as empty bytes in NOT NULL columns. (Before the SELECT: a SQLite
	// pool has a single connection.)
	notNull, err := sqliteNotNull(src, table)
	if err != nil {
		return 0, err
	}
	rows, err := src.Query(`SELECT * FROM ` + table)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = "`" + c + "`"
	}
	head := `INSERT INTO ` + table + ` (` + strings.Join(quoted, ", ") + `) VALUES `
	one := `(` + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + `)`
	const batch = 200
	var (
		args  []any
		count int64
		inRow int
	)
	flush := func() error {
		if inRow == 0 {
			return nil
		}
		_, err := dst.Exec(head+strings.TrimSuffix(strings.Repeat(one+", ", inRow), ", "), args...)
		args, inRow = args[:0], 0
		return err
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return count, err
		}
		for i, v := range vals {
			if b, isBytes := v.([]byte); (v == nil || isBytes && b == nil) && notNull[cols[i]] {
				vals[i] = []byte{}
			}
		}
		args = append(args, vals...)
		inRow++
		count++
		if inRow == batch {
			if err := flush(); err != nil {
				return count, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, flush()
}

// sqliteNotNull returns the NOT NULL columns of a SQLite table (none for
// MySQL, whose driver keeps NULL and empty apart).
func sqliteNotNull(db *sql.DB, table string) (map[string]bool, error) {
	out := map[string]bool{}
	if sqlite.IsMySQL(db) {
		return out, nil
	}
	rows, err := db.Query(`SELECT name, "notnull" FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var nn int
		if err := rows.Scan(&name, &nn); err != nil {
			return nil, err
		}
		out[name] = nn != 0
	}
	return out, rows.Err()
}
