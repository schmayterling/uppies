// Command uppies is a self-hosted uptime monitor with status pages.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"uppies/internal/alert"
	"uppies/internal/demo"
	"uppies/internal/engine"
	"uppies/internal/store"
	"uppies/internal/web"
)

const (
	shutdownGrace  = 10 * time.Second
	heartbeatEvery = time.Minute
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "reset-password" {
		os.Exit(resetPassword(os.Args[2:]))
	}
	addr := flag.String("addr", ":8080", "address to listen on")
	dbBackend := flag.String("db-backend", os.Getenv("UPPIES_DB_BACKEND"), "sqlite (default), postgres, mariadb, turso-sqlite, or tursopg")
	dbPath := flag.String("db", "uppies.db", "database file for sqlite and turso-sqlite")
	heartbeatURL := flag.String("heartbeat-url", "", "url to request every minute, so an outside service notices when uppies stops")
	smtpAddr := flag.String("smtp-addr", "", "host:port of an smtp server for email alerts, for example smtp.example.com:587")
	smtpFrom := flag.String("smtp-from", "", "sender address for email alerts")
	demoData := flag.Bool("demo", false, "fill an empty database with demo data that shows every part of the interface")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: uppies [flags]\n       uppies reset-password [flags] username\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mailer, err := newMailer(*smtpAddr, *smtpFrom)
	if err != nil {
		log.Error("uppies cannot start", "error", err)
		os.Exit(2)
	}
	dsn := os.Getenv("UPPIES_DATABASE_URL")
	if err := run(*addr, store.Backend(*dbBackend), *dbPath, dsn, *heartbeatURL, *demoData, mailer, log); err != nil {
		log.Error("uppies stopped", "error", err)
		os.Exit(1)
	}
}

// resetPassword runs "uppies reset-password", which sets a new random
// password for an account and prints it. It returns the exit code.
func resetPassword(args []string) int {
	flags := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	dbBackend := flags.String("db-backend", os.Getenv("UPPIES_DB_BACKEND"), "sqlite (default), postgres, mariadb, turso-sqlite, or tursopg")
	dbPath := flags.String("db", "uppies.db", "database file for sqlite and turso-sqlite")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "usage: uppies reset-password [flags] username\n\n"+
			"Sets a new random password for the account, signs it out on every device and prints the password.\n"+
			"Point it at the same database as the server, for example -db /data/uppies.db in the container.\n\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return 2
	}
	username := flags.Arg(0)
	backend := store.Backend(*dbBackend)
	// Opening a file that does not exist would create an empty database, and
	// the account would then seem not to exist.
	if backend == "" || backend == store.SQLite || backend == store.TursoSQLite {
		if _, err := os.Stat(*dbPath); err != nil {
			fmt.Fprintf(os.Stderr, "uppies: no database at %s. pass -db with the server's database file.\n", *dbPath)
			return 1
		}
	}
	ctx := context.Background()
	st, err := store.OpenBackend(ctx, backend, *dbPath, os.Getenv("UPPIES_DATABASE_URL"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "uppies: open the database: %v\n", err)
		return 1
	}
	password, err := web.ResetPassword(ctx, st, username)
	if err := errors.Join(err, st.Close()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "uppies: no account is named %q.\n", username)
		} else {
			fmt.Fprintf(os.Stderr, "uppies: reset the password: %v\n", err)
		}
		return 1
	}
	fmt.Printf("new password for %s: %s\n", username, password)
	fmt.Println("the account is signed out everywhere. sign in with this password, then change it under users.")
	return 0
}

// newMailer builds the email sender from the flags and the environment. The
// SMTP sign-in comes from UPPIES_SMTP_USERNAME and UPPIES_SMTP_PASSWORD, so
// that the password never appears in a process list. It returns nil when
// email is not configured.
func newMailer(addr, from string) (*alert.Mailer, error) {
	if addr == "" && from == "" {
		return nil, nil
	}
	if addr == "" || from == "" {
		return nil, errors.New("-smtp-addr and -smtp-from must be set together")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("-smtp-addr must be host:port: %w", err)
	}
	if err := alert.ValidateEmail(from); err != nil {
		return nil, fmt.Errorf("-smtp-from: %w", err)
	}
	return &alert.Mailer{
		Addr: addr, From: from,
		Username: os.Getenv("UPPIES_SMTP_USERNAME"), Password: os.Getenv("UPPIES_SMTP_PASSWORD"),
	}, nil
}

func run(addr string, backend store.Backend, dbPath, dsn, heartbeatURL string, demoData bool, mailer *alert.Mailer, log *slog.Logger) (err error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.OpenBackend(ctx, backend, dbPath, dsn)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, st.Close())
	}()

	if demoData {
		switch err := demo.Seed(ctx, st, time.Now()); {
		case errors.Is(err, demo.ErrNotEmpty):
			log.Warn(err.Error())
		case err != nil:
			return fmt.Errorf("add demo data: %w", err)
		default:
			log.Info("demo data added")
		}
	}

	checks := engine.New(st, log, mailer)
	site, err := web.New(st, log, checks.Reload, mailer)
	if err != nil {
		return err
	}
	handler, err := site.Handler()
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	engineDone := make(chan error, 1)
	serverDone := make(chan error, 1)
	go func() { engineDone <- checks.Run(ctx) }()
	go func() { serverDone <- server.ListenAndServe() }()
	if heartbeatURL != "" {
		go engine.Heartbeat(ctx, heartbeatURL, heartbeatEvery, log)
	}
	log.Info("uppies is running", "addr", addr, "database", st.Backend())

	select {
	case <-ctx.Done():
	case err = <-serverDone:
	}
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	// The engine is waited for so that no check is written while the database closes.
	return errors.Join(err, server.Shutdown(shutdownCtx), <-engineDone)
}
