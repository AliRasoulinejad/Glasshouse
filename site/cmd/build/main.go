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
  body { font: 16px/1.6 system-ui, sans-serif; max-width: 48rem; margin: 0 auto; padding: 0 16px 96px; }
  .lab-panel-wrap {
    display: block;
    width: 100vw;
    margin-left: calc(50% - 50vw);
    margin-right: calc(50% - 50vw);
  }
  .lab-panel-inner {
    display: block;
    max-width: 1100px;
    margin: 0 auto;
  }
  .lab-fullscreen {
    display: block;
    margin: 0 0 6px auto;
    font: inherit;
    padding: 6px 12px;
    border: 1px solid #8884;
    border-radius: 6px;
    background: none;
    color: inherit;
    cursor: pointer;
  }
  .lab-panel {
    display: block;
    width: 100%;
    height: 640px;
    border: 1px solid #8884;
    border-radius: 6px;
  }
  .lab-panel:fullscreen {
    width: 100vw;
    height: 100vh;
  }
</style>
<main>
{{.Body}}
</main>
`

const indexPage = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Glasshouse</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 16px/1.6 system-ui, sans-serif; max-width: 48rem; margin: 0 auto; padding: 0 16px; }
</style>
<main>
<h1>Glasshouse</h1>
<ul>
{{range .Articles}}<li><a href="{{.Slug}}.html">{{.Title}}</a></li>
{{end}}</ul>
</main>
`

type articleLink struct {
	Title string
	Slug  string
}

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
	idxTmpl := template.Must(template.New("index").Parse(indexPage))

	var links []articleLink
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		a, err := article.Build(src, known)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		slug := strings.TrimSuffix(filepath.Base(p), ".md")
		f, err := os.Create(filepath.Join(outDir, slug+".html"))
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
		links = append(links, articleLink{Title: a.Title, Slug: slug})
	}

	f, err := os.Create(filepath.Join(outDir, "index.html"))
	if err != nil {
		return err
	}
	err = idxTmpl.Execute(f, map[string]any{"Articles": links})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
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
