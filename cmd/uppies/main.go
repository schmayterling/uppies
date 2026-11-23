// Command uppies is a self-hosted uptime monitor with status pages.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*addr, *dbPath, *heartbeatURL, log); err != nil {
		log.Error("uppies stopped", "error", err)
		os.Exit(1)
	}
}

func run(addr, dbPath, heartbeatURL string, log *slog.Logger) (err error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, st.Close())
	}()

	checks := engine.New(st, log)
	site, err := web.New(st, log, checks.Reload)
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
