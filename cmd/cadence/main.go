// Command cadence runs the Cadence Cards server (Go + HTMX rewrite of
// cadence-cards-svelte).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"cadence-cards/internal/claude"
	"cadence-cards/internal/config"
	"cadence-cards/internal/server"
	"cadence-cards/internal/store"
)

func main() {
	createUser := flag.String("create-user", "", "create a user with the given email (prompts for password) and exit")
	backupPath := flag.String("backup", "", `write a consistent snapshot of the database to this path ("-" for stdout) and exit`)
	flag.Parse()

	// In "-" mode the snapshot owns stdout, so the logs — including the "loaded
	// .env" line from config.Load below — have to go elsewhere or they corrupt
	// the stream.
	logOut := io.Writer(os.Stdout)
	if *backupPath == "-" {
		logOut = os.Stderr
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(logOut, nil)))

	cfg, err := config.Load()
	if err != nil {
		fatal("config", err)
	}

	// Handled before store.Open, which migrates: a backup must never do that
	// (see store.OpenForBackup).
	if *backupPath != "" {
		if err := runBackup(context.Background(), cfg.DBPath, *backupPath, os.Stdout); err != nil {
			fatal("backup", err)
		}
		return
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fatal("open store", err)
	}
	defer st.Close()

	if *createUser != "" {
		if err := runCreateUser(st, *createUser); err != nil {
			fatal("create user", err)
		}
		return
	}

	srv, handler, err := server.New(cfg, st, claude.New(cfg))
	if err != nil {
		fatal("server", err)
	}

	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Hourly maintenance: expired sessions + rate-limiter entries.
	cleanupCtx, stopCleanup := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-cleanupCtx.Done():
				return
			case now := <-ticker.C:
				if err := st.DeleteExpiredSessions(cleanupCtx, now); err != nil {
					slog.Error("session cleanup failed", "error", err)
				}
				srv.Cleanup(now)
			}
		}
	}()

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		slog.Info("shutting down")
		stopCleanup()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpServer.Shutdown(ctx)
	}()

	slog.Info("cadence-cards listening", "port", cfg.Port, "db", cfg.DBPath, "version", cfg.AppVersion,
		"claudeConfigured", cfg.ClaudeAPIKey != "")
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("listen", err)
	}
}

// runBackup writes a consistent snapshot of the database at dbPath to dest.
//
// dest "-" streams the snapshot to out: VACUUM INTO needs a seekable
// destination and refuses to overwrite an existing file, so it cannot write to
// a pipe and the snapshot goes to a temp file first.  That file lives in
// os.TempDir() — the container's writable layer, not the data volume — so a
// backup leaves nothing behind.  (os.TempDir honors TMPDIR, so if the service
// ever gains read_only: true the fix is a compose env var, not a code change.)
func runBackup(ctx context.Context, dbPath, dest string, out io.Writer) error {
	st, err := store.OpenForBackup(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	if dest != "-" {
		return backupToFile(ctx, st, dest)
	}

	dir, err := os.MkdirTemp("", "cadence-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	// A fresh directory rather than os.CreateTemp: VACUUM INTO refuses a file
	// that already exists.
	path := filepath.Join(dir, "cadence.db")
	if err := st.Backup(ctx, path); err != nil {
		return err
	}
	if err := store.CheckSnapshot(ctx, path); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// Checked, so that a reader that went away (EPIPE) exits non-zero instead of
	// silently handing back a truncated backup.
	n, err := io.Copy(out, f)
	if err != nil {
		return fmt.Errorf("stream snapshot: %w", err)
	}
	slog.Info("backup written", "dest", "stdout", "bytes", n)
	return nil
}

// backupToFile snapshots to dest via a .part file, so an interrupted backup
// never leaves a plausible-looking database at the destination path.
func backupToFile(ctx context.Context, st *store.Store, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	part := dest + ".part"
	os.Remove(part) // VACUUM INTO refuses an existing file; a leftover .part is not an error
	if err := st.Backup(ctx, part); err != nil {
		return err
	}
	if err := store.CheckSnapshot(ctx, part); err != nil {
		os.Remove(part)
		return err
	}
	if err := os.Rename(part, dest); err != nil {
		os.Remove(part)
		return err
	}

	info, err := os.Stat(dest)
	if err != nil {
		return err
	}
	slog.Info("backup written", "dest", dest, "bytes", info.Size())
	return nil
}

// runCreateUser interactively creates an account (replaces the Svelte app's
// scripts/create-user.ts; used when public registration is disabled).
func runCreateUser(st *store.Store, email string) error {
	fmt.Print("Name: ")
	var name string
	fmt.Scanln(&name)

	fmt.Print("Password (min 8 chars): ")
	password, err := readPassword()
	if err != nil {
		return err
	}
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}

	hash, err := bcrypt.GenerateFromPassword(password, 12)
	if err != nil {
		return err
	}
	user, err := st.CreateUser(context.Background(), name, email, string(hash))
	if err != nil {
		return err
	}
	fmt.Printf("Created user %d (%s)\n", user.ID, email)
	return nil
}

func readPassword() ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		defer fmt.Println()
		return term.ReadPassword(fd)
	}
	// Non-interactive fallback (piped input).
	var pw string
	if _, err := fmt.Scanln(&pw); err != nil {
		return nil, err
	}
	return []byte(pw), nil
}

func fatal(msg string, err error) {
	slog.Error(msg, "error", err)
	os.Exit(1)
}
