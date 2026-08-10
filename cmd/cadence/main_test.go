package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"cadence-cards/internal/store"
)

// sqliteMagic is the 16-byte header every SQLite database file starts with.
const sqliteMagic = "SQLite format 3\x00"

// seedDB creates a database with one user in it and returns its path.
func seedDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.CreateUser(context.Background(), "Sean", "sean@example.com", "hash"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return path
}

// assertRestorable checks that path is a database holding the seeded user.
func assertRestorable(t *testing.T, path string) {
	t.Helper()
	st, err := store.OpenForBackup(path)
	if err != nil {
		t.Fatalf("OpenForBackup(%s): %v", path, err)
	}
	defer st.Close()
	if _, _, err := st.GetUserByEmail(context.Background(), "sean@example.com"); err != nil {
		t.Errorf("seeded user missing from snapshot: %v", err)
	}
}

func TestRunBackupToFile(t *testing.T) {
	src := seedDB(t)
	dest := filepath.Join(t.TempDir(), "snap.db")

	if err := runBackup(context.Background(), src, dest, io.Discard); err != nil {
		t.Fatalf("runBackup: %v", err)
	}
	assertRestorable(t, dest)
	if _, err := os.Stat(dest + ".part"); err == nil {
		t.Error("runBackup left a .part file behind")
	}

	// Refuses to overwrite, so a caller can never silently lose a good backup.
	if err := runBackup(context.Background(), src, dest, io.Discard); err == nil {
		t.Error("runBackup over an existing file: got nil, want error")
	}
}

func TestRunBackupToStdout(t *testing.T) {
	src := seedDB(t)

	var out bytes.Buffer
	if err := runBackup(context.Background(), src, "-", &out); err != nil {
		t.Fatalf("runBackup: %v", err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte(sqliteMagic)) {
		t.Fatalf("stream does not start with the SQLite magic: %q", out.Bytes()[:min(16, out.Len())])
	}

	// The stream has to be a usable database, not just plausible bytes.
	streamed := filepath.Join(t.TempDir(), "streamed.db")
	if err := os.WriteFile(streamed, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	assertRestorable(t, streamed)
}

func TestRunBackupMissingDatabase(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.db")
	if err := runBackup(context.Background(), missing, "-", io.Discard); err == nil {
		t.Fatal("runBackup on a missing database: got nil, want error")
	}
}
