package main

import (
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"code.sli.ke/go/vega/packages/ipc"
)

// ensureDaemon guarantees the uploaderd engine is running before the UI starts
// driving it, so one launch of the app brings up both — no separate manual
// start. It is a no-op when the daemon is already up (installed builds autostart
// it via launchd/registry, or a previous run left it running), so it never
// spawns a duplicate. When it does spawn, the daemon is detached into its own
// session/process group: uploads must outlive this window, so the engine cannot
// be a child that dies when the UI quits.
func ensureDaemon(socket string) {
	if daemonAlive(socket) {
		return
	}
	cmd := daemonCommand()
	if cmd == nil {
		log.Print("uploaderd: engine not found and no dev repo to run it from; " +
			"start it with scripts/dev-stack.sh")
		return
	}
	detach(cmd) // platform-specific: new session (unix) / detached group (windows)
	cmd.Stdout, cmd.Stderr = daemonLog(), daemonLog()
	if err := cmd.Start(); err != nil {
		log.Printf("uploaderd: failed to start engine: %v", err)
		return
	}
	_ = cmd.Process.Release() // fully hand off; we do not wait on it

	// Give the freshly spawned daemon a moment to bind its socket so the first
	// UI calls land instead of racing the listener.
	waitForSocket(socket, 3*time.Second)
}

// daemonAlive reports whether something is already listening on the control
// socket. A successful dial is enough — the UI's authenticated calls follow.
func daemonAlive(socket string) bool {
	conn, err := net.DialTimeout("unix", socket, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func waitForSocket(socket string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if daemonAlive(socket) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// daemonCommand builds the command that runs the engine, or nil if neither a
// bundled binary nor a dev checkout is available. It prefers the daemon binary
// bundled beside the app (installed builds); in development it runs the engine
// with `go run` from the repo-root module, whose go.sum has the engine's full
// dependency set (the UI's nested module does not). The engine's server address
// is deployment config: the daemon reads SLIKE_SERVER (and friends) from the
// environment, which this detached child inherits from the app, so nothing is
// hardcoded here.
func daemonCommand() *exec.Cmd {
	if bin := bundledDaemon(); bin != "" {
		return exec.Command(bin)
	}
	root := repoRoot()
	if root == "" {
		return nil
	}
	cmd := exec.Command("go", "run", "./apps/uploaderd")
	cmd.Dir = root
	return cmd
}

// repoRoot walks up from the working directory to the root module (the one whose
// go.mod declares the repo module path), returning "" outside a dev checkout.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if strings.HasPrefix(string(b), "module code.sli.ke/go/vega\n") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// bundledDaemon returns the path to the uploaderd binary shipped with an
// installed app, or "" if none is found (development). The daemon sits beside
// the executable on Windows/Linux and under Contents/Resources on macOS.
func bundledDaemon() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	candidates := []string{filepath.Join(dir, daemonBinName())}
	if runtime.GOOS == "darwin" {
		// .app/Contents/MacOS/<exe> -> .app/Contents/Resources/uploaderd
		candidates = append(candidates, filepath.Join(dir, "..", "Resources", "uploaderd"))
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}

func daemonBinName() string {
	if runtime.GOOS == "windows" {
		return "uploaderd.exe"
	}
	return "uploaderd"
}

// daemonLog opens the engine's log file, falling back to discarding output so a
// log-path problem never blocks startup.
func daemonLog() *os.File {
	if path, err := ipc.StatePath("uploaderd.log"); err == nil {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			return f
		}
	}
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	return devnull
}
