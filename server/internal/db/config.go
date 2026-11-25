package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

const DefaultConfigFile = "db.json"

// Config is loaded from a JSON file (default: cfgDir/db.json).
//
// Example:
//
//	{
//	  "mysql": {
//	    "user": "root",
//	    "password": "password",
//	    "net": "tcp",
//	    "addr": "127.0.0.1:3306",
//	    "database": "ai_analyst_local",
//	    "params": {
//	      "tls": "false"
//	    }
//	  },
//	  "maxOpenConns": 10,
//	  "maxIdleConns": 10,
//	  "connMaxIdleSeconds": 300,
//	  "connMaxLifeSeconds": 1800
//	}
type Config struct {
	MySQL MySQLConfig `json:"mysql"`

	// Optional connection pool tuning. If nil, defaults are used.
	MaxOpenConns *int `json:"maxOpenConns,omitempty"`
	MaxIdleConns *int `json:"maxIdleConns,omitempty"`

	ConnMaxIdleSeconds *int `json:"connMaxIdleSeconds,omitempty"`
	ConnMaxLifeSeconds *int `json:"connMaxLifeSeconds,omitempty"`
}

type MySQLConfig struct {
	// DSN, when set, is used verbatim and overrides the other MySQL fields.
	DSNText string `json:"dsn,omitempty"`

	User     string            `json:"user,omitempty"`
	Password string            `json:"password,omitempty"`
	Net      string            `json:"net,omitempty"`  // tcp|unix (default tcp)
	Addr     string            `json:"addr,omitempty"` // host:port (tcp) or socket path (unix)
	Database string            `json:"database,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
}

func resolvePath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}

	// Support "~" and "~/" for user-entered paths.
	// We intentionally do not support "~user" expansion.
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			if p == "~" {
				p = home
			} else {
				p = filepath.Join(home, p[2:])
			}
		}
	}

	// filepath.Clean("") returns ".", so only clean non-empty strings.
	p = filepath.Clean(p)
	return p
}

func configPath(cfgDir string, dbConfigPath string) (string, error) {
	p := resolvePath(dbConfigPath)
	if filepath.IsAbs(p) {
		return p, nil
	}
	dir := strings.TrimSpace(cfgDir)
	if dir == "" {
		return "", errors.New("empty cfgDir")
	}
	if p == "" {
		return filepath.Join(dir, DefaultConfigFile), nil
	}
	return filepath.Join(dir, p), nil
}

func decodeJSONStrict(r io.Reader, dst any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	// Prevent trailing junk.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected additional JSON content")
		}
		return err
	}
	return nil
}

func LoadConfig(cfgDir string, dbConfigPath string) (Config, string, error) {
	p, err := configPath(cfgDir, dbConfigPath)
	if err != nil {
		return Config{}, "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Config{}, p, err
	}

	var cfg Config
	if err := decodeJSONStrict(bytes.NewReader(b), &cfg); err != nil {
		return Config{}, p, fmt.Errorf("invalid JSON: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return Config{}, p, err
	}
	return cfg, p, nil
}

func (c Config) validate() error {
	// Pool validation (best-effort).
	if c.MaxOpenConns != nil && *c.MaxOpenConns < 0 {
		return errors.New("maxOpenConns must be >= 0")
	}
	if c.MaxIdleConns != nil && *c.MaxIdleConns < 0 {
		return errors.New("maxIdleConns must be >= 0")
	}
	if c.ConnMaxIdleSeconds != nil && *c.ConnMaxIdleSeconds < 0 {
		return errors.New("connMaxIdleSeconds must be >= 0")
	}
	if c.ConnMaxLifeSeconds != nil && *c.ConnMaxLifeSeconds < 0 {
		return errors.New("connMaxLifeSeconds must be >= 0")
	}

	if _, err := c.MySQL.DSN(); err != nil {
		return err
	}
	return nil
}

func (c MySQLConfig) DSN() (string, error) {
	// Allow users to specify a full DSN for maximum flexibility.
	if v := strings.TrimSpace(c.DSNText); v != "" {
		return v, nil
	}

	user := strings.TrimSpace(c.User)
	if user == "" {
		return "", errors.New("mysql.user is required")
	}
	dbName := strings.TrimSpace(c.Database)
	if dbName == "" {
		return "", errors.New("mysql.database is required")
	}

	netw := strings.TrimSpace(c.Net)
	if netw == "" {
		netw = "tcp"
	}
	if netw != "tcp" && netw != "unix" {
		return "", errors.New("mysql.net must be \"tcp\" or \"unix\"")
	}

	addr := strings.TrimSpace(c.Addr)
	if addr == "" {
		if netw == "tcp" {
			addr = "127.0.0.1:3306"
		} else {
			return "", errors.New("mysql.addr is required for unix sockets")
		}
	}

	mc := mysql.NewConfig()
	mc.User = user
	mc.Passwd = c.Password
	mc.Net = netw
	mc.Addr = addr
	mc.DBName = dbName

	// Common defaults for application code.
	mc.ParseTime = true
	mc.Loc = time.UTC

	if len(c.Params) > 0 {
		mc.Params = make(map[string]string, len(c.Params))
		for k, v := range c.Params {
			kk := strings.TrimSpace(k)
			if kk == "" {
				continue
			}
			mc.Params[kk] = strings.TrimSpace(v)
		}
	}

	return mc.FormatDSN(), nil
}
