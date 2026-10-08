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

	// The two sides of the marker are rendered separately, and the trusted
	// panelHTML is appended between them as a literal string. goldmark's
	// default (no WithUnsafe) renderer drops every raw HTML block alike —
	// it cannot tell panelHTML apart from an attacker's <script> tag, so
	// running the whole body through one Convert call would drop both.
	before, after, _ := strings.Cut(body, panelMarker)
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(before), &buf); err != nil {
		return Article{}, err
	}
	buf.WriteString(panelHTML)
	if err := goldmark.Convert([]byte(after), &buf); err != nil {
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
