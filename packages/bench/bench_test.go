package bench

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"testing"
	"time"

	uploadserver "code.sli.ke/go/vega/apps/upload-server"
	"code.sli.ke/go/vega/packages/auth"
	"code.sli.ke/go/vega/packages/storage"
	"code.sli.ke/go/vega/packages/transport"
	"code.sli.ke/go/vega/packages/transport/slkt"

	"github.com/quic-go/quic-go/http3"
)

const (
	partSize = 32 << 20 // 32 MiB part — one S3 part, both transports buffer it
	devToken = "bench-token"
)

// scenario is one emulated link.
type scenario struct {
	loss  float64
	rttMS int
}

// TestTransportComparison uploads a fixed part over SLKT and over HTTP/3 across
// a matrix of loss/RTT link conditions and prints throughput for each. Run with:
//
//	go test ./packages/bench -run TestTransportComparison -v -timeout 300s
func TestTransportComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("link benchmark skipped in -short")
	}
	scenarios := []scenario{
		{loss: 0.00, rttMS: 0},
		{loss: 0.00, rttMS: 50},
		{loss: 0.01, rttMS: 50},
		{loss: 0.05, rttMS: 50},
		{loss: 0.01, rttMS: 200},
		{loss: 0.05, rttMS: 200},
	}
	data := make([]byte, partSize)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}

	fmt.Printf("\npart=%d MiB\n", partSize>>20)
	fmt.Printf("%-6s %-7s | %-22s | %-22s\n", "loss", "rtt", "SLKT", "HTTP/3 (QUIC)")
	fmt.Println("-------------------------------------------------------------------")
	for _, s := range scenarios {
		slktRes := runSLKT(t, data, s)
		h3Res := runHTTP3(t, data, s)
		fmt.Printf("%-5.0f%% %-5dms | %-22s | %-22s\n",
			s.loss*100, s.rttMS, slktRes, h3Res)
	}
	fmt.Println()
}

// result formats a throughput measurement or a failure. A deadline is not a
// crash: it means the controller drove throughput below partSize/ctx, so we
// report it as a collapse with the throughput ceiling the timeout implies.
func result(d time.Duration, err error) string {
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			ceil := (float64(partSize) / (1 << 20)) / d.Seconds()
			return fmt.Sprintf("collapse (<%.2f MiB/s)", ceil)
		}
		return "FAILED: " + short(err.Error())
	}
	mbps := (float64(partSize) / (1 << 20)) / d.Seconds()
	return fmt.Sprintf("%6.1f MiB/s (%dms)", mbps, d.Milliseconds())
}

func short(s string) string {
	if len(s) > 60 {
		return s[:60]
	}
	return s
}

func oneWay(rttMS int) time.Duration { return time.Duration(rttMS/2) * time.Millisecond }

// runSLKT stands up a SLKT server, routes its UDP data path through the relay,
// and times one part upload. SLKT's TCP control channel is dialed directly (not
// through the relay), so its NAK feedback sees no loss or latency — this
// flatters SLKT relative to H3, whose ACKs traverse the emulated link. See the
// report's caveats.
func runSLKT(t *testing.T, data []byte, s scenario) string {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	objects := storage.NewMemStore()

	// Server's real UDP data socket + control TCP listener.
	serverUDP, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	clientUDPAddr, stop, err := startRelay(serverUDP.LocalAddr(), s.loss, oneWay(s.rttMS), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// Control TCP must share the host:port the client dials for UDP (the relay's
	// client-facing address), so bind the TCP listener on that same port.
	host, port, _ := net.SplitHostPort(clientUDPAddr)
	tcpLn, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go slkt.NewServer(objects, log).Serve(ctx, tcpLn, serverUDP)

	client := slkt.NewClient(clientUDPAddr)
	return result(timeUpload(client, data))
}

// runHTTP3 stands up an HTTP/3 server, routes its QUIC socket through the relay,
// and times one part upload through the ordinary multipart contract. Both the
// data and the ACK path traverse the emulated link.
func runHTTP3(t *testing.T, data []byte, s scenario) string {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	objects := storage.NewMemStore()
	verifier := auth.DevVerifier{Token: devToken, Subject: "bench"}
	handler := uploadserver.New(objects, verifier, nil, log).Handler()

	serverUDP, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	clientAddr, stop, err := startRelay(serverUDP.LocalAddr(), s.loss, oneWay(s.rttMS), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	h3 := &http3.Server{Handler: handler, TLSConfig: benchTLS(t)}
	go h3.Serve(serverUDP)
	defer h3.Close()

	store := transport.NewHTTP3Store("https://"+clientAddr, devToken, true)
	return result(timeUpload(store, data))
}

// timeUpload runs the multipart contract (init → upload one part) against any
// ObjectStore and returns how long the part transfer took.
func timeUpload(store storage.ObjectStore, data []byte) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	key := "bench/part"
	id, err := store.InitMultipart(ctx, key)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	_, err = store.UploadPart(ctx, key, id, 1, bytes.NewReader(data), int64(len(data)))
	return time.Since(start), err
}

// benchTLS mints an in-memory self-signed certificate for the HTTP/3 server.
func benchTLS(t *testing.T) *tls.Config {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "bench"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}
