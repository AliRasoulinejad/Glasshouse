package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"glasshouse/gateway/internal/lab"
)

const public = "https://glasshouse.example"

var key = []byte("0123456789abcdef0123456789abcdef")

type nopRunner struct{ addr string }

func (n nopRunner) Start(context.Context, string, []string) (string, error) { return n.addr, nil }
func (nopRunner) Stop(context.Context, string) error                        { return nil }
func (nopRunner) StopAll(context.Context) error                             { return nil }

// fakeInspector records what the gateway sends a lab.
type fakeInspector struct {
	srv  *httptest.Server
	seen chan *http.Request
}

func newFakeInspector(t *testing.T) *fakeInspector {
	f := &fakeInspector{seen: make(chan *http.Request, 10)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.seen <- r.Clone(context.Background())
		switch r.URL.Path {
		case "/health":
			io.WriteString(w, `{"actions":[{"name":"insert_rows","query":"INSERT ..."}]}`)
		case "/snapshot":
			io.WriteString(w, `{"schema_version":1,"snapshot":{"type":"postgres.heap_and_index"}}`)
		case "/actions/insert_rows":
			io.WriteString(w, `{"action":"insert_rows","result":{"tag":"INSERT 0 10"}}`)
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "id: 7\nevent: event\ndata: {}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newTestServer(t *testing.T, labAddr string, providers ...Provider) (*Server, *lab.Manager) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := lab.NewManager(lab.Config{MaxLabs: 2, MaxQueue: 2, Idle: time.Hour, Lifetime: time.Hour, StartsPerHour: 5,
		QueueStale: time.Hour, StartTimeout: 10 * time.Second}, nopRunner{addr: labAddr}, lab.HTTPProbe(http.DefaultClient), log)
	s, err := New(Config{PublicURL: public, SessionKey: key, Providers: providers, Labs: mgr,
		Client: http.DefaultClient, LabClient: http.DefaultClient, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	return s, mgr
}

func sessionCookieFor(t *testing.T, s *Server, sess Session) *http.Cookie {
	v, err := s.signer.Sign(sessionCookie, sess)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookie, Value: v}
}

func do(s *Server, method, path string, mod func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, public+path, nil)
	if method != http.MethodGet {
		r.Header.Set("Origin", public)
		r.Header.Set(ActionHeader, "1")
	}
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestEdgeRules(t *testing.T) {
	s, _ := newTestServer(t, "")
	valid := sessionCookieFor(t, s, Session{Provider: "github", Subject: "1", Expires: time.Now().Add(time.Hour).Unix()})
	cases := []struct {
		name   string
		method string
		mod    func(*http.Request)
		want   int
	}{
		{"wrong host (DNS rebinding)", "GET", func(r *http.Request) { r.Host = "evil.example"; r.AddCookie(valid) }, 403},
		{"foreign origin on GET", "GET", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example"); r.AddCookie(valid) }, 403},
		{"POST without origin", "POST", func(r *http.Request) { r.Header.Del("Origin"); r.AddCookie(valid) }, 403},
		{"POST from foreign origin", "POST", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example"); r.AddCookie(valid) }, 403},
		{"POST without action header", "POST", func(r *http.Request) { r.Header.Del(ActionHeader); r.AddCookie(valid) }, 403},
		{"no session", "GET", nil, 401},
		{"tampered session", "GET", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: sessionCookie, Value: valid.Value + "x"}) }, 401},
		{"expired session", "GET", func(r *http.Request) {
			r.AddCookie(sessionCookieFor(t, s, Session{Provider: "github", Subject: "1", Expires: time.Now().Add(-time.Second).Unix()}))
		}, 401},
		{"state cookie replayed as session", "GET", func(r *http.Request) {
			v, _ := s.signer.Sign(oauthCookie, Session{Provider: "github", Subject: "1", Expires: time.Now().Add(time.Hour).Unix()})
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})
		}, 401},
		{"valid session", "GET", func(r *http.Request) { r.AddCookie(valid) }, 200},
	}
	for _, c := range cases {
		path := "/api/lab"
		if w := do(s, c.method, path, c.mod); w.Code != c.want {
			t.Errorf("%s: %s %s = %d, want %d", c.name, c.method, path, w.Code, c.want)
		}
	}
}

func TestProxySendsNothingFromTheReader(t *testing.T) {
	insp := newFakeInspector(t)
	s, mgr := newTestServer(t, strings.TrimPrefix(insp.srv.URL, "http://"))
	cookie := sessionCookieFor(t, s, Session{Provider: "github", Subject: "42", Expires: time.Now().Add(time.Hour).Unix()})
	withCookie := func(r *http.Request) { r.AddCookie(cookie) }

	if w := do(s, "POST", "/api/lab", withCookie); w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for mgr.Status("github:42").State != lab.Ready {
		if time.Now().After(deadline) {
			t.Fatal("lab never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	<-insp.seen // the /health probe

	w := do(s, "POST", "/api/lab/actions/insert_rows", func(r *http.Request) {
		withCookie(r)
		r.Body = io.NopCloser(strings.NewReader("DROP TABLE glasshouse_demo"))
	})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "INSERT 0 10") {
		t.Fatalf("action: %d %s", w.Code, w.Body)
	}
	got := <-insp.seen
	if got.Host != lab.InspectorHost || got.Header.Get("Origin") != "" || got.Header.Get("Cookie") != "" ||
		got.Header.Get(ActionHeader) != "1" || got.ContentLength > 0 {
		t.Fatalf("inspector saw Host=%q Origin=%q Cookie=%q header=%q len=%d", got.Host, got.Header.Get("Origin"),
			got.Header.Get("Cookie"), got.Header.Get(ActionHeader), got.ContentLength)
	}

	if w := do(s, "POST", "/api/lab/actions/drop_everything", withCookie); w.Code != 404 {
		t.Fatalf("unlisted action: %d, want 404", w.Code)
	}
	if w := do(s, "POST", "/api/lab/actions/..%2Fsnapshot", withCookie); w.Code != 404 {
		t.Fatalf("path trick: %d, want 404", w.Code)
	}
	if w := do(s, "GET", "/api/lab/snapshot", withCookie); w.Code != 200 || !strings.Contains(w.Body.String(), "heap_and_index") {
		t.Fatalf("snapshot: %d %s", w.Code, w.Body)
	}
	<-insp.seen
	w = do(s, "GET", "/api/lab/events", func(r *http.Request) { withCookie(r); r.Header.Set("Last-Event-ID", "5") })
	if !strings.Contains(w.Body.String(), "id: 7") || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events: %q %q", w.Body, w.Header())
	}
	if ev := <-insp.seen; ev.Header.Get("Last-Event-ID") != "5" {
		t.Fatalf("Last-Event-ID not forwarded: %q", ev.Header.Get("Last-Event-ID"))
	}

	// Another reader cannot reach this lab.
	other := sessionCookieFor(t, s, Session{Provider: "google", Subject: "42", Expires: time.Now().Add(time.Hour).Unix()})
	if w := do(s, "GET", "/api/lab/snapshot", func(r *http.Request) { r.AddCookie(other) }); w.Code != 404 {
		t.Fatalf("other reader's snapshot: %d, want 404", w.Code)
	}
}

func TestActionRateLimit(t *testing.T) {
	s, _ := newTestServer(t, "")
	for i := 0; i < actionLimit; i++ {
		if !s.allowAction("u") {
			t.Fatalf("action %d refused", i)
		}
	}
	if s.allowAction("u") {
		t.Fatal("action over the limit allowed")
	}
	if !s.allowAction("v") {
		t.Fatal("another reader was limited")
	}
}

// fakeProvider is an OAuth server plus userinfo endpoint.
func fakeProvider(t *testing.T, verified bool) (Provider, *httptest.Server, *url.Values) {
	var tokenForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			r.ParseForm()
			tokenForm = r.PostForm
			io.WriteString(w, `{"access_token":"tok"}`)
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"sub": "g-1", "name": "Reader", "email_verified": verified})
		}
	}))
	t.Cleanup(srv.Close)
	p := Google("cid", "csecret", srv.URL+"/userinfo")
	p.AuthURL, p.TokenURL = srv.URL+"/auth", srv.URL+"/token"
	return p, srv, &tokenForm
}

func login(t *testing.T, s *Server, next string) (state string, cookie *http.Cookie) {
	w := do(s, "GET", "/auth/google/login?next="+url.QueryEscape(next), nil)
	if w.Code != http.StatusFound {
		t.Fatalf("login: %d", w.Code)
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("redirect_uri") != public+"/auth/google/callback" {
		t.Fatalf("authorize URL missing PKCE or redirect: %s", loc)
	}
	return q.Get("state"), w.Result().Cookies()[0]
}

func TestOAuthFlow(t *testing.T) {
	p, _, form := fakeProvider(t, true)
	s, _ := newTestServer(t, "", p)
	state, oc := login(t, s, "/articles/postgres-heap-page/")

	w := do(s, "GET", "/auth/google/callback?code=c&state="+url.QueryEscape(state), func(r *http.Request) { r.AddCookie(oc) })
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/articles/postgres-heap-page/#try-it-yourself" {
		t.Fatalf("callback: %d %q %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	if (*form).Get("code_verifier") == "" || (*form).Get("client_secret") != "csecret" {
		t.Fatalf("token request lacked verifier or secret: %v", *form)
	}
	var sc *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			sc = c
		}
	}
	if sc == nil || !sc.HttpOnly || !sc.Secure || sc.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie %+v", sc)
	}
	me := do(s, "GET", "/api/me", func(r *http.Request) { r.AddCookie(sc) })
	if me.Code != 200 || !strings.Contains(me.Body.String(), `"name":"Reader"`) {
		t.Fatalf("me: %d %s", me.Code, me.Body)
	}
}

func TestOAuthRefusals(t *testing.T) {
	p, _, _ := fakeProvider(t, false)
	s, _ := newTestServer(t, "", p)

	state, oc := login(t, s, "/")
	w := do(s, "GET", "/auth/google/callback?code=c&state="+url.QueryEscape(state), func(r *http.Request) { r.AddCookie(oc) })
	if w.Code != http.StatusForbidden {
		t.Fatalf("unverified email: %d, want 403", w.Code)
	}

	state, oc = login(t, s, "/")
	w = do(s, "GET", "/auth/google/callback?code=c&state=forged", func(r *http.Request) { r.AddCookie(oc) })
	if w.Code != http.StatusBadRequest {
		t.Fatalf("state mismatch: %d, want 400", w.Code)
	}
	w = do(s, "GET", "/auth/google/callback?code=c&state="+url.QueryEscape(state), nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("no state cookie: %d, want 400", w.Code)
	}
	if w := do(s, "GET", "/auth/nope/login", nil); w.Code != 404 {
		t.Fatalf("unknown provider: %d", w.Code)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/articles/x/": "/articles/x/", "": "/", "https://evil.example": "/", "//evil.example": "/",
		"/\\evil.example": "/", "/a\r\nSet-Cookie: x": "/", "javascript:alert(1)": "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateAddr(t *testing.T) {
	for addr, ok := range map[string]bool{"127.0.0.1:8080": true, "localhost:8080": true, "0.0.0.0:8080": false, ":8080": false, "10.0.0.1:80": false} {
		if err := ValidateAddr(addr); (err == nil) != ok {
			t.Errorf("ValidateAddr(%q) = %v", addr, err)
		}
	}
}
