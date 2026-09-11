package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// TimeFormat is how every timestamp is stored: RFC 3339 UTC, whole seconds,
// so lexical order in SQL equals chronological order.
const TimeFormat = "2006-01-02T15:04:05Z"

// FormatTime renders t in the storage format.
func FormatTime(t time.Time) string { return t.UTC().Truncate(time.Second).Format(TimeFormat) }

// ParseTime reads a stored timestamp.
func ParseTime(s string) (time.Time, error) { return time.Parse(TimeFormat, s) }

// Open opens the SQLite database at path (":memory:" works for tests) and
// applies pending migrations.
func Open(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path != ":memory:" {
		dsn += "&_pragma=journal_mode(WAL)"
	}
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite has a single writer anyway, and an in-memory
	// database only exists on the connection that created it.
	conn.SetMaxOpenConns(1)

	if err := migrateUp(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return conn, nil
}

func migrateUp(conn *sql.DB) error {
	src, err := iofs.New(migrations, "migrations")
	if err != nil {
		return err
	}
	drv, err := sqlite.WithInstance(conn, &sqlite.Config{})
	if err != nil {
		return err
	}
	// Not closing m on purpose: its Close closes conn, which the caller owns.
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", drv)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}
