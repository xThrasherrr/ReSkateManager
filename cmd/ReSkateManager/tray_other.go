//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
)

// Linux hosts run headless (systemd); a desktop session gets a browser tab at most.
func openBrowser(url string) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return
	}
	_ = exec.Command("xdg-open", url).Start()
}

func runTray(context.Context, context.CancelFunc, string) {}
