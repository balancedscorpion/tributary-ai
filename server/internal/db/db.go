package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type OpenOptions struct {
	MaxOpenConns int
	MaxIdleConns int
	ConnMaxIdle  time.Duration
	ConnMaxLife  time.Duration
}

func OpenFromConfig(cfg Config, opts *OpenOptions) (*sql.DB, error) {
	dsn, err := cfg.MySQL.DSN()
	if err != nil {
		return nil, err
	}
	return OpenDSN(dsn, opts)
}

func OpenDSN(dsn string, opts *OpenOptions) (*sql.DB, error) {
	clean := strings.TrimSpace(dsn)
	if clean == "" {
		return nil, fmt.Errorf("empty mysql DSN")
	}
	db, err := sql.Open("mysql", clean)
	if err != nil {
		return nil, err
	}
	// Reasonable small defaults for local usage.
	maxOpen := 10
	maxIdle := 10
	maxIdleTime := 5 * time.Minute
	maxLife := 30 * time.Minute
	if opts != nil {
		if opts.MaxOpenConns > 0 {
			maxOpen = opts.MaxOpenConns
		}
		if opts.MaxIdleConns > 0 {
			maxIdle = opts.MaxIdleConns
		}
		if opts.ConnMaxIdle > 0 {
			maxIdleTime = opts.ConnMaxIdle
		}
		if opts.ConnMaxLife > 0 {
			maxLife = opts.ConnMaxLife
		}
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxIdleTime(maxIdleTime)
	db.SetConnMaxLifetime(maxLife)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
