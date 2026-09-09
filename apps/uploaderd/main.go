// Command uploaderd is the upload daemon: the long-lived engine that the desktop
// UI and CLI drive but never contain. It survives UI close, crash, and logout —
// on start it recovers interrupted uploads and keeps the queue moving, exposing
// pause/resume/cancel and a live event stream over a per-user Unix socket.
//
// The UI is a thin controller: it connects to the socket, lists uploads, and
// issues commands. Kill the UI and uploads keep running; relaunch it and it
// reattaches to the same daemon.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/database"
	"code.sli.ke/go/vega/packages/ipc"
	"code.sli.ke/go/vega/packages/logger"
	"code.sli.ke/go/vega/packages/manifests"
	"code.sli.ke/go/vega/packages/resumable"
	"code.sli.ke/go/vega/packages/storage"
	"code.sli.ke/go/vega/packages/telemetry"
	"code.sli.ke/go/vega/packages/transport"
	"code.sli.ke/go/vega/packages/uploader"
)

type options struct {
	mode       string
	server     string
	insecure   bool
	token      string
	dbPath     string
	user       string
	email      string
	workers    int
	concurrent int
	level      string
	socket     string
}

func main() {
	// Flag defaults come from the environment so one mechanism configures the
	// daemon however it is launched: the desktop app spawns it as a child (env
	// inherited), launchd/registry set the env on the installed service, and a
	// dev shell exports them. Explicit flags still override.
	var o options
	flag.StringVar(&o.mode, "transport", envOr("SLIKE_TRANSPORT", "auto"), "transport: auto | h3 | slkt | http | presigned")
	flag.StringVar(&o.server, "server", envOr("SLIKE_SERVER", "https://localhost:443"), "upload-server base URL; HTTPS and SLKT share its host:port")
	flag.BoolVar(&o.insecure, "insecure", os.Getenv("SLIKE_INSECURE") != "", "skip TLS verification (self-signed dev server)")
	flag.StringVar(&o.token, "token", envOr("SLIKE_TOKEN", "dev-token"), "bearer token for the upload server")
	flag.StringVar(&o.dbPath, "db", "", "SQLite state file (default: per-user state dir)")
	flag.StringVar(&o.user, "user", "dev-user", "CMS user id this daemon serves")
	flag.StringVar(&o.email, "email", "dev@example.com", "user email")
	flag.IntVar(&o.workers, "workers", 4, "concurrent chunk uploads per file")
	flag.IntVar(&o.concurrent, "concurrent", 40, "max simultaneous uploads")
	flag.StringVar(&o.level, "log", "info", "log level")
	flag.StringVar(&o.socket, "socket", "", "control socket path (default: per-user config dir)")
	flag.Parse()

	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// envOr returns the environment value for key, or fallback when it is unset.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func run(o options) error {
	// Cancel on SIGINT/SIGTERM so both the socket server and the supervisor
	// unwind cleanly, leaving every upload resumable.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log := logger.New(os.Stdout, logger.ParseLevel(o.level))

	// A daemon launched by launchd or the Windows session has no reliable working
	// directory, so keep the state file beside the socket and token by default.
	if o.dbPath == "" {
		p, err := ipc.StatePath("uploader.db")
		if err != nil {
			return err
		}
		o.dbPath = p
	}

	db, err := database.Open(ctx, o.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		return err
	}

	store := manifests.NewStore(db)
	objects, err := chooseTransport(ctx, o, log)
	if err != nil {
		return err
	}

	bus := common.NewEventBus()
	metrics := telemetry.NewMetrics()
	eng := uploader.New(store, objects, log, uploader.Config{Workers: o.workers, Bus: bus, Metrics: metrics})

	// Recover anything a previous run left mid-flight, then let the supervisor
	// pick it back up alongside newly enqueued work.
	recovered, err := resumable.Recover(ctx, store)
	if err != nil {
		return err
	}
	log.Info("recovered interrupted uploads", "count", recovered)

	sup := uploader.NewSupervisor(ctx, store, eng, log, o.concurrent)
	sup.Kick()

	socket := o.socket
	if socket == "" {
		if socket, err = ipc.DefaultSocketPath(); err != nil {
			return err
		}
	}
	ipcToken, err := ipc.LoadOrCreateToken()
	if err != nil {
		return err
	}

	be := &backend{store: store, engine: eng, sup: sup, metrics: metrics, user: common.UserID(o.user), email: o.email}
	srv := ipc.NewServer(be, bus, ipcToken, log)

	log.Info("uploaderd listening", "socket", socket, "user", o.user)
	err = srv.Serve(ctx, socket)
	sup.Stop() // wait for in-flight runs to unwind before the process exits
	log.Info("uploaderd stopped", "metrics", metrics.Snapshot())
	return err
}

// chooseTransport resolves -transport into a store. "auto" runs the full
// waterfall — periodic reachability probing plus realtime circuit-breaker
// failover across h3/http/slkt — for the daemon's entire lifetime, since ctx
// here is the process's own signal-bound context; see transport.BuildAuto.
func chooseTransport(ctx context.Context, o options, log *slog.Logger) (storage.ObjectStore, error) {
	return transport.Choose(ctx, o.mode, transport.AutoConfig{
		Server:   o.server,
		Token:    o.token,
		Insecure: o.insecure,
	}, log)
}
