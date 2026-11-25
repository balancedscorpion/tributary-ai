package db

import (
	"database/sql"
	"errors"

	dbmigrate "ai-analyst-local/internal/db/migrate"
)

// Migrate applies the embedded SQL migrations under server/internal/db/migrate/migrations.
func Migrate(db *sql.DB) error {
	if db == nil {
		return errors.New("nil db")
	}
	return (dbmigrate.Migrator{FS: dbmigrate.EmbeddedMigrations, Dir: "migrations"}).Up(db)
}
