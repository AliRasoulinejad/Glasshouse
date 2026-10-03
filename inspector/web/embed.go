// Package web embeds the viewer shell so the Inspector ships as one binary.
package web

import "embed"

// Static is the viewer shell: HTML, CSS and ES modules, no build step.
//
//go:embed static
var Static embed.FS
