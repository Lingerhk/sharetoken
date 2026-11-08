package mysqldb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Migration is one numbered schema change. A component (the core tables, the
// mail tables) numbers its own migrations from 1, in order, and never edits one
// that has shipped.
//
// MinReader says which binaries can still work on a database with this
// migration applied. Leave it 0 for an additive change: older binaries keep
// running. A change they cannot survive sets it to its own Version, and they
// refuse to start.
type Migration struct {
	Version   int
	Name      string
	MinReader int
	SQL       string
}

const migrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	component  VARCHAR(64) NOT NULL,
	version    BIGINT NOT NULL,
	name       VARCHAR(191) NOT NULL,
	min_reader BIGINT NOT NULL DEFAULT 0,
	applied_at BIGINT NOT NULL,
	PRIMARY KEY (component, version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`

const writeLockTable = `CREATE TABLE IF NOT EXISTS st_write_lock (id INT PRIMARY KEY) ENGINE=InnoDB`

// ErrSchemaTooNew means the database needs a newer binary.
var ErrSchemaTooNew = errors.New("mysqldb: the database schema needs a newer version of this program")

// migrateLockWait bounds the wait for another process applying migrations.
const migrateLockWait = 2 * time.Minute

var sqlComment = regexp.MustCompile(`(?m)--.*$`)

// Latest returns the highest version in ms.
func Latest(ms []Migration) int {
	latest := 0
	for _, m := range ms {
		latest = max(latest, m.Version)
	}
	return latest
}

// CheckSchema refuses a database whose migrations this binary, which knows
// versions up to known, cannot work with. A database not migrated yet passes.
func CheckSchema(db *sql.DB, component string, known int) error {
	var need sql.NullInt64
	err := db.QueryRow(`SELECT MAX(min_reader) FROM schema_migrations WHERE component = ?`, component).Scan(&need)
	if errIs(err, 1146) { // no such table
		return nil
	}
	if err != nil {
		return err
	}
	if int(need.Int64) > known {
		return fmt.Errorf("%w (%s: needs %d, this build knows %d)", ErrSchemaTooNew, component, need.Int64, known)
	}
	return nil
}

func applied(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, component string) (map[int]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT version FROM schema_migrations WHERE component = ?`, component)
	if errIs(err, 1146) {
		return map[int]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	done := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		done[v] = true
	}
	return done, rows.Err()
}

// Migrate applies the migrations of component that the database lacks, and
// records them. Processes may start together: one applies, the others wait.
// DDL commits statement by statement, so a migration interrupted half-way runs
// again from its start: "already exists" errors of columns and indexes are
// ignored, and tables use IF NOT EXISTS.
func Migrate(db *sql.DB, component string, ms []Migration) error {
	if err := CheckSchema(db, component, Latest(ms)); err != nil {
		return err
	}
	ctx := context.Background()
	done, err := applied(ctx, db, component)
	if err != nil {
		return err
	}
	pending := false
	for _, m := range ms {
		pending = pending || !done[m.Version]
	}
	if !pending {
		return nil
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('sharetoken_migrate', ?)`, int(migrateLockWait/time.Second)).Scan(&got); err != nil {
		return err
	}
	if got.Int64 != 1 {
		return errors.New("mysqldb: another process has been applying migrations for too long")
	}
	defer conn.ExecContext(ctx, `SELECT RELEASE_LOCK('sharetoken_migrate')`) //nolint:errcheck // the connection ends with it

	for _, s := range []string{migrationsTable, writeLockTable, `INSERT IGNORE INTO st_write_lock (id) VALUES (1)`} {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("mysqldb: migrate: %w", err)
		}
	}
	if done, err = applied(ctx, conn, component); err != nil {
		return err
	}
	for _, m := range ms {
		if done[m.Version] {
			continue
		}
		for _, stmt := range strings.Split(sqlComment.ReplaceAllString(m.SQL, ""), ";") {
			if stmt = strings.TrimSpace(stmt); stmt == "" {
				continue
			}
			// 1060 duplicate column, 1061 duplicate index: this statement ran before an interruption.
			if _, err := conn.ExecContext(ctx, stmt); err != nil && !errIs(err, 1060) && !errIs(err, 1061) {
				return fmt.Errorf("mysqldb: migrate %s %d (%s): %w\n%s", component, m.Version, m.Name, err, stmt)
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations (component, version, name, min_reader, applied_at) VALUES (?, ?, ?, ?, ?)`,
			component, m.Version, m.Name, m.MinReader, time.Now().Unix()); err != nil {
			return fmt.Errorf("mysqldb: migrate %s %d: %w", component, m.Version, err)
		}
	}
	return nil
}
