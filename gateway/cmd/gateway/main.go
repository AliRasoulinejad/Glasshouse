// Command gateway serves sign-in and per-reader labs for the hosted site.
//
// Secrets come from the environment only, never argv:
//
//	GLASSHOUSE_PUBLIC_URL        https://glasshouse.example (the one origin)
//	GLASSHOUSE_SESSION_KEY       at least 32 random bytes, e.g. openssl rand -hex 32
//	GLASSHOUSE_GITHUB_CLIENT_ID / GLASSHOUSE_GITHUB_CLIENT_SECRET
//	GLASSHOUSE_GOOGLE_CLIENT_ID / GLASSHOUSE_GOOGLE_CLIENT_SECRET
//	DOCKER_HOST                  the lab daemon (dind), e.g. unix:///run/dind/docker.sock
//
// A provider is enabled when both its id and secret are set.
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

	"glasshouse/gateway/internal/gateway"
	"glasshouse/gateway/internal/lab"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("gateway stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address (loopback only)")
	labDir := flag.String("lab-dir", "/etc/glasshouse/lab", "directory with compose.yml and lab.override.yml")
	static := flag.String("static", "", "serve this site export directory too (local runs without a reverse proxy)")
	maxLabs := flag.Int("max-labs", 10, "labs starting or running at once")
	maxQueue := flag.Int("max-queue", 50, "readers waiting for a lab")
	idle := flag.Duration("idle", 15*time.Minute, "stop a lab this long after its last action")
	lifetime := flag.Duration("lifetime", 60*time.Minute, "stop a lab this long after it started, regardless of use")
	startsPerHour := flag.Int("starts-per-hour", 6, "lab starts allowed per reader per hour")
	flag.Parse()

	if err := gateway.ValidateAddr(*addr); err != nil {
		return err
	}
	var providers []gateway.Provider
	if id, sec := os.Getenv("GLASSHOUSE_GITHUB_CLIENT_ID"), os.Getenv("GLASSHOUSE_GITHUB_CLIENT_SECRET"); id != "" && sec != "" {
		providers = append(providers, gateway.GitHub(id, sec, "https://api.github.com"))
	}
	if id, sec := os.Getenv("GLASSHOUSE_GOOGLE_CLIENT_ID"), os.Getenv("GLASSHOUSE_GOOGLE_CLIENT_SECRET"); id != "" && sec != "" {
		providers = append(providers, gateway.Google(id, sec, "https://openidconnect.googleapis.com/v1/userinfo"))
	}
	if len(providers) == 0 {
		return errors.New("no sign-in provider configured (set GLASSHOUSE_GITHUB_CLIENT_ID/SECRET or GLASSHOUSE_GOOGLE_CLIENT_ID/SECRET)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := lab.ComposeRunner{Dir: *labDir, Env: lab.DockerEnv()}
	cleanCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	err := runner.StopAll(cleanCtx)
	cancel()
	if err != nil {
		return err
	}

	labClient := &http.Client{Transport: &http.Transport{Proxy: nil}}
	mgr := lab.NewManager(lab.Config{
		MaxLabs: *maxLabs, MaxQueue: *maxQueue, Idle: *idle, Lifetime: *lifetime,
		StartsPerHour: *startsPerHour, QueueStale: 2 * time.Minute, StartTimeout: 90 * time.Second,
	}, runner, lab.HTTPProbe(&http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}), log)
	go mgr.Run(ctx, 30*time.Second)

	var staticHandler http.Handler
	if *static != "" {
		staticHandler = http.FileServer(http.Dir(*static))
	}
	srv, err := gateway.New(gateway.Config{
		PublicURL: os.Getenv("GLASSHOUSE_PUBLIC_URL"), SessionKey: []byte(os.Getenv("GLASSHOUSE_SESSION_KEY")),
		Providers: providers, Labs: mgr, Static: staticHandler,
		Client: &http.Client{Timeout: 10 * time.Second}, LabClient: labClient, Logger: log,
	})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{Addr: *addr, Handler: srv, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdown)
	}()
	log.Info("gateway listening", "addr", *addr, "providers", len(providers))
	if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
