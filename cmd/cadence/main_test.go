package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

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

// The name prompt must take the whole line and the piped password must be the
// next line — fmt.Scanln used to split "Sean Alexandre" at the space and feed
// the surname's tail to the password prompt.
func TestRunCreateUserMultiWordName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("Sean Alexandre\nsuper secret password\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	if err := runCreateUser(st, "sean@example.com"); err != nil {
		t.Fatalf("runCreateUser: %v", err)
	}
	user, hash, err := st.GetUserByEmail(context.Background(), "sean@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if user.Name == nil || *user.Name != "Sean Alexandre" {
		t.Errorf("name = %v, want %q", user.Name, "Sean Alexandre")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("super secret password")) != nil {
		t.Error("stored hash does not match the piped password")
	}
}

func TestRunListUsers(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	var empty bytes.Buffer
	if err := runListUsers(ctx, st, &empty); err != nil {
		t.Fatalf("runListUsers (empty): %v", err)
	}
	if got := empty.String(); got != "No users.\n" {
		t.Errorf("empty database printed %q, want %q", got, "No users.\n")
	}

	if _, err := st.CreateUser(ctx, "Sean Alexandre", "sean@example.com", "hash-one"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := st.CreateUser(ctx, "", "second@example.com", "hash-two"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	var out bytes.Buffer
	if err := runListUsers(ctx, st, &out); err != nil {
		t.Fatalf("runListUsers: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want a header plus 2 users:\n%s", len(lines), out.String())
	}
	for _, want := range []string{"sean@example.com", "Sean Alexandre", "second@example.com"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output is missing %q:\n%s", want, out.String())
		}
	}
	// A listing must never leak password hashes.
	if strings.Contains(out.String(), "hash-one") {
		t.Errorf("output contains the password hash:\n%s", out.String())
	}
}

// pipeStdin replaces os.Stdin with a pipe holding text for the duration of the
// test.
func pipeStdin(t *testing.T, text string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(text); err != nil {
		t.Fatal(err)
	}
	w.Close()
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })
}

func TestRunResetPassword(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	old, err := bcrypt.GenerateFromPassword([]byte("old password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, "Sean", "sean@example.com", string(old))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	token, err := st.CreateSession(ctx, user.ID, time.Now())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	pipeStdin(t, "brand new password\n")
	if err := runResetPassword(st, "sean@example.com"); err != nil {
		t.Fatalf("runResetPassword: %v", err)
	}

	_, hash, err := st.GetUserByEmail(ctx, "sean@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("brand new password")) != nil {
		t.Error("stored hash does not match the piped password")
	}
	// The reset signs every existing session out.
	if _, err := st.GetSessionUser(ctx, token, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("session after reset: err = %v, want ErrNotFound", err)
	}
}

func TestRunResetPasswordUnknownEmail(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	pipeStdin(t, "brand new password\n")
	if err := runResetPassword(st, "nobody@example.com"); err == nil {
		t.Fatal("runResetPassword on an unknown email: got nil, want error")
	}
}

func TestRunResetPasswordTooShort(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.CreateUser(ctx, "Sean", "sean@example.com", "old-hash"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	pipeStdin(t, "short\n")
	if err := runResetPassword(st, "sean@example.com"); err == nil {
		t.Fatal("runResetPassword with a 5-char password: got nil, want error")
	}
	if _, hash, err := st.GetUserByEmail(ctx, "sean@example.com"); err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	} else if hash != "old-hash" {
		t.Errorf("hash = %q, want the original to be untouched", hash)
	}
}
