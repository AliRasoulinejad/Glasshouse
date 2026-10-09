package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"glasshouse/inspector/internal/adapter"
	"glasshouse/inspector/internal/adapter/mock"
	"glasshouse/inspector/internal/hub"
)

const testAddr = "127.0.0.1:8765"
const testOrigin = "https://article.example"

type stubActioner struct {
	*mock.Adapter
	ran *int
}

// flushRecorder is a ResponseRecorder that is safe to read while a handler
// is still writing, which the SSE test needs.
type flushRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{rec: httptest.NewRecorder()}
}

func (f *flushRecorder) Header() http.Header  { return f.rec.Header() }
func (f *flushRecorder) WriteHeader(code int) { f.rec.WriteHeader(code) }
func (f *flushRecorder) Flush()               {}
func (f *flushRecorder) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rec.Write(b)
}
func (f *flushRecorder) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rec.Body.String()
}

func (s *stubActioner) Actions() map[string]adapter.Action {
	return map[string]adapter.Action{
		"ping": {Description: "test", Run: func(context.Context) (any, error) {
			*s.ran++
			return "pong", nil
		}},
	}
}

func newTestServer(t *testing.T, ran *int) *Server {
	t.Helper()
	a := &stubActioner{Adapter: mock.New(time.Hour), ran: ran}
	if err := a.Connect(context.Background(), adapter.Target{Name: "test"}); err != nil {
		t.Fatal(err)
	}
	h := hub.New(hub.DefaultHistory, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s, err := New(Config{
		Addr:           testAddr,
		AllowedOrigins: []string{testOrigin},
		Target:         adapter.Target{Name: "test"},
		Adapter:        a,
		Hub:            h,
		Web:            fstest.MapFS{"index.html": {Data: []byte("<html></html>")}},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func do(t *testing.T, s *Server, method, path string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Host = testAddr
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestValidateAddrRejectsNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8765", ":8765", "192.168.1.10:8765", "[::]:8765"} {
		if err := ValidateAddr(addr); err == nil {
			t.Errorf("ValidateAddr(%q) = nil, want error", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8765", "localhost:8765"} {
		if err := ValidateAddr(addr); err != nil {
			t.Errorf("ValidateAddr(%q) = %v, want nil", addr, err)
		}
	}
}

func TestContainerModeAllowsOnlyAnyInterfaceExtra(t *testing.T) {
	if err := ValidateContainerAddr("0.0.0.0:8765"); err != nil {
		t.Fatalf("container 0.0.0.0 rejected: %v", err)
	}
	for _, addr := range []string{"192.168.1.10:8765", "10.0.0.1:8765", "[::]:8765"} {
		if err := ValidateContainerAddr(addr); err == nil {
			t.Errorf("container mode accepted %q", addr)
		}
	}
	if err := ValidateAddr("0.0.0.0:8765"); err == nil {
		t.Fatal("host mode accepted 0.0.0.0")
	}
}

func TestNewRejectsNonLoopbackConfig(t *testing.T) {
	_, err := New(Config{Addr: "0.0.0.0:8765", Adapter: mock.New(time.Hour), Hub: hub.New(10, slog.Default()), Logger: slog.Default()})
	if err == nil {
		t.Fatal("New accepted 0.0.0.0")
	}
}

func TestHostHeaderMustBeLoopbackListener(t *testing.T) {
	s := newTestServer(t, new(int))
	rec := do(t, s, "GET", "/health", func(r *http.Request) { r.Host = "evil.example:8765" })
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign Host: got %d, want 403", rec.Code)
	}
	rec = do(t, s, "GET", "/health", func(r *http.Request) { r.Host = "127.0.0.1:9999" })
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong port Host: got %d, want 403", rec.Code)
	}
}

func TestHealthActionsIncludeDescriptionAndQuery(t *testing.T) {
	s := newTestServer(t, new(int))
	rec := do(t, s, "GET", "/health", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/health: got %d", rec.Code)
	}
	var body struct {
		Actions []ActionInfo `json:"actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /health: %v", err)
	}
	if len(body.Actions) != 1 || body.Actions[0].Name != "ping" || body.Actions[0].Description != "test" {
		t.Fatalf("got actions %+v, want one ping action with its description", body.Actions)
	}
}

func TestHealthIsOpenToAnyOriginOthersAreNot(t *testing.T) {
	s := newTestServer(t, new(int))
	rec := do(t, s, "GET", "/health", func(r *http.Request) { r.Header.Set("Origin", "https://random.example") })
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("/health: code=%d acao=%q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}

	for _, path := range []string{"/snapshot", "/events"} {
		rec := do(t, s, "GET", path, func(r *http.Request) { r.Header.Set("Origin", "https://random.example") })
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s from unknown origin: got %d, want 403", path, rec.Code)
		}
	}
}

func TestSameOriginViewerIsAllowedWithoutCORS(t *testing.T) {
	ran := 0
	s := newTestServer(t, &ran)
	// The viewer's own origin sends Origin on a same-origin POST.
	rec := do(t, s, "POST", "/actions/ping", func(r *http.Request) {
		r.Header.Set(ActionHeader, "1")
		r.Header.Set("Origin", "http://127.0.0.1:8765")
	})
	if rec.Code != http.StatusOK || ran != 1 {
		t.Fatalf("same-origin action: code=%d ran=%d", rec.Code, ran)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("self origin should not receive CORS headers")
	}
	rec = do(t, s, "GET", "/snapshot", func(r *http.Request) {
		r.Header.Set("Origin", "http://evil.example:8765")
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("lookalike origin: got %d, want 403", rec.Code)
	}
}

func TestCORSGrantedOnlyToConfiguredOrigin(t *testing.T) {
	s := newTestServer(t, new(int))
	rec := do(t, s, "GET", "/snapshot", func(r *http.Request) { r.Header.Set("Origin", testOrigin) })
	if rec.Code != http.StatusOK {
		t.Fatalf("configured origin: got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != testOrigin {
		t.Fatalf("ACAO = %q, want %q", got, testOrigin)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") == "*" {
		t.Fatal("wildcard CORS leaked onto /snapshot")
	}
}

func TestSnapshotEnvelopeShape(t *testing.T) {
	s := newTestServer(t, new(int))
	rec := do(t, s, "GET", "/snapshot", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	var env struct {
		SchemaVersion int `json:"schema_version"`
		Snapshot      struct {
			Type string `json:"type"`
		} `json:"snapshot"`
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.SchemaVersion != adapter.SchemaVersion || env.Snapshot.Type != mock.Type {
		t.Fatalf("unexpected envelope: %+v", env)
	}
}

func TestActionsRequireHeaderAndAllowlistedName(t *testing.T) {
	ran := 0
	s := newTestServer(t, &ran)

	// No custom header: a cross-site form post cannot set it, so it must fail.
	rec := do(t, s, "POST", "/actions/ping", nil)
	if rec.Code != http.StatusForbidden || ran != 0 {
		t.Fatalf("missing header: code=%d ran=%d", rec.Code, ran)
	}

	rec = do(t, s, "POST", "/actions/ping", func(r *http.Request) { r.Header.Set(ActionHeader, "1") })
	if rec.Code != http.StatusOK || ran != 1 {
		t.Fatalf("allowlisted action: code=%d ran=%d body=%s", rec.Code, ran, rec.Body)
	}

	rec = do(t, s, "POST", "/actions/rm-rf", func(r *http.Request) { r.Header.Set(ActionHeader, "1") })
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action: got %d, want 404", rec.Code)
	}
}

func TestActionIgnoresRequestBody(t *testing.T) {
	ran := 0
	s := newTestServer(t, &ran)
	req := httptest.NewRequest("POST", "/actions/ping", strings.NewReader(`{"sql":"DROP TABLE x"}`))
	req.Host = testAddr
	req.Header.Set(ActionHeader, "1")
	req.Header.Set("Origin", testOrigin)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "DROP") {
		t.Fatalf("body influenced action: code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestPreflightOnlyForConfiguredOrigin(t *testing.T) {
	s := newTestServer(t, new(int))
	rec := do(t, s, "OPTIONS", "/actions/ping", func(r *http.Request) {
		r.Header.Set("Origin", testOrigin)
	})
	if rec.Header().Get("Access-Control-Allow-Origin") != testOrigin {
		t.Fatalf("preflight ACAO = %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), ActionHeader) {
		t.Fatalf("preflight does not allow %s", ActionHeader)
	}

	rec = do(t, s, "OPTIONS", "/actions/ping", func(r *http.Request) {
		r.Header.Set("Origin", "https://random.example")
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("preflight from unknown origin: got %d", rec.Code)
	}
}

func TestEventsReplayFromLastEventID(t *testing.T) {
	s := newTestServer(t, new(int))
	// Seed history through the hub's real ingest path.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan adapter.Event, 3)
	errs := make(chan error)
	go s.cfg.Hub.Run(ctx, events, errs)
	for i := uint64(1); i <= 3; i++ {
		events <- adapter.Event{Seq: i, Source: "test", ID: fmt.Sprintf("test:%d", i), Kind: "k"}
	}
	waitFor(t, func() bool { return len(s.cfg.Hub.History()) == 3 })

	req := httptest.NewRequest("GET", "/events", nil)
	req.Host = testAddr
	req.Header.Set("Last-Event-ID", "1")
	ctx2, cancel2 := context.WithCancel(context.Background())
	req = req.WithContext(ctx2)
	rec := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		s.ServeHTTP(rec, req)
		close(done)
	}()
	waitFor(t, func() bool { return strings.Count(rec.body(), "event: event") == 2 })
	cancel2()
	<-done

	body := rec.body()
	if strings.Contains(body, "id: 1\n") || !strings.Contains(body, "id: 2\n") || !strings.Contains(body, "id: 3\n") {
		t.Fatalf("replay after Last-Event-ID=1 wrong:\n%s", body)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestFrameAncestorsListsOnlyConfiguredOrigins(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(Config{
		Addr:           testAddr,
		AllowedOrigins: []string{testOrigin},
		Target:         adapter.Target{Name: "mock"},
		Adapter:        mock.New(time.Hour),
		Hub:            hub.New(hub.DefaultHistory, logger),
		Web:            fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}},
		Logger:         logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/", "/health"} {
		req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+path, nil)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)

		got := rec.Header().Get("Content-Security-Policy")
		want := "frame-ancestors 'self' " + testOrigin
		if got != want {
			t.Errorf("%s: CSP = %q, want %q", path, got, want)
		}
		if strings.Contains(got, "evil.example") {
			t.Errorf("%s: CSP lists an origin that was never configured", path)
		}
	}
}

func TestNewRejectsOriginsThatWouldWidenFraming(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bad := []string{
		"https://*",
		"https://a.example *",
		"https://a.example; script-src *",
		"https://a.example/some/path",
		"https://a.example?q=1",
		"https://a.example#frag",
		"https://user@a.example",
	}
	for _, o := range bad {
		_, err := New(Config{
			Addr:           testAddr,
			AllowedOrigins: []string{o},
			Target:         adapter.Target{Name: "mock"},
			Adapter:        mock.New(time.Hour),
			Hub:            hub.New(hub.DefaultHistory, logger),
			Logger:         logger,
		})
		if err == nil {
			t.Errorf("origin %q: want rejected, got accepted", o)
		}
	}
}

func TestNewAcceptsAnOriginWithNoPathOrQuery(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, o := range []string{"https://a.example", "https://a.example:8443", "HTTPS://A.Example"} {
		_, err := New(Config{
			Addr:           testAddr,
			AllowedOrigins: []string{o},
			Target:         adapter.Target{Name: "mock"},
			Adapter:        mock.New(time.Hour),
			Hub:            hub.New(hub.DefaultHistory, logger),
			Logger:         logger,
		})
		if err != nil {
			t.Errorf("origin %q: want accepted, got %v", o, err)
		}
	}
}
