// Package store owns the SQLite database: opening it, applying migrations,
// and the queries the rest of commonplace runs against it.
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a row to change does not exist.
var ErrNotFound = errors.New("not found")

// ErrInvalid matches every error about bad user input, so callers can
// show the message to the user. The errors themselves say only what is
// wrong.
var ErrInvalid = errors.New("invalid input")

// invalid is a message about bad user input; it matches ErrInvalid.
type invalid string

func (e invalid) Error() string        { return string(e) }
func (e invalid) Is(target error) bool { return target == ErrInvalid }

// Store wraps the commonplace database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies any
// pending migrations.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrateTo(-1); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the underlying handle. Keyword search is its one caller
// outside tests: its FTS5 query is built alongside the ranking it serves.
// Everything else queries through Store methods.
func (s *Store) DB() *sql.DB {
	return s.db
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %q: name must be NNNN_description.sql", e.Name())
		}
		v, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %q: bad version: %w", e.Name(), err)
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %q: expected version %d", m.name, i+1)
		}
	}
	return out, nil
}

// SchemaVersion reports the applied migration version (PRAGMA user_version).
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

// migrateTo applies migrations up to version target, or all of them when
// target is negative. Tests use a target to build an older schema.
func (s *Store) migrateTo(target int) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if target >= 0 && target < len(migrations) {
		migrations = migrations[:target]
	}
	current, err := s.SchemaVersion()
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if target < 0 && current > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", current, len(migrations))
	}
	if current >= len(migrations) {
		return nil
	}
	for _, m := range migrations[current:] {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		// PRAGMA does not accept bound parameters.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			tx.Rollback()
			return fmt.Errorf("set schema version %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.name, err)
		}
	}
	return nil
}

// Backup writes a consistent copy of the database to path, which must not
// exist. The copy is a single compacted file with no -wal or -shm
// companions, so it is safe to put in a synced folder.
func (s *Store) Backup(path string) error {
	if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("back up db: %w", err)
	}
	return nil
}
