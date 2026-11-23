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
	addr := flag.String("addr", ":8080", "address to listen on")
	dbPath := flag.String("db", "uppies.db", "path of the database file")
	heartbeatURL := flag.String("heartbeat-url", "", "url to request every minute, so an outside service notices when uppies stops")
	smtpAddr := flag.String("smtp-addr", "", "host:port of an smtp server for email alerts, for example smtp.example.com:587")
	smtpFrom := flag.String("smtp-from", "", "sender address for email alerts")
	demoData := flag.Bool("demo", false, "fill an empty database with demo data that shows every part of the interface")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	mailer, err := newMailer(*smtpAddr, *smtpFrom)
	if err != nil {
		log.Error("uppies cannot start", "error", err)
		os.Exit(2)
	}
	if err := run(*addr, *dbPath, *heartbeatURL, *demoData, mailer, log); err != nil {
		log.Error("uppies stopped", "error", err)
		os.Exit(1)
	}
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

func run(addr, dbPath, heartbeatURL string, demoData bool, mailer *alert.Mailer, log *slog.Logger) (err error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, dbPath)
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
	log.Info("uppies is running", "addr", addr, "db", dbPath)

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
