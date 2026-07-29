// Command uploader-desktop is the Slike Uploader desktop UI: a thin Wails
// controller over the uploaderd daemon. It renders the upload list, forwards
// user actions (enqueue, pause, resume, cancel) to the daemon over its Unix
// socket, and mirrors the daemon's live event stream into the frontend. It owns
// no upload state — the engine runs in the daemon and outlives this window.
package main

import (
	"context"
	"embed"
	"log"
	"time"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/ipc"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	// Registering the event type gives the frontend a strongly typed listener.
	application.RegisterEvent[common.Event]("upload.event")
}

func main() {
	// Bring the engine up first so a single launch of the app starts both the UI
	// and the daemon it drives. No-op if the daemon is already running.
	if socket, err := ipc.DefaultSocketPath(); err == nil {
		ensureDaemon(socket)
	}

	svc, err := newUploadService()
	if err != nil {
		log.Fatal(err)
	}

	app := application.New(application.Options{
		Name:        "Slike Uploader",
		Description: "Desktop uploader for the Slike Video CMS",
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			// The app lives in the tray; closing the window hides it rather than
			// quitting, so it must survive the last window closing.
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Slike Uploader",
		Width:  1000,
		Height: 680,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
			// The UI is styled for a dark backdrop; without this the vibrancy
			// material follows the OS appearance, so light-mode systems render
			// dark text on a light pane and it becomes unreadable.
			Appearance: application.NSAppearanceNameDarkAqua,
		},
		BackgroundColour: application.NewRGB(15, 17, 26),
		URL:              "/",
	})

	// Close hides the window instead of quitting; the tray keeps the app around.
	hideOnClose(win)
	setupTray(app, win, svc)

	// Mirror the daemon's SSE stream into frontend events. The daemon may be
	// down or restart at any time, so reconnect forever with a short backoff.
	go mirrorEvents(app, svc)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func mirrorEvents(app *application.App, svc *UploadService) {
	for {
		ch, err := svc.client.Events(context.Background())
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		for ev := range ch {
			app.Event.Emit("upload.event", ev)
		}
		// Stream ended (daemon closed or died); loop and reconnect.
		time.Sleep(time.Second)
	}
}
