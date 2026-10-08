// Package web embeds the built panel (web/dist, produced by `pnpm build`).
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
