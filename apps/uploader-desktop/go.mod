module code.sli.ke/go/vega/apps/uploader-desktop

go 1.25.1

// The desktop app is a nested module in this monorepo; point the shared
// packages (ipc, common) at the repo root rather than a published version.
require code.sli.ke/go/vega v0.0.0

replace code.sli.ke/go/vega => ../../

require github.com/wailsapp/wails/v3 v3.0.0-alpha2.117

require (
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	golang.org/x/sys v0.47.0 // indirect
)
