package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Open opens a MySQL connection using a JSON config file.
//
//	cfgDir:       base config directory (e.g. ~/.config/aianalyst)
//	dbConfigPath: optional override; absolute or relative to cfgDir (default: "db.json")
//
// It panics on configuration or connection errors.
func Open(cfgDir string, dbConfigPath string) *sql.DB {
	cfg, resolvedPath, err := LoadConfig(cfgDir, dbConfigPath)
	if err != nil {
		panic(fmt.Errorf("db config (%s): %w", resolvedPath, err))
	}

	dsn, err := cfg.MySQL.DSN()
	if err != nil {
		panic(fmt.Errorf("db dsn: %w", err))
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		panic(fmt.Errorf("db open: %w", err))
	}

	// Sane pool defaults for a local-only service.
	maxOpen := 10
	maxIdle := 10
	maxIdleTime := 5 * time.Minute
	maxLifeTime := 30 * time.Minute

	if cfg.MaxOpenConns != nil {
		maxOpen = *cfg.MaxOpenConns
	}
	if cfg.MaxIdleConns != nil {
		maxIdle = *cfg.MaxIdleConns
	}
	if cfg.ConnMaxIdleSeconds != nil {
		maxIdleTime = time.Duration(*cfg.ConnMaxIdleSeconds) * time.Second
	}
	if cfg.ConnMaxLifeSeconds != nil {
		maxLifeTime = time.Duration(*cfg.ConnMaxLifeSeconds) * time.Second
	}

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxIdleTime(maxIdleTime)
	db.SetConnMaxLifetime(maxLifeTime)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		panic(fmt.Errorf("db ping: %w", err))
	}

	return db
}
