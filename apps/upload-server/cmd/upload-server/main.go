// Command upload-server runs the upload server. It only wires adapters together;
// all logic lives in packages and the uploadserver package.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"

	uploadserver "code.sli.ke/go/vega/apps/upload-server"
	"code.sli.ke/go/vega/packages/auth"
	"code.sli.ke/go/vega/packages/logger"
	"code.sli.ke/go/vega/packages/storage"
	"code.sli.ke/go/vega/packages/transport/slkt"
)

func main() {
	// One listen address serves everything: the JSON API, multipart uploads, and
	// SLKT — HTTPS and SLKT control multiplexed on the TCP socket, SLKT bulk data
	// on the UDP socket of the same port. 443 is the default so clients need no
	// custom port through a firewall.
	addr := flag.String("addr", ":443", "listen address for HTTPS + SLKT (one port serves API, uploads, and SLKT)")
	backend := flag.String("backend", "mem", "object store backend: mem | s3")
	token := flag.String("dev-token", "dev-token", "dev bearer token (used when -jwt-secret is empty)")
	subject := flag.String("dev-subject", "dev-user", "subject the dev token maps to")
	jwtSecret := flag.String("jwt-secret", "", "HS256 secret; if set, validate real JWTs instead of the dev token")
	jwtAud := flag.String("jwt-aud", "", "required JWT audience")
	jwtIss := flag.String("jwt-iss", "", "required JWT issuer")
	tlsCert := flag.String("tls-cert", "", "TLS certificate file (production); a self-signed dev cert is used if unset")
	tlsKey := flag.String("tls-key", "", "TLS key file")
	level := flag.String("log", "info", "log level")
	flag.Parse()

	log := logger.New(os.Stdout, logger.ParseLevel(*level))

	objects, err := buildStore(*backend)
	if err != nil {
		log.Error("storage init failed", "err", err)
		os.Exit(1)
	}

	var verifier auth.Verifier
	if *jwtSecret != "" {
		verifier = auth.JWTVerifier{Secret: []byte(*jwtSecret), Audience: *jwtAud, Issuer: *jwtIss}
		log.Info("auth: JWT (HS256)")
	} else {
		verifier = auth.DevVerifier{Token: *token, Subject: *subject}
		log.Info("auth: dev token (insecure; set -jwt-secret for real JWTs)")
	}

	tlsCfg, err := buildTLS(*tlsCert, *tlsKey, log)
	if err != nil {
		log.Error("tls setup failed", "err", err)
		os.Exit(1)
	}

	base, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Error("tcp listen failed", "err", err)
		os.Exit(1)
	}
	udp, err := net.ListenPacket("udp", *addr)
	if err != nil {
		log.Error("udp listen failed", "err", err)
		os.Exit(1)
	}

	// Split the one TCP socket into two logical listeners: TLS/HTTPS and SLKT
	// control. SLKT data shares the UDP socket directly.
	httpLn := newMuxListener(base.Addr())
	slktLn := newMuxListener(base.Addr())
	go demux(base, httpLn, slktLn, log)

	go func() {
		if err := slkt.NewServer(objects, log).Serve(context.Background(), slktLn, udp); err != nil {
			log.Error("slkt server exited", "err", err)
		}
	}()

	srv := uploadserver.New(objects, verifier, uploadserver.NewMemOwners(), log)
	httpSrv := &http.Server{Handler: srv.Handler()}

	log.Info("upload-server listening (HTTPS + SLKT multiplexed)", "addr", *addr, "backend", *backend)
	if err := httpSrv.Serve(tls.NewListener(httpLn, tlsCfg)); err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
}

// buildTLS loads the production certificate, or mints an in-memory self-signed
// one for local runs so the port can still multiplex HTTPS with SLKT.
func buildTLS(certFile, keyFile string, log *slog.Logger) (*tls.Config, error) {
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		log.Info("tls: loaded certificate", "cert", certFile)
		return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
	}
	cert, err := devCertificate()
	if err != nil {
		return nil, err
	}
	log.Info("tls: self-signed dev certificate (insecure; pass -tls-cert/-tls-key for production)")
	return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
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
