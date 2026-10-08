package main

import (
	"io/fs"

	"github.com/xThrasherrr/ReSkateManager/web"
)

func staticFS() (fs.FS, error) { return fs.Sub(web.Dist, "dist") }
