// Package gateway is the hosted front door: sign-in, one lab per reader, and
// a narrow proxy to that reader's Inspector.
//
// Security rules, enforced in ServeHTTP for every route:
//   - Host must be the public host (blocks DNS rebinding).
//   - A request with an Origin header must come from the public origin, and
//     every non-GET must carry one (with SameSite=Lax cookies, this is the
//     CSRF defence; there is no CSRF token).
//   - Every state-changing request needs X-Glasshouse-Action: 1, which a
//     cross-site form cannot send.
//   - No CORS headers at all: the site and the API share one origin.
//
// The proxy forwards only GET /snapshot, GET /events and POST
// /actions/<name> for a name in that lab's own action list, never the
// reader's cookies or any request body.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"glasshouse/gateway/internal/lab"
)

const ActionHeader = "X-Glasshouse-Action"

// Actions per reader per actionWindow.
const (
	actionLimit  = 20
	actionWindow = 10 * time.Second
)

type Config struct {
	// PublicURL is the one origin readers use, e.g. https://glasshouse.example.
	PublicURL  string
	SessionKey []byte
	Providers  []Provider
	Labs       *lab.Manager
	// Static, if set, serves the site export (for local runs without Caddy).
	Static http.Handler
	// Client calls OAuth providers; LabClient calls lab Inspectors.
	Client    *http.Client
	LabClient *http.Client
	Logger    *slog.Logger
}

type Server struct {
	publicURL string
	host      string
	secure    bool
	signer    *Signer
	providers map[string]Provider
	labs      *lab.Manager
	client    *http.Client
	labClient *http.Client
	log       *slog.Logger
	now       func() time.Time
	mux       *http.ServeMux

	mu      sync.Mutex
	actions map[string][]time.Time
}

func New(cfg Config) (*Server, error) {
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("public URL %q must be scheme://host[:port]", cfg.PublicURL)
	}
	signer, err := NewSigner(cfg.SessionKey)
	if err != nil {
		return nil, err
	}
	s := &Server{
		publicURL: u.Scheme + "://" + u.Host, host: u.Host, secure: u.Scheme == "https",
		signer: signer, providers: map[string]Provider{}, labs: cfg.Labs,
		client: cfg.Client, labClient: cfg.LabClient, log: cfg.Logger, now: time.Now,
		mux: http.NewServeMux(), actions: map[string][]time.Time{},
	}
	for _, p := range cfg.Providers {
		s.providers[p.Name] = p
	}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	s.mux.HandleFunc("GET /auth/{provider}/login", s.handleLogin)
	s.mux.HandleFunc("GET /auth/{provider}/callback", s.handleCallback)
	s.mux.HandleFunc("POST /auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/me", s.withSession(s.handleMe))
	s.mux.HandleFunc("GET /api/lab", s.withSession(s.handleLabStatus))
	s.mux.HandleFunc("POST /api/lab", s.withSession(s.handleLabStart))
	s.mux.HandleFunc("DELETE /api/lab", s.withSession(s.handleLabStop))
	s.mux.HandleFunc("GET /api/lab/snapshot", s.withSession(s.handleSnapshot))
	s.mux.HandleFunc("GET /api/lab/events", s.withSession(s.handleEvents))
	s.mux.HandleFunc("POST /api/lab/actions/{name}", s.withSession(s.handleAction))
	if cfg.Static != nil {
		s.mux.Handle("GET /", cfg.Static)
	}
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != s.host {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	origin := r.Header.Get("Origin")
	safe := r.Method == http.MethodGet || r.Method == http.MethodHead
	if (origin != "" && origin != s.publicURL) || (!safe && origin == "") {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if !safe && r.Header.Get(ActionHeader) != "1" {
		http.Error(w, "missing "+ActionHeader+" header", http.StatusForbidden)
		return
	}
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Content-Security-Policy", "frame-ancestors 'none'")
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
		h.Set("Cache-Control", "no-store")
	}
	s.mux.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) withSession(h func(http.ResponseWriter, *http.Request, Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.session(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in first"})
			return
		}
		h(w, r, sess)
	}
}

func (s *Server) handleMe(w http.ResponseWriter, _ *http.Request, sess Session) {
	writeJSON(w, http.StatusOK, map[string]string{"provider": sess.Provider, "name": sess.Name})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.setCookie(w, sessionCookie, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLabStatus(w http.ResponseWriter, _ *http.Request, sess Session) {
	writeJSON(w, http.StatusOK, s.labs.Status(sess.Key()))
}

func (s *Server) handleLabStart(w http.ResponseWriter, _ *http.Request, sess Session) {
	st, err := s.labs.Start(sess.Key())
	if errors.Is(err, lab.ErrTooManyStarts) || errors.Is(err, lab.ErrQueueFull) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not start a lab"})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleLabStop(w http.ResponseWriter, _ *http.Request, sess Session) {
	s.labs.Stop(sess.Key())
	w.WriteHeader(http.StatusNoContent)
}

// labRequest builds a request to the reader's Inspector. It copies nothing
// from the reader's request: no cookies, no Origin, no body.
func (s *Server) labRequest(ctx context.Context, method, addr, path string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://"+addr+path, nil)
	if err != nil {
		return nil, err
	}
	req.Host = lab.InspectorHost
	return req, nil
}

func noLab(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "no running lab"})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request, sess Session) {
	addr, _, ok := s.labs.Ready(sess.Key())
	if !ok {
		noLab(w)
		return
	}
	req, err := s.labRequest(r.Context(), http.MethodGet, addr, "/snapshot")
	if err != nil {
		noLab(w)
		return
	}
	s.relay(w, req)
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request, sess Session) {
	addr, actions, ok := s.labs.Ready(sess.Key())
	if !ok {
		noLab(w)
		return
	}
	name := r.PathValue("name")
	known := false
	for _, a := range actions {
		known = known || a.Name == name
	}
	if !known {
		http.NotFound(w, r)
		return
	}
	if !s.allowAction(sess.Key()) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "slow down"})
		return
	}
	s.labs.Touch(sess.Key())
	req, err := s.labRequest(r.Context(), http.MethodPost, addr, "/actions/"+url.PathEscape(name))
	if err != nil {
		noLab(w)
		return
	}
	req.Header.Set(ActionHeader, "1")
	s.relay(w, req)
}

// relay forwards one Inspector JSON response as-is.
func (s *Server) relay(w http.ResponseWriter, req *http.Request) {
	resp, err := s.labClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the lab did not answer"})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 8<<20))
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, sess Session) {
	addr, _, ok := s.labs.Ready(sess.Key())
	if !ok {
		noLab(w)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	req, err := s.labRequest(r.Context(), http.MethodGet, addr, "/events")
	if err != nil {
		noLab(w)
		return
	}
	if id := r.Header.Get("Last-Event-ID"); id != "" && isDigits(id) {
		req.Header.Set("Last-Event-ID", id)
	}
	resp, err := s.labClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the lab did not answer"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the lab did not answer"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flusher.Flush()
		}
		if err != nil {
			return
		}
	}
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0 && len(s) < 20
}

func (s *Server) allowAction(user string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	recent := s.actions[user][:0]
	for _, t := range s.actions[user] {
		if now.Sub(t) < actionWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= actionLimit {
		s.actions[user] = recent
		return false
	}
	s.actions[user] = append(recent, now)
	return true
}

// ValidateAddr allows only a loopback listener: Caddy, in the same network
// namespace, is the only client.
func ValidateAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("addr %q: %w", addr, err)
	}
	if host != "127.0.0.1" && host != "localhost" {
		return fmt.Errorf("addr %q: the gateway binds 127.0.0.1 or localhost only", addr)
	}
	return nil
}
