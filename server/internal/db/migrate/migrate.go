package migrate

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

type Migrator struct {
	FS embed.FS
	// Root within FS where *.sql migrations live.
	Dir string
}

func (m Migrator) Up(db *sql.DB) error {
	if db == nil {
		return errors.New("nil db")
	}
	dir := strings.TrimSpace(m.Dir)
	if dir == "" {
		dir = "migrations"
	}
	// Ensure migration table exists.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		id VARCHAR(255) NOT NULL PRIMARY KEY,
		applied_at DATETIME(3) NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
		return err
	}

	applied, err := loadApplied(db)
	if err != nil {
		return err
	}

	entries, err := fs.ReadDir(m.FS, dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(strings.ToLower(name), ".sql") {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	now := time.Now().UTC()
	for _, name := range names {
		id := name
		if applied[id] {
			continue
		}
		b, err := m.FS.ReadFile(dir + "/" + name)
		if err != nil {
			return err
		}
		sqlText := strings.TrimSpace(string(b))
		if sqlText == "" {
			// Still record empty migrations so ordering stays stable.
			if err := recordApplied(db, id, now); err != nil {
				return err
			}
			continue
		}
		stmts := splitSQL(sqlText)
		for _, st := range stmts {
			if strings.TrimSpace(st) == "" {
				continue
			}
			if _, err := db.Exec(st); err != nil {
				// MySQL DDL commits per statement. If the process stopped after
				// an additive statement but before schema_migrations was recorded,
				// replay reports an existing column/index. Migrations keep each
				// additive operation in its own statement, so these two errors mean
				// that exact step is already complete and later steps must continue.
				if (id == "0017_long_horizon_query_orchestration.sql" || id == "0018_query_profiling_durability.sql" ||
					id == "0019_query_obligations.sql") && isReplaySafeAdditiveDDLError(err) {
					continue
				}
				return fmt.Errorf("migration %s failed: %w", id, err)
			}
		}
		if err := recordApplied(db, id, now); err != nil {
			return err
		}
	}

	return nil
}

func isReplaySafeAdditiveDDLError(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	switch mysqlErr.Number {
	case 1060, // duplicate column name
		1061: // duplicate key name
		return true
	default:
		return false
	}
}

func loadApplied(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`SELECT id FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func recordApplied(db *sql.DB, id string, ts time.Time) error {
	_, err := db.Exec(`INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)`, id, ts)
	return err
}

// splitSQL splits on ';' for simple DDL migrations.
// This is intentionally conservative: migrations in this repo should avoid stored
// procedures or complex delimiter changes.
func splitSQL(in string) []string {
	parts := strings.Split(in, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		st := strings.TrimSpace(p)
		if st == "" {
			continue
		}
		out = append(out, st)
	}
	return out
}
