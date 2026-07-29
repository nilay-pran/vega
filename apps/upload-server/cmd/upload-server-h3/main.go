// Command upload-server-h3 is the QUIC/HTTP-3 variant of the upload server, kept
// beside the SLKT-multiplexing upload-server for comparison. It shows what the
// listener wiring becomes once the custom UDP protocol is replaced by HTTP/3:
//
//   - TCP :443 serves HTTPS (HTTP/1.1 + HTTP/2), the failover path.
//   - UDP :443 serves HTTP/3 (QUIC), the fast path.
//
// The two live at different transport layers (TCP vs UDP), so the kernel splits
// them by protocol number. The first-byte demux the SLKT build needs to tell
// TLS-on-TCP apart from SLKT-control-on-TCP disappears entirely: there is no
// mux.go, no peek, no channel-backed listeners. Both servers share one
// http.Handler and one certificate; QUIC brings TLS 1.3, congestion control,
// stream multiplexing, and connection migration with no bespoke code.
package main

import (
	"crypto/tls"
	"flag"
	"net/http"
	"os"

	uploadserver "code.sli.ke/go/vega/apps/upload-server"
	"code.sli.ke/go/vega/packages/auth"
	"code.sli.ke/go/vega/packages/logger"
	"code.sli.ke/go/vega/packages/storage"

	"github.com/quic-go/quic-go/http3"
)

func main() {
	addr := flag.String("addr", ":443", "listen address for HTTPS (TCP) + HTTP/3 (UDP)")
	backend := flag.String("backend", "mem", "object store backend: mem | s3")
	token := flag.String("dev-token", "dev-token", "dev bearer token")
	subject := flag.String("dev-subject", "dev-user", "subject the dev token maps to")
	tlsCert := flag.String("tls-cert", "", "TLS certificate file (production); self-signed dev cert if unset")
	tlsKey := flag.String("tls-key", "", "TLS key file")
	level := flag.String("log", "info", "log level")
	flag.Parse()

	log := logger.New(os.Stdout, logger.ParseLevel(*level))

	objects, err := buildStore(*backend)
	if err != nil {
		log.Error("storage init failed", "err", err)
		os.Exit(1)
	}
	verifier := auth.DevVerifier{Token: *token, Subject: *subject}

	cert, err := loadOrDevCert(*tlsCert, *tlsKey)
	if err != nil {
		log.Error("tls setup failed", "err", err)
		os.Exit(1)
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}

	handler := uploadserver.New(objects, verifier, uploadserver.NewMemOwners(), log).Handler()

	// HTTP/3 (QUIC) on UDP — the fast path. No demux: UDP is its own L4.
	h3 := &http3.Server{Addr: *addr, Handler: handler, TLSConfig: tlsCfg}
	go func() {
		log.Info("http/3 listening (QUIC/UDP)", "addr", *addr, "backend", *backend)
		if err := h3.ListenAndServe(); err != nil {
			log.Error("http/3 server exited", "err", err)
			os.Exit(1)
		}
	}()

	// HTTPS on TCP — the failover path. Advertises h3 via Alt-Svc for clients
	// that can upgrade. Still no demux: TLS-on-TCP is the only thing here.
	httpsCfg := tlsCfg.Clone()
	httpsCfg.NextProtos = []string{"h2", "http/1.1"}
	srv := &http.Server{
		Addr:      *addr,
		TLSConfig: httpsCfg,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = h3.SetQUICHeaders(w.Header()) // Alt-Svc: h3=":443"
			handler.ServeHTTP(w, r)
		}),
	}
	log.Info("https listening (TCP)", "addr", *addr, "backend", *backend)
	if err := srv.ListenAndServeTLS("", ""); err != nil {
		log.Error("https server exited", "err", err)
		os.Exit(1)
	}
}

func buildStore(backend string) (storage.ObjectStore, error) {
	if backend == "s3" {
		return storage.NewS3Store(storage.S3Config{
			Endpoint:  os.Getenv("SPACES_ENDPOINT"),
			Region:    os.Getenv("SPACES_REGION"),
			AccessKey: os.Getenv("SPACES_KEY"),
			SecretKey: os.Getenv("SPACES_SECRET"),
			Bucket:    os.Getenv("SPACES_BUCKET"),
			UseSSL:    os.Getenv("SPACES_SSL") != "false",
		})
	}
	return storage.NewMemStore(), nil
}
