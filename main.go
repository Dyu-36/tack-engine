package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	_ "github.com/charmbracelet/crush/internal/dns"
	"github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/server"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("tack-engine failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "server" {
		return errors.New("usage: tack-engine server [--host <address>] [--data-dir <path>] [--debug]")
	}

	flags := flag.NewFlagSet("server", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	host := flags.String("host", server.DefaultHost(), "server host (tcp, unix, or npipe)")
	flags.StringVar(host, "H", server.DefaultHost(), "server host (shorthand)")
	dataDir := flags.String("data-dir", "", "custom engine data directory")
	debug := flags.Bool("debug", false, "enable debug mode")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}

	globalDir := config.GlobalWorkspaceDir()
	cfg, err := config.Load(globalDir, *dataDir, *debug)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	// Configure rotating file logging before anything can log. Best-effort: a
	// server that cannot open its log file still serves.
	logPath := filepath.Join(globalDir, "logs", "tack-engine.log")
	if mkErr := os.MkdirAll(filepath.Dir(logPath), 0o755); mkErr == nil {
		log.Setup(logPath, *debug)
	}
	hostURL, err := server.ParseHostURL(*host)
	if err != nil {
		return fmt.Errorf("parse host: %w", err)
	}

	srv := server.NewServer(cfg, hostURL.Scheme, hostURL.Host)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	select {
	case <-sigCh:
	case err := <-errCh:
		if err != nil && !errors.Is(err, server.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, server.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
