// Package web embeds the built UI (web/dist) into the binary.
package web

import "embed"

//go:embed all:dist
var FS embed.FS
