package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backup writes a consistent copy of the database into dir with VACUUM INTO,
// which is safe while promo-api is running (unlike copying the file next to
// its WAL), then deletes all but the newest keep backups.
func Backup(ctx context.Context, conn *sql.DB, dir string, keep int, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "promo-"+now.UTC().Format("20060102-150405")+".db")
	// VACUUM INTO takes an expression; quote the literal instead of relying on
	// parameter binding support in this statement.
	quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	if _, err := conn.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return "", fmt.Errorf("vacuum into %s: %w", path, err)
	}

	old, err := filepath.Glob(filepath.Join(dir, "promo-*.db"))
	if err != nil {
		return path, err
	}
	sort.Strings(old) // timestamped names sort chronologically
	for keep > 0 && len(old) > keep {
		if err := os.Remove(old[0]); err != nil {
			return path, err
		}
		old = old[1:]
	}
	return path, nil
}
