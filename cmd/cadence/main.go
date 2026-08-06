// Command cadence runs the Cadence Cards server (Go + HTMX rewrite of
// cadence-cards-svelte).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		fatal("config", err)
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
