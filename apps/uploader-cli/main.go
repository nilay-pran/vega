// Command uploader-cli drives the upload engine from the terminal. It is the
// headless controller for the engine until the Wails desktop UI (which needs the
// `wails` toolchain) is built in a later phase; both are thin controllers over
// the same engine.
//
// Modes:
//
//	uploader-cli [flags] <file>     enqueue <file> and upload it now
//	uploader-cli -enqueue-only <f>  add <file> to the persistent queue, do not run
//	uploader-cli -serve             recover interrupted uploads and drain the queue
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/database"
	"code.sli.ke/go/vega/packages/logger"
	"code.sli.ke/go/vega/packages/manifests"
	"code.sli.ke/go/vega/packages/resumable"
	"code.sli.ke/go/vega/packages/storage"
	"code.sli.ke/go/vega/packages/telemetry"
	"code.sli.ke/go/vega/packages/transport"
	"code.sli.ke/go/vega/packages/uploader"
)

type options struct {
	mode        string // http | slkt
	server      string
	insecure    bool
	token       string
	dbPath      string
	user        string
	email       string
	workers     int
	concurrent  int
	level       string
	enqueueOnly bool
	serve       bool
}

func main() {

	var o options
	flag.StringVar(&o.mode, "transport", "auto", "transport: auto (probe & pick) | h3 (QUIC) | slkt (UDP+TCP) | http (server-in-path) | presigned (direct-to-Spaces)")
	flag.StringVar(&o.server, "server", "https://localhost:443", "upload-server base URL; HTTPS and SLKT share its host:port")
	flag.BoolVar(&o.insecure, "insecure", false, "skip TLS verification (self-signed dev server)")
	flag.StringVar(&o.token, "token", "dev-token", "bearer token")
	flag.StringVar(&o.dbPath, "db", "uploader.db", "SQLite state file")
	flag.StringVar(&o.user, "user", "dev-user", "CMS user id")
	flag.StringVar(&o.email, "email", "dev@example.com", "user email")
	flag.IntVar(&o.workers, "workers", 4, "concurrent chunk uploads per file")
	flag.IntVar(&o.concurrent, "concurrent", 40, "max simultaneous uploads (serve mode)")
	flag.StringVar(&o.level, "log", "info", "log level")
	flag.BoolVar(&o.enqueueOnly, "enqueue-only", false, "add to the queue without uploading")
	flag.BoolVar(&o.serve, "serve", false, "recover and drain the upload queue")
	flag.Parse()

	if err := run(o, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(o options, args []string) error {
	ctx := context.Background()
	log := logger.New(os.Stdout, logger.ParseLevel(o.level))

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
	metrics := telemetry.NewMetrics()
	eng := uploader.New(store, objects, log, uploader.Config{Workers: o.workers, Metrics: metrics})

	if o.serve {
		recovered, err := resumable.Recover(ctx, store)
		if err != nil {
			return err
		}
		ids, err := uploader.NewManager(store, eng, o.concurrent).RunQueue(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("recovered %d, processed %d, metrics %v\n", recovered, len(ids), metrics.Snapshot())
		return nil
	}

	if len(args) != 1 {
		return fmt.Errorf("usage: uploader-cli [flags] <file>  (or -serve)")
	}
	id, err := eng.Enqueue(ctx, uploader.EnqueueReq{User: common.UserID(o.user), Email: o.email, Path: args[0]})
	if err != nil {
		return err
	}
	fmt.Println("upload:", id)
	if o.enqueueOnly {
		return nil
	}
	if err := eng.Run(ctx, id); err != nil {
		return err
	}
	fmt.Println("completed:", id)
	return nil
}

// chooseTransport resolves -transport into a store. "auto" runs the full
// waterfall — periodic reachability probing plus realtime circuit-breaker
// failover across h3/http/slkt — for the duration of ctx; see
// transport.BuildAuto. In -serve mode ctx lives for the whole process, same as
// the daemon; for a single-file upload it lives only for this run.
func chooseTransport(ctx context.Context, o options, log *slog.Logger) (storage.ObjectStore, error) {
	return transport.Choose(ctx, o.mode, transport.AutoConfig{
		Server:   o.server,
		Token:    o.token,
		Insecure: o.insecure,
	}, log)
}
