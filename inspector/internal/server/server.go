// Package server exposes the Inspector HTTP/SSE API and serves the viewer.
//
// Security rules, all enforced here rather than per handler:
//   - Host header must name this loopback listener (blocks DNS rebinding).
//   - CORS is granted only to configured origins; /health alone is open to any.
//   - Requests carrying an Origin header must come from a configured origin.
//   - Actions are looked up by name in a fixed map, and need a custom header
//     that a cross-site form post cannot set.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"glasshouse/inspector/internal/adapter"
	"glasshouse/inspector/internal/hub"
)

// ActionHeader must be sent by the browser on every POST /actions/*. A
// cross-site HTML form cannot set it, and a cross-site fetch triggers a CORS
// preflight that the origin check rejects.
const ActionHeader = "X-Glasshouse-Action"

// heartbeatInterval keeps SSE connections alive through proxies and lets the
// server notice a gone client.
const heartbeatInterval = 15 * time.Second

// Config is everything the server needs. Adapter and Hub must be non-nil.
type Config struct {
	// Addr must be a loopback address such as 127.0.0.1:8765. In container
	// mode it may be 0.0.0.0:<port>, because the container's own loopback is
	// unreachable through Docker's port publishing.
	Addr string
	// ContainerMode permits binding 0.0.0.0 inside a container. The host must
	// still publish the port on 127.0.0.1 only.
	ContainerMode bool
	// AllowedOrigins lists the article origins allowed to call the API,
	// e.g. "https://example.com". Empty means no cross-origin access.
	AllowedOrigins []string
	Target         adapter.Target
	Adapter        adapter.Adapter
	Hub            *hub.Hub
	// Web is the viewer shell, served at "/".
	Web    fs.FS
	Logger *slog.Logger
}

// Server is the configured HTTP handler.
type Server struct {
	cfg     Config
	origins map[string]struct{}
	// selfOrigins are the viewer's own origins. Same-origin POSTs carry an
	// Origin header, so they must pass the check. They get no CORS headers.
	selfOrigins map[string]struct{}
	hosts       map[string]struct{}
	actions     map[string]adapter.Action
	mux         *http.ServeMux
}

// ValidateAddr returns an error unless addr is a loopback host:port. It is
// strict on purpose: there is no override flag.
func ValidateAddr(addr string) error {
	return validate(addr, false)
}

// ValidateContainerAddr is ValidateAddr plus the one extra case container mode
// needs: 0.0.0.0 on a numeric port. Nothing else is widened.
func ValidateContainerAddr(addr string) error {
	return validate(addr, true)
}

func validate(addr string, container bool) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("addr %q: %w", addr, err)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("addr %q: port must be numeric", addr)
	}
	if host == "127.0.0.1" || host == "localhost" {
		return nil
	}
	if container && host == "0.0.0.0" {
		return nil
	}
	if container {
		return fmt.Errorf("addr %q: container mode may bind only 0.0.0.0, 127.0.0.1 or localhost", addr)
	}
	return fmt.Errorf("addr %q: only 127.0.0.1 or localhost may be bound (use -container inside a container)", addr)
}

// normalizeOrigin validates one allowed-origin value and returns its
// canonical form "scheme://host[:port]", with no wildcard, path, query,
// fragment, or userinfo. The result is used verbatim both as the CORS
// Access-Control-Allow-Origin value and as a frame-ancestors source in the
// CSP header, so anything wider than one exact origin here widens both.
func normalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("allowed origin %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("allowed origin %q must start with http:// or https://", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("allowed origin %q has no host", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("allowed origin %q must not carry user info", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("allowed origin %q must not carry a path; use %s://%s", raw, scheme, u.Host)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("allowed origin %q must not carry a query or fragment", raw)
	}
	if strings.ContainsAny(u.Host, "*; ") {
		return "", fmt.Errorf("allowed origin %q must be a single host, not a wildcard or pattern", raw)
	}
	return scheme + "://" + strings.ToLower(u.Host), nil
}

// New validates the config and builds the handler.
func New(cfg Config) (*Server, error) {
	validateFn := ValidateAddr
	if cfg.ContainerMode {
		validateFn = ValidateContainerAddr
	}
	if err := validateFn(cfg.Addr); err != nil {
		return nil, err
	}
	_, port, _ := net.SplitHostPort(cfg.Addr)

	s := &Server{
		cfg:     cfg,
		origins: make(map[string]struct{}),
		selfOrigins: map[string]struct{}{
			"http://127.0.0.1:" + port: {},
			"http://localhost:" + port: {},
		},
		hosts: map[string]struct{}{
			"127.0.0.1:" + port: {},
			"localhost:" + port: {},
		},
		actions: make(map[string]adapter.Action),
		mux:     http.NewServeMux(),
	}
	for _, o := range cfg.AllowedOrigins {
		normalized, err := normalizeOrigin(o)
		if err != nil {
			return nil, err
		}
		s.origins[normalized] = struct{}{}
	}
	if a, ok := cfg.Adapter.(adapter.Actioner); ok {
		for name, act := range a.Actions() {
			s.actions[name] = act
		}
	}

	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /snapshot", s.handleSnapshot)
	s.mux.HandleFunc("GET /events", s.handleEvents)
	s.mux.HandleFunc("POST /actions/{name}", s.handleAction)
	s.mux.HandleFunc("OPTIONS /actions/{name}", s.handlePreflight)
	if cfg.Web != nil {
		s.mux.Handle("GET /", http.FileServerFS(cfg.Web))
	}
	return s, nil
}

// ServeHTTP enforces the host check on every request, then routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.hosts[r.Host]; !ok {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	s.setFraming(w)
	s.mux.ServeHTTP(w, r)
}

// setFraming lets only this viewer and the configured article origins frame
// it. Everything else, including clickjacking pages, gets no frame permission.
func (s *Server) setFraming(w http.ResponseWriter) {
	sources := []string{"'self'"}
	for o := range s.origins {
		sources = append(sources, o)
	}
	sort.Strings(sources[1:])
	w.Header().Set("Content-Security-Policy", "frame-ancestors "+strings.Join(sources, " "))
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	s.cfg.Logger.Info("inspector listening", "addr", s.cfg.Addr, "target", s.cfg.Target.Name)
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// originAllowed reports whether a request may proceed for its Origin header.
// A request with no Origin header is same-origin or non-browser and passes.
func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if _, ok := s.selfOrigins[origin]; ok {
		return true
	}
	_, ok := s.origins[origin]
	return ok
}

// setCORS grants cross-origin read access to one configured origin only.
func (s *Server) setCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	if _, ok := s.origins[origin]; ok {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	// /health is the one endpoint open to any origin, so the article can
	// detect a running Inspector. It exposes no target state.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"schema_version": adapter.SchemaVersion,
		"target":         s.cfg.Target.Name,
		"actions":        s.ActionNames(),
	})
}

// snapshotEnvelope is the shape of GET /snapshot: the current snapshot plus
// the recent event history, so a viewer can render its timeline on load.
type snapshotEnvelope struct {
	SchemaVersion int              `json:"schema_version"`
	Snapshot      adapter.Snapshot `json:"snapshot"`
	Events        []adapter.Event  `json:"events"`
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	s.setCORS(w, r)
	snap, err := s.cfg.Adapter.Snapshot(r.Context())
	if err != nil {
		s.cfg.Logger.Error("snapshot failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "snapshot unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, snapshotEnvelope{
		SchemaVersion: adapter.SchemaVersion,
		Snapshot:      snap,
		Events:        s.cfg.Hub.History(),
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	s.setCORS(w, r)

	// Resume after the last event the client saw, if it says so.
	afterSeq, hasAfter := parseAfter(r)
	replay, live, unsubscribe := s.cfg.Hub.Subscribe(afterSeq, hasAfter)
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	write := func(ev adapter.Event) bool {
		body, err := json.Marshal(ev)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(w, "id: %d\nevent: event\ndata: %s\n\n", ev.Seq, body)
		flusher.Flush()
		return err == nil
	}
	for _, ev := range replay {
		if !write(ev) {
			return
		}
	}

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-live:
			if !ok {
				// Hub dropped this subscriber for falling behind. The viewer
				// reconnects with Last-Event-ID and catches up from history.
				return
			}
			if !write(ev) {
				return
			}
		}
	}
}

func parseAfter(r *http.Request) (uint64, bool) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	if raw == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	s.setCORS(w, r)
	w.Header().Set("Access-Control-Allow-Methods", "POST")
	w.Header().Set("Access-Control-Allow-Headers", ActionHeader+", Content-Type")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if r.Header.Get(ActionHeader) != "1" {
		http.Error(w, "missing "+ActionHeader+" header", http.StatusForbidden)
		return
	}
	s.setCORS(w, r)

	name := r.PathValue("name")
	act, ok := s.actions[name]
	if !ok {
		// Unknown names get the same answer as "no such action" in the map;
		// the browser never gets to name a command, only pick from the list.
		http.NotFound(w, r)
		return
	}
	// The request body is deliberately never read: actions take no input.
	result, err := act.Run(r.Context())
	if err != nil {
		s.cfg.Logger.Error("action failed", "action", name, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"action": name, "error": "action failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": name, "result": result})
}

// ActionNames lists the registered actions in sorted order.
func (s *Server) ActionNames() []string {
	names := make([]string, 0, len(s.actions))
	for n := range s.actions {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
