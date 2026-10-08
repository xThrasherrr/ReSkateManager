//go:build windows

package main

import (
	"context"
	_ "embed"
	"os/exec"

	"fyne.io/systray"
)

//go:embed icon.ico
var trayIcon []byte

func openBrowser(url string) {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// runTray shows the tray icon until the manager stops or the host picks Quit.
func runTray(ctx context.Context, stop context.CancelFunc, url string) {
	go func() {
		<-ctx.Done()
		systray.Quit()
	}()
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("ReSkateManager")
		systray.SetTooltip("ReSkateManager - " + url)
		open := systray.AddMenuItem("Open panel", "Open the web panel")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Stop servers and quit", "Stop every server and exit")
		systray.SetOnTapped(func() { openBrowser(url) })
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					openBrowser(url)
				case <-quit.ClickedCh:
					stop()
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	}, func() {})
}
