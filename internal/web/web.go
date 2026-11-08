// Package web embeds the user interface into the executable.
package web

import "embed"

//go:embed static
var Files embed.FS
