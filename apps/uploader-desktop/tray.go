package main

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/icons"
)

// terminal upload statuses — everything else is "in flight".
var terminalStatus = map[string]bool{
	"completed": true,
	"failed":    true,
	"canceled":  true,
	"paused":    true,
}

// setupTray adds the menu-bar / system-tray item so the controller lives on
// after the window is closed: the engine daemon runs regardless, and the tray
// keeps a way back to the window plus at-a-glance progress and quick actions.
func setupTray(app *application.App, win *application.WebviewWindow, svc *UploadService) {
	tray := app.SystemTray.New()
	// macOS wants a monochrome template icon it tints for light/dark menu bars;
	// Windows takes a normal coloured icon.
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(icons.SystrayMacTemplate)
	} else {
		tray.SetIcon(icons.DefaultWindowsIcon)
	}
	tray.SetTooltip("Slike Uploader")

	menu := application.NewMenu()
	menu.Add("Open Slike Uploader").OnClick(func(*application.Context) { showWindow(win) })
	menu.AddSeparator()
	menu.Add("Pause all").OnClick(func(*application.Context) { setAllPaused(svc, true) })
	menu.Add("Resume all").OnClick(func(*application.Context) { setAllPaused(svc, false) })
	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(*application.Context) { app.Quit() })
	tray.SetMenu(menu)

	// Left-click reopens the window; right-click opens the menu (Wails default).
	tray.OnClick(func() { showWindow(win) })

	go trayStatusLoop(tray, svc)
}

// showWindow brings the (possibly hidden or minimised) window back to the front.
func showWindow(win *application.WebviewWindow) {
	win.Restore()
	win.Show()
	win.Focus()
}

// hideOnClose keeps the app running in the tray when the window is closed
// instead of quitting: the daemon is separate, so the controller should simply
// step out of the way and stay reachable from the tray.
func hideOnClose(win *application.WebviewWindow) {
	win.OnWindowEvent(events.Common.WindowClosing, func(e *application.WindowEvent) {
		e.Cancel()
		win.Hide()
	})
}

// setAllPaused pauses or resumes every in-flight upload in one action.
func setAllPaused(svc *UploadService, pause bool) {
	ctx := context.Background()
	list, err := svc.List(ctx)
	if err != nil {
		return
	}
	for _, u := range list {
		if terminalStatus[u.Status] {
			continue
		}
		if pause {
			_ = svc.Pause(ctx, u.ID)
		} else if u.Status == "paused" {
			_ = svc.Resume(ctx, u.ID)
		}
	}
}

// trayStatusLoop keeps the tooltip (and, on macOS, the menu-bar label) showing
// live activity. It polls the daemon rather than following the event stream so
// the tray stays correct even if a stray event is missed.
func trayStatusLoop(tray *application.SystemTray, svc *UploadService) {
	for {
		summarise(tray, svc)
		time.Sleep(2 * time.Second)
	}
}

func summarise(tray *application.SystemTray, svc *UploadService) {
	list, err := svc.List(context.Background())
	if err != nil {
		tray.SetLabel("")
		tray.SetTooltip("Slike Uploader — engine not running")
		return
	}
	var active int
	var bps int64
	for _, u := range list {
		if !terminalStatus[u.Status] {
			active++
			bps += u.CurBPS
		}
	}
	if active == 0 {
		tray.SetLabel("") // idle: icon only
		tray.SetTooltip("Slike Uploader — idle")
		return
	}
	tray.SetLabel(fmt.Sprintf("%d ↑", active))
	tray.SetTooltip(fmt.Sprintf("Slike Uploader — %d uploading · %s", active, humanBps(bps)))
}

func humanBps(n int64) string {
	if n <= 0 {
		return "0 B/s"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B/s", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n) / unit
	i := 0
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s/s", v, units[i])
}
