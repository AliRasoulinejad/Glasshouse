# Glasshouse Website Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the first Glasshouse article: a static page that walks the reader through a live Postgres heap page, with a panel embedded at the end that drives the Inspector running in the reader's own compose stack.

**Architecture:** Static articles are built from Markdown by a small Go program in `site/`. Each article declares the Inspector actions its panel may call, and the build fails if an action is unknown. The panel is an iframe of the Inspector's own viewer on `127.0.0.1:8765`. The Inspector already runs as a service in `adapters/postgres/compose.yml` and already has the `Actioner` contract, so it needs only a frame-policy header and a small listing flag. It gets no Docker access.

**Tech Stack:** Go 1.24 (Inspector and site build), `github.com/yuin/goldmark` for Markdown with raw HTML disabled, Docker Compose, plain HTML and ES modules for the viewer (unchanged).

**Spec:** `docs/superpowers/specs/2026-10-04-website-design.md`

## Global Constraints

- Bind addresses stay loopback only: `127.0.0.1` or `localhost`; container mode may use `0.0.0.0` only inside the container.
- Actions take no request body and no browser input; SQL stays constant.
- The Inspector never talks to the Docker socket; no compose control from the browser.
- Compose ports stay published on `127.0.0.1` only.
- The viewer writes API data with `textContent` only.
- The `-origin` allowlist has no wildcard.
- The event contract in `schema/README.md` does not change; `SchemaVersion` stays `1`.
- Go code passes `gofmt -l` and `go vet`, the way `make test` checks.

## Review Focus

1. Lab stopped while the reader opens the article: the panel must say the lab is not running, not show stale data. Covered by the manual check in Task 7.
2. Article names an action the Inspector does not have: the build must fail, not publish a dead button. Covered by Task 5.
3. Article contains raw `<script>` or HTML in its Markdown: the build must drop it, not render it. Covered by Task 5.
4. A page from an origin not in the allowlist tries to frame the viewer or read the API: it must get no frame permission and no CORS. Covered by Task 2.
5. Reader leaves `GLASSHOUSE_ALLOWED_ORIGIN` unset on a real host: the default origin must not silently grant access to the wrong site. Covered by Task 6.

---

### Task 1: Spike — can a public article reach and frame the loopback Inspector?

This task has no repo code. Its output is a recorded decision in the spec. The spec's PNA risk is unverified, and the iframe approach may avoid it.

**Prerequisite (user):** pick the public host for the test page. This spike needs a real public `https://` origin. A `localhost` page does not trigger the same rules. Ask before deploying anything.

**Files:**
- Create (throwaway, not committed): `$SCRATCHPAD/spike/index.html`
- Modify: `docs/superpowers/specs/2026-10-04-website-design.md` (add a "Spike results" section)

**Interfaces:**
- Consumes: the existing Inspector, run with `make run-mock ORIGIN=<the test host origin>`.
- Produces: a decision: `iframe` or `fetch` (or `fetch+PNA`) as the panel's mechanism, which decides whether Task 3 runs.

- [ ] **Step 1: Write the spike page**

The page has two panels. One loads the viewer in an iframe. The other calls `GET /health` with `fetch` from script.

```html
<!doctype html>
<meta charset="utf-8">
<title>Glasshouse spike</title>
<h1>Iframe</h1>
<iframe src="http://127.0.0.1:8765/" width="600" height="300"></iframe>
<h1>Fetch</h1>
<pre id="out">pending</pre>
<script>
  fetch("http://127.0.0.1:8765/health", { mode: "cors" })
    .then(r => r.text())
    .then(t => document.getElementById("out").textContent = "ok: " + t)
    .catch(e => document.getElementById("out").textContent = "failed: " + e);
</script>
```

- [ ] **Step 2: Run the Inspector on the mock adapter with the test origin**

Run: `make run-mock ORIGIN=https://<test-host-origin>`
Expected: `inspector listening` in the log.

- [ ] **Step 3: Serve the spike page from the public host and open it in Chrome, Firefox and Safari**

Record for each browser: whether the iframe shows the viewer, whether `fetch` returns `ok`, and any console message that mentions Private Network Access or mixed content.

- [ ] **Step 4: Record the result in the spec**

Add a "Spike results" section to the spec with the table of browser results and one of these decisions:
- Iframe works in all three: the panel is an iframe. Task 2 runs. Task 3 is skipped.
- Iframe works, `fetch` fails: the panel is an iframe and does not use `fetch` from the article. Task 2 runs. Task 3 is skipped.
- Iframe fails in any browser: Task 3 runs.

- [ ] **Step 5: Commit the spec update**

```bash
git add docs/superpowers/specs/2026-10-04-website-design.md
git commit -m "Record Private Network Access spike results

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 2: Frame policy header for the viewer

Lets only the configured article origins embed the viewer. Without it, any site can frame the viewer.

**Files:**
- Modify: `inspector/internal/server/server.go` (`ServeHTTP`, and a new `setFraming` method)
- Test: `inspector/internal/server/server_test.go`

**Interfaces:**
- Consumes: `Config.AllowedOrigins`, the `Server.origins` map.
- Produces: every response carries `Content-Security-Policy: frame-ancestors 'self' <allowed origins>`. Later tasks do not depend on it.

- [ ] **Step 1: Write the failing test**

Add to `inspector/internal/server/server_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `make test` (or `cd inspector && go test ./internal/server -run TestFrameAncestors -v`)
Expected: FAIL, `CSP = "", want "frame-ancestors 'self' https://article.example"`

- [ ] **Step 3: Write the minimal implementation**

In `inspector/internal/server/server.go`, change `ServeHTTP` and add `setFraming`:

```go
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
```

`sort` and `strings` are already imported in `server.go`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `make test`
Expected: PASS, and `gofmt -l` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add inspector/internal/server/server.go inspector/internal/server/server_test.go
git commit -m "Restrict framing of the viewer to configured article origins

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 3 (conditional): Private Network Access preflight

Run this task only if Task 1 recorded a `fetch+PNA` decision. If the panel is an iframe, skip it.

**Files:**
- Modify: `inspector/internal/server/server.go` (`setCORS`, `handlePreflight`)
- Test: `inspector/internal/server/server_test.go`

**Interfaces:**
- Consumes: `setCORS`, `Server.origins`.
- Produces: preflights from a configured origin that include `Access-Control-Request-Private-Network: true` get `Access-Control-Allow-Private-Network: true`.

- [ ] **Step 1: Write the failing test**

```go
func TestPreflightGrantsPrivateNetworkOnlyToConfiguredOrigin(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(Config{
		Addr:           testAddr,
		AllowedOrigins: []string{testOrigin},
		Target:         adapter.Target{Name: "mock"},
		Adapter:        mock.New(time.Hour),
		Hub:            hub.New(hub.DefaultHistory, logger),
		Logger:         logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodOptions, "http://"+testAddr+"/actions/bump", nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Errorf("configured origin: got %q, want true", got)
	}

	req = httptest.NewRequest(http.MethodOptions, "http://"+testAddr+"/actions/bump", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Private-Network"); got != "" {
		t.Errorf("unknown origin: got %q, want no grant", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `make test`
Expected: FAIL, `configured origin: got "", want true`

- [ ] **Step 3: Write the minimal implementation**

In `handlePreflight`, after `setCORS(w, r)`:

```go
	if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
		if _, ok := s.origins[r.Header.Get("Origin")]; ok {
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}
	}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add inspector/internal/server/server.go inspector/internal/server/server_test.go
git commit -m "Grant Private Network Access preflight to configured origins only

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 4: `-list-actions` flag

The site build needs the real action names without a database. This flag prints them and exits.

**Files:**
- Modify: `inspector/cmd/inspector/main.go` (new flag and `listActions`)
- Create: `inspector/cmd/inspector/main_test.go`

**Interfaces:**
- Consumes: `adapter.Actioner`, `postgres.New`, `mock.New`.
- Produces: `listActions(name string, w io.Writer) error`. Writes one action name per line, sorted. Needs no DSN.

- [ ] **Step 1: Write the failing test**

Create `inspector/cmd/inspector/main_test.go`:

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestListActionsPrintsPostgresActionsWithoutDSN(t *testing.T) {
	t.Setenv("GLASSHOUSE_PG_DSN", "")

	var out bytes.Buffer
	if err := listActions("postgres", &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "insert_rows" {
		t.Errorf("got %q, want %q", got, "insert_rows")
	}
}

func TestListActionsRejectsAdapterWithoutActions(t *testing.T) {
	var out bytes.Buffer
	if err := listActions("nope", &out); err == nil {
		t.Error("unknown adapter: want error")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd inspector && go test ./cmd/inspector -run TestListActions -v`
Expected: FAIL, `undefined: listActions`

- [ ] **Step 3: Write the minimal implementation**

In `main.go`, add `"io"` and `"sort"` to the imports, then add the flag in `main()` before `flag.Parse()`:

```go
	listFlag := flag.Bool("list-actions", false, "print the adapter's action names and exit")
```

After `flag.Parse()`, before the logger is built:

```go
	if *listFlag {
		if err := listActions(*adapterName, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
```

And add the function:

```go
// listActions prints the action names an adapter exposes, one per line,
// sorted. It needs no target, so the site build can run without a database.
func listActions(name string, w io.Writer) error {
	var a adapter.Actioner
	switch name {
	case "mock":
		a = mock.New(time.Second)
	case "postgres":
		a = postgres.New(500 * time.Millisecond)
	default:
		return fmt.Errorf("unknown adapter %q", name)
	}
	names := make([]string, 0)
	for n := range a.Actions() {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintln(w, n)
	}
	return nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add inspector/cmd/inspector/main.go inspector/cmd/inspector/main_test.go
git commit -m "Add -list-actions to print an adapter's action names

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 5: Static site build with action validation

Turns Markdown articles into static HTML. Refuses an article whose panel names an unknown action, and drops raw HTML from the Markdown. This task also updates the Makefile and `.gitignore`, and makes the `CLAUDE.md` changes the spec lists.

**Files:**
- Create: `site/go.mod`
- Create: `site/cmd/build/main.go`
- Create: `site/internal/article/article.go`
- Create: `site/internal/article/article_test.go`
- Create: `site/articles/` (added with Task 7)
- Modify: `Makefile` (`site-actions`, `site-build` targets; update the help block)
- Modify: `.gitignore` (`/site/dist/`, `/site/actions.txt`)
- Modify: `CLAUDE.md` (the "Not built yet" line and the Layout list; the compose-control rule)

**Interfaces:**
- Consumes: `site/actions.txt`, one action name per line, produced by `make site-actions`.
- Produces: `article.Parse(src []byte) (Article, error)`, where `Article` has `Title string`, `Actions []string`, `Body []byte` (HTML). `article.Build(src []byte, known map[string]bool) (Article, error)` rejects any declared action not in `known`.
- Front matter format, one key per line: `title: <text>`, `actions: [a, b]`. Anything else is an error.
- The panel marker in the Markdown is exactly `<!-- lab-panel -->`. Each article has exactly one.

- [ ] **Step 1: Write the failing tests**

Create `site/internal/article/article_test.go`:

```go
package article

import (
	"strings"
	"testing"
)

const good = `---
title: Reading a heap page
actions: [insert_rows]
---

Some prose.

<!-- lab-panel -->
`

func known() map[string]bool { return map[string]bool{"insert_rows": true} }

func TestBuildAcceptsKnownActions(t *testing.T) {
	a, err := Build([]byte(good), known())
	if err != nil {
		t.Fatal(err)
	}
	if a.Title != "Reading a heap page" {
		t.Errorf("title = %q", a.Title)
	}
	if len(a.Actions) != 1 || a.Actions[0] != "insert_rows" {
		t.Errorf("actions = %v", a.Actions)
	}
	if !strings.Contains(string(a.Body), `<iframe`) {
		t.Error("panel marker was not replaced with an iframe")
	}
}

func TestBuildRejectsUnknownAction(t *testing.T) {
	src := strings.Replace(good, "insert_rows", "drop_table", 1)
	if _, err := Build([]byte(src), known()); err == nil {
		t.Error("unknown action: want error")
	}
}

func TestBuildDropsRawHTML(t *testing.T) {
	src := strings.Replace(good, "Some prose.", "<script>alert(1)</script>", 1)
	a, err := Build([]byte(src), known())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(a.Body), "<script>") {
		t.Error("raw HTML from the Markdown reached the page")
	}
}

func TestBuildRequiresExactlyOnePanel(t *testing.T) {
	src := strings.Replace(good, "<!-- lab-panel -->", "", 1)
	if _, err := Build([]byte(src), known()); err == nil {
		t.Error("no panel: want error")
	}
}

func TestBuildRejectsUnknownFrontMatterKey(t *testing.T) {
	src := strings.Replace(good, "title:", "owner:", 1)
	if _, err := Build([]byte(src), known()); err == nil {
		t.Error("unknown key: want error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd site && go test ./internal/article -v`
Expected: FAIL, `undefined: Build`

- [ ] **Step 3: Write the minimal implementation**

Create `site/go.mod`:

```
module glasshouse/site

go 1.24

require github.com/yuin/goldmark v1.7.8
```

Run: `cd site && go mod tidy`

Create `site/internal/article/article.go`:

```go
// Package article parses one Markdown article, checks its declared lab
// actions against the Inspector's real action list, and renders it to HTML.
package article

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
)

// panelMarker is the one place in an article where the lab panel goes.
const panelMarker = "<!-- lab-panel -->"

// panelHTML is the Inspector viewer, framed. It is always the loopback
// address: the reader runs the lab on their own machine.
const panelHTML = `<iframe class="lab-panel" src="http://127.0.0.1:8765/" title="Lab panel" height="640"></iframe>`

// Article is one built page.
type Article struct {
	Title   string
	Actions []string
	Body    []byte
}

// Build parses src, verifies every declared action is in known, and renders
// the body. Raw HTML in the Markdown is not passed through.
func Build(src []byte, known map[string]bool) (Article, error) {
	front, body, err := split(src)
	if err != nil {
		return Article{}, err
	}

	var a Article
	for _, line := range front {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return Article{}, fmt.Errorf("front matter line %q: want key: value", line)
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "title":
			a.Title = val
		case "actions":
			a.Actions = parseList(val)
		default:
			return Article{}, fmt.Errorf("unknown front matter key %q", key)
		}
	}
	if a.Title == "" {
		return Article{}, errors.New("front matter needs a title")
	}
	for _, name := range a.Actions {
		if !known[name] {
			return Article{}, fmt.Errorf("article %q declares unknown action %q", a.Title, name)
		}
	}

	if n := strings.Count(body, panelMarker); n != 1 {
		return Article{}, fmt.Errorf("article %q needs exactly one %s, found %d", a.Title, panelMarker, n)
	}
	body = strings.Replace(body, panelMarker, "\n\n"+panelHTML+"\n\n", 1)

	var buf bytes.Buffer
	// No WithUnsafe: raw HTML in the Markdown is dropped, not rendered.
	if err := goldmark.Convert([]byte(body), &buf); err != nil {
		return Article{}, err
	}
	a.Body = buf.Bytes()
	return a, nil
}

// split separates the "---" delimited front matter from the Markdown body.
func split(src []byte) ([]string, string, error) {
	text := string(src)
	if !strings.HasPrefix(text, "---\n") {
		return nil, "", errors.New("article needs front matter starting with ---")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return nil, "", errors.New("front matter is not closed with ---")
	}
	front := strings.Split(text[4:4+end], "\n")
	return front, text[4+end+5:], nil
}

// parseList reads "[a, b]" into a slice. An empty list is allowed.
func parseList(val string) []string {
	val = strings.TrimSuffix(strings.TrimPrefix(val, "["), "]")
	var out []string
	for _, part := range strings.Split(val, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd site && go test ./internal/article -v`
Expected: PASS for all five tests.

- [ ] **Step 5: Add the build command**

Create `site/cmd/build/main.go`:

```go
// Command build turns site/articles/*.md into static HTML in site/dist.
//
//	go run ./cmd/build -articles articles -out dist -actions actions.txt
package main

import (
	"bufio"
	"flag"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"

	"glasshouse/site/internal/article"
)

const page = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · Glasshouse</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 16px/1.6 system-ui, sans-serif; max-width: 48rem; margin: 0 auto; padding: 0 16px; }
  .lab-panel { width: 100%; border: 1px solid #8884; border-radius: 6px; }
</style>
<main>
{{.Body}}
</main>
`

func main() {
	articles := flag.String("articles", "articles", "directory of Markdown articles")
	out := flag.String("out", "dist", "output directory")
	actionsFile := flag.String("actions", "actions.txt", "one known action name per line, from make site-actions")
	flag.Parse()

	if err := run(*articles, *out, *actionsFile); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}
}

func run(articlesDir, outDir, actionsFile string) error {
	known, err := readActions(actionsFile)
	if err != nil {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(articlesDir, "*.md"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("no articles in %s", articlesDir)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	tmpl := template.Must(template.New("page").Parse(page))

	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		a, err := article.Build(src, known)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		f, err := os.Create(filepath.Join(outDir, strings.TrimSuffix(filepath.Base(p), ".md")+".html"))
		if err != nil {
			return err
		}
		err = tmpl.Execute(f, map[string]any{
			"Title": a.Title,
			"Body":  template.HTML(a.Body),
		})
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func readActions(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read actions list (run make site-actions): %w", err)
	}
	defer f.Close()
	known := make(map[string]bool)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if name := strings.TrimSpace(sc.Text()); name != "" {
			known[name] = true
		}
	}
	return known, sc.Err()
}
```

Note: `template.HTML(a.Body)` is safe here because `article.Build` already removed raw HTML and only goldmark output and the trusted panel iframe reach it.

- [ ] **Step 6: Wire the Makefile and gitignore**

In `Makefile`, add these targets and list them in `.PHONY` and the help block:

```make
# ---- site -------------------------------------------------------------------

site-actions:
	cd $(INSPECTOR_DIR) && $(GO) run ./cmd/inspector -list-actions -adapter postgres > ../site/actions.txt

site-build: site-actions
	cd site && go run ./cmd/build -articles articles -out dist -actions actions.txt
	@echo "site built in site/dist"
```

Help block lines to add:

```
#   make site-build   build the static articles into site/dist (validates lab actions)
```

In `.gitignore`, add:

```
# generated by make site-build
/site/dist/
/site/actions.txt
```

Note: `site-actions` runs with `$(GO)`, so `USE_DOCKER=1` works here too. The `-list-actions` output goes to `site/actions.txt`, which the `cd site` build reads.

- [ ] **Step 7: Update CLAUDE.md**

In `CLAUDE.md`:
- In the Layout list, add a bullet for `site/`: "static articles, built by `make site-build`; plain files, no backend."
- Replace the line "Not built yet (deferred, do not start unless asked): the Website (Phase 1), ..." with: "Not built yet (deferred): the Postgres index and replication adapters (3b/3c), and Redis/Mongo/MinIO. The Website is in progress; see `docs/superpowers/specs/2026-10-04-website-design.md`."
- In Security rules, add: "Compose is never run from the browser. The Inspector has no Docker access; the reader starts the lab in their own terminal."

- [ ] **Step 8: Run the checks**

Run: `make test` and `make site-build` (with the article in Task 7 present, or after Task 7). Before Task 7, `make site-build` should fail with "no articles" — that is the expected result at this point.

- [ ] **Step 9: Commit**

```bash
git add site/ Makefile .gitignore CLAUDE.md
git commit -m "Add static article build with lab action validation

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 6: Lab compose origin and the viewer's frame header in the stack

The compose file already runs the Inspector. This task sets the public origin explicitly, so a reader who forgets the variable gets a clear error, not a silent default.

**Files:**
- Modify: `adapters/postgres/compose.yml` (`-origin` value)
- Modify: `adapters/postgres/README.md` (the origin variable and the header check)
- Modify: `Makefile` (`ORIGIN` default)

**Interfaces:**
- Consumes: the `-origin` flag and Task 2's frame header.
- Produces: `GLASSHOUSE_ALLOWED_ORIGIN` is required, with no default.

- [ ] **Step 1: Make the origin required**

In `adapters/postgres/compose.yml`, replace the `-origin` line:

```yaml
      - -origin
      - ${GLASSHOUSE_ALLOWED_ORIGIN:?set GLASSHOUSE_ALLOWED_ORIGIN to the article site origin, e.g. https://example.com}
```

- [ ] **Step 2: Verify the compose file renders**

Run:
```bash
GLASSHOUSE_INSPECTOR_CONTEXT=. GLASSHOUSE_ALLOWED_ORIGIN=https://example.com docker compose -f adapters/postgres/compose.yml config -q && echo ok
GLASSHOUSE_INSPECTOR_CONTEXT=. docker compose -f adapters/postgres/compose.yml config -q; echo "exit=$?"
```
Expected: the first prints `ok`. The second exits non-zero with the message about `GLASSHOUSE_ALLOWED_ORIGIN`.

- [ ] **Step 3: Verify the header in the running stack**

Run the stack with the origin set, then check the header:
```bash
GLASSHOUSE_ALLOWED_ORIGIN=https://example.com make stack-up
curl -sI -H 'Host: 127.0.0.1:8765' http://127.0.0.1:8765/ | grep -i content-security-policy
```
Expected: `Content-Security-Policy: frame-ancestors 'self' https://example.com`

Run: `sh adapters/postgres/demo.sh` — expected: the before/after snapshot prints as it does today.

If Docker is not available on this machine, say so in the report and do Steps 1 and 2 only.

- [ ] **Step 4: Update the Makefile default and the README**

In `Makefile`, leave `ORIGIN ?= https://article.example` for `make run-*` only. Add a line to `adapters/postgres/README.md` under "Run it":

```
Set `GLASSHOUSE_ALLOWED_ORIGIN` to the origin the article is served from
(for example `https://example.com`). It is required. It sets which sites may
frame the viewer.
```

- [ ] **Step 5: Commit**

```bash
git add adapters/postgres/compose.yml adapters/postgres/README.md Makefile
git commit -m "Require GLASSHOUSE_ALLOWED_ORIGIN for the lab stack

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 7: First article — the heap-page walkthrough

**Files:**
- Create: `site/articles/postgres-heap-page.md`

**Interfaces:**
- Consumes: the `insert_rows` action (Task 4 lists it), the viewer's existing heap-page view (`inspector/web/static/views/postgres-heap-page.js`), `make site-build` (Task 5).
- Produces: `site/dist/postgres-heap-page.html`.

- [ ] **Step 1: Write the article**

Create `site/articles/postgres-heap-page.md` with this front matter and these sections:

```markdown
---
title: Reading a heap page
actions: [insert_rows]
---
```

Sections, in order, each written in plain prose:

1. **What you will see.** One paragraph: a PostgreSQL table stores rows in fixed-size pages, and this article shows one page of a real table.
2. **Start the lab.** The exact command from `adapters/postgres/README.md`, with `GLASSHOUSE_ALLOWED_ORIGIN` set to the article's origin. A fenced `sh` block.
3. **Read the page.** Point at the header and the item list in the panel. Explain `lp`, `lp_off`, `lp_len`, and `t_xmin`, using only fields the viewer shows.
4. **Insert rows.** Tell the reader to press the **insert_rows** action in the panel and watch the page change.
5. **Check it yourself.** Show `sh adapters/postgres/demo.sh` as the command-line equivalent.
6. The panel marker on its own line, exactly:

```
<!-- lab-panel -->
```

The panel is the last thing on the page.

- [ ] **Step 2: Build and check**

Run: `make site-build`
Expected: `site built in site/dist`, and `site/dist/postgres-heap-page.html` exists and contains `<iframe class="lab-panel"`.

- [ ] **Step 3: Manual check of the lab-not-running case (Review Focus item 1)**

With the stack stopped, open `site/dist/postgres-heap-page.html` in a browser served from the public host. Expected: the panel shows the viewer's own "connecting" or error state, and the article text still reads correctly. No values from an earlier session appear as live.

Record the result in the report. If the viewer shows stale values as live, that is a viewer bug. Stop and report it. Do not fix it inside this task.

- [ ] **Step 4: Commit**

```bash
git add site/articles/postgres-heap-page.md
git commit -m "Add the heap-page walkthrough article

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Self-review

- **Spec coverage:** static articles (Tasks 5, 7); the reader runs compose (Task 6, README); the Inspector as controller with fixed actions (Tasks 2, 4, 5); no Docker access (Global Constraints, Task 5 docs); origin allowlist (Tasks 2, 6); PNA risk (Tasks 1, 3); first article (Task 7); documentation changes (Task 5 Step 7). The spec's "custom commands deferred" is not in the plan, as intended.
- **Review Focus:** items 1 (Task 7 Step 3), 2 (Task 5 test), 3 (Task 5 test), 4 (Task 2 test), 5 (Task 6 Step 2). Each is pinned.
- **Type consistency:** `article.Build(src, known)` and `Article{Title, Actions, Body}` match between the test and the build command. `listActions(name, w)` matches its test. `setFraming` and `setCORS` do not conflict.
- **Known gap:** Task 5 Step 8's `make site-build` fails until Task 7 adds an article. That is expected and noted.
- **Blocker:** Task 1 needs the user's public host before it can start. Tasks 4 and 5 do not depend on it, so they can run first.

## Execution

Plan saved to `docs/superpowers/plans/2026-10-04-website-implementation.md`. Please review it. Tasks 1 and 3 depend on the spike, and Task 1 needs your public host name. Tasks 2, 4 and 5 do not depend on it.

Two execution options:

- **Subagent-driven:** a fresh subagent per task, with a fresh reviewer after each. Most thorough, since Tasks 2 and 3 touch the security rules.
- **Native:** I implement the tasks myself in this session, and one reviewer checks the whole branch at the end. Cheaper and faster, since the tasks are small and sequential.

I recommend **Native**, because the tasks are small and sequential, and the security-sensitive tests (Tasks 2 and 3) already pin the rules. Before this goes out, the whole-branch review should cover the security rules in `CLAUDE.md`.

Which approach do you want, and what is the public host for the spike?
