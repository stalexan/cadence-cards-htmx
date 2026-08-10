package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// OpenForBackup opens an existing database without applying migrations.
//
// A backup can be taken with a *newer* binary than the running server —
// `docker compose exec` runs the image's binary, not the container's process,
// so between `docker compose build` and `up -d` the two differ — and applying
// migrations to the live file underneath an older server would corrupt it.
// A missing file is an error rather than a silently created empty database.
func OpenForBackup(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("database %s: %w", path, err)
	}
	return open(path, modeBackup)
}

// Backup writes a consistent point-in-time snapshot of the database to dest
// using VACUUM INTO. dest must not already exist.
//
// Safe to run against a live server: the snapshot is taken inside a read
// transaction, so under WAL concurrent writers are neither blocked nor
// partially included, and committed transactions still sitting in the
// write-ahead log are captured — which a plain copy of the .db file would
// lose. The result is a single compacted file with no -wal/-shm sidecars, so
// restoring it is one move.
//
// Deliberately not PRAGMA wal_checkpoint first: that takes the checkpointer
// lock and mutates the production database for no benefit here. The only
// interaction is that the server's automatic checkpoint cannot reset the WAL
// while this read transaction is open, so the WAL may grow for the duration.
func (s *Store) Backup(ctx context.Context, dest string) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		return fmt.Errorf("vacuum into %s: %w", dest, err)
	}
	return nil
}

// CheckSnapshot opens path read-only and runs PRAGMA quick_check, so a corrupt
// backup fails when it is taken rather than on the day it is needed.
//
// quick_check rather than integrity_check: VACUUM INTO already walks every
// b-tree in the source to build the copy, so the extra index-versus-table
// cross-check buys little against a freshly rebuilt file. What is being
// asserted is that the shipped file is a structurally valid, openable SQLite
// database.
func CheckSnapshot(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", dsn(path, modeVerify))
	if err != nil {
		return fmt.Errorf("open snapshot %s: %w", path, err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return fmt.Errorf("quick_check %s: %w", path, err)
	}
	defer rows.Close()

	var problems []string
	n := 0
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("quick_check %s: %w", path, err)
		}
		n++
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("quick_check %s: %w", path, err)
	}
	if n == 0 {
		return fmt.Errorf("quick_check %s: no result", path)
	}
	if len(problems) > 0 {
		return fmt.Errorf("snapshot %s failed quick_check: %s", path, strings.Join(problems, "; "))
	}
	return nil
}
