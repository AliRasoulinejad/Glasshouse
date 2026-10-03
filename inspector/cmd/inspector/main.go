// Command inspector serves a running target's internal state to the viewer.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"glasshouse/inspector/internal/adapter"
	"glasshouse/inspector/internal/adapter/mock"
	"glasshouse/inspector/internal/adapter/postgres"
	"glasshouse/inspector/internal/hub"
	"glasshouse/inspector/internal/server"
	"glasshouse/inspector/web"
)

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	addr := flag.String("addr", "127.0.0.1:8765", "listen address; must be loopback")
	adapterName := flag.String("adapter", "mock", "adapter to run: mock | postgres")
	containerMode := flag.Bool("container", false, "allow binding 0.0.0.0 inside a container; the host must publish on 127.0.0.1 only")
	var origins multiFlag
	flag.Var(&origins, "origin", "article origin allowed to call the API (repeatable), e.g. https://example.com")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*addr, *adapterName, origins, *containerMode, logger); err != nil {
		logger.Error("inspector stopped", "err", err)
		os.Exit(1)
	}
}

func run(addr, adapterName string, origins []string, containerMode bool, logger *slog.Logger) error {
	validateFn := server.ValidateAddr
	if containerMode {
		validateFn = server.ValidateContainerAddr
	}
	if err := validateFn(addr); err != nil {
		return err
	}

	a, target, err := buildAdapter(adapterName)
	if err != nil {
		return err
	}
	if closer, ok := a.(interface{ Close() }); ok {
		defer closer.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := a.Connect(connectCtx, target); err != nil {
		return fmt.Errorf("connect %s: %w", adapterName, err)
	}

	events, errs, err := a.StreamEvents(ctx)
	if err != nil {
		return fmt.Errorf("start event stream: %w", err)
	}

	h := hub.New(hub.DefaultHistory, logger)
	go h.Run(ctx, events, errs)

	webFS, err := fs.Sub(web.Static, "static")
	if err != nil {
		return err
	}

	srv, err := server.New(server.Config{
		Addr:           addr,
		ContainerMode:  containerMode,
		AllowedOrigins: origins,
		Target:         target,
		Adapter:        a,
		Hub:            h,
		Web:            webFS,
		Logger:         logger,
	})
	if err != nil {
		return err
	}
	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// buildAdapter returns the adapter for the given name. Adapter endpoints that
// hold credentials are read from the environment, never from command-line
// arguments, so they do not show up in process listings.
func buildAdapter(name string) (adapter.Adapter, adapter.Target, error) {
	switch name {
	case "mock":
		return mock.New(time.Second), adapter.Target{Name: "mock"}, nil
	case "postgres":
		dsn := os.Getenv("GLASSHOUSE_PG_DSN")
		if dsn == "" {
			return nil, adapter.Target{}, errors.New("GLASSHOUSE_PG_DSN is not set")
		}
		return postgres.New(500 * time.Millisecond), adapter.Target{Name: "postgres", Endpoint: dsn}, nil
	default:
		return nil, adapter.Target{}, fmt.Errorf("unknown adapter %q", name)
	}
}
