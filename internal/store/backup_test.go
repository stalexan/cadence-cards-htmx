package store

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"cadence-cards/internal/sm2"
)

func TestBackupMatchesSource(t *testing.T) {
	s := newTestStore(t)
	userID, _, deckID := seed(t, s, true)
	for _, front := range []string{"hola", "adios", "gracias"} {
		mkCard(t, s, userID, deckID, front, sm2.PriorityA, "greeting")
	}

	snap := filepath.Join(t.TempDir(), "snap.db")
	if err := s.Backup(ctx, snap); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := CheckSnapshot(ctx, snap); err != nil {
		t.Fatalf("CheckSnapshot: %v", err)
	}
	// A VACUUM INTO snapshot is self-contained: no sidecars to copy alongside it.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(snap + suffix); err == nil {
			t.Errorf("snapshot left a %s sidecar", suffix)
		}
	}

	// OpenForBackup, not Open: reopening with Open would migrate the snapshot and
	// mask a backup that had been taken with migrations missing.
	restored, err := OpenForBackup(snap)
	if err != nil {
		t.Fatalf("OpenForBackup: %v", err)
	}
	defer restored.Close()

	want, wantTotal, err := s.ListCards(ctx, userID, CardListParams{DeckID: &deckID})
	if err != nil {
		t.Fatal(err)
	}
	got, gotTotal, err := restored.ListCards(ctx, userID, CardListParams{DeckID: &deckID})
	if err != nil {
		t.Fatalf("ListCards on snapshot: %v", err)
	}
	if gotTotal != wantTotal || len(got) != len(want) {
		t.Fatalf("snapshot has %d/%d cards, want %d/%d", len(got), gotTotal, len(want), wantTotal)
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Front != want[i].Front || got[i].Back != want[i].Back {
			t.Errorf("card %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Same migration state on both sides — which also proves OpenForBackup did
	// not apply any of its own.
	var srcMigrations, snapMigrations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&srcMigrations); err != nil {
		t.Fatal(err)
	}
	if err := restored.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&snapMigrations); err != nil {
		t.Fatal(err)
	}
	if srcMigrations == 0 || snapMigrations != srcMigrations {
		t.Errorf("schema_migrations: snapshot has %d rows, source has %d", snapMigrations, srcMigrations)
	}
}

func TestBackupRefusesExistingDestination(t *testing.T) {
	s := newTestStore(t)
	seed(t, s, false)

	dest := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(dest, []byte("do not clobber me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, dest); err == nil {
		t.Fatal("Backup over an existing file: got nil, want error")
	}
	body, err := os.ReadFile(dest)
	if err != nil || string(body) != "do not clobber me" {
		t.Errorf("destination was modified: %q, %v", body, err)
	}
}

// TestBackupWithConcurrentWriter covers the production shape: `docker compose
// exec` snapshots through a second process while the server keeps writing.  Two
// *Store values on one file is the closest in-process analogue — each has its
// own *sql.DB, so they coordinate through SQLite's file locks rather than
// through a shared pool.  Running both through a single store would prove
// nothing, since SetMaxOpenConns(1) would just serialize them.
func TestBackupWithConcurrentWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer writer.Close()
	userID, _, deckID := seed(t, writer, false)

	reader, err := OpenForBackup(path)
	if err != nil {
		t.Fatalf("OpenForBackup: %v", err)
	}
	defer reader.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	stop := make(chan struct{})
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := writer.CreateCard(ctx, userID, CardParams{
				DeckID: deckID, Front: "card", Back: "back", Priority: sm2.PriorityA,
			}); err != nil {
				t.Errorf("CreateCard during backup: %v", err)
				return
			}
		}
	}()

	snap := filepath.Join(t.TempDir(), "snap.db")
	backupErr := reader.Backup(ctx, snap)
	close(stop)
	wg.Wait()

	if backupErr != nil {
		t.Fatalf("Backup under a concurrent writer: %v", backupErr)
	}
	if err := CheckSnapshot(ctx, snap); err != nil {
		t.Fatalf("CheckSnapshot: %v", err)
	}
	// The snapshot is a point in time, so its card count is unpredictable — what
	// matters is that it opens and reads consistently.
	restored, err := OpenForBackup(snap)
	if err != nil {
		t.Fatalf("OpenForBackup(snapshot): %v", err)
	}
	defer restored.Close()
	if _, _, err := restored.ListCards(ctx, userID, CardListParams{DeckID: &deckID}); err != nil {
		t.Errorf("ListCards on snapshot: %v", err)
	}
}

func TestOpenForBackupMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.db")
	if _, err := OpenForBackup(path); err == nil {
		t.Fatal("OpenForBackup on a missing file: got nil, want error")
	}
	// Must not have helpfully created an empty database at the typo'd path.
	if _, err := os.Stat(path); err == nil {
		t.Error("OpenForBackup created a database at a nonexistent path")
	}
}
