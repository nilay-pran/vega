package slkt

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/database"
	"code.sli.ke/go/vega/packages/manifests"
	"code.sli.ke/go/vega/packages/storage"
	"code.sli.ke/go/vega/packages/uploader"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// startServer binds TCP and UDP on the same loopback port and serves until stop.
func startServer(t *testing.T, objects storage.ObjectStore) (addr string, stop func()) {
	t.Helper()
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(tcpLn.Addr().String())
	udp, err := net.ListenPacket("udp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go NewServer(objects, discardLog()).Serve(ctx, tcpLn, udp)
	return tcpLn.Addr().String(), cancel
}

func TestSLKTObjectStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	mem := storage.NewMemStore()
	addr, stop := startServer(t, mem)
	defer stop()

	c := NewClient(addr)
	mp, err := c.InitMultipart(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 5000) // ~5 UDP datagrams
	rand.Read(payload)
	part, err := c.UploadPart(ctx, "k", mp, 1, bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}
	if err := c.CompleteMultipart(ctx, "k", mp, []storage.Part{{PartNumber: 1, ETag: part.ETag}}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	rc, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Fatal("object bytes differ")
	}
}

func TestSLKTRejectsUnsupportedVersion(t *testing.T) {
	mem := storage.NewMemStore()
	addr, stop := startServer(t, mem)
	defer stop()

	c := NewClient(addr)
	c.version = 99 // pretend to be a future, unsupported protocol version
	if _, err := c.InitMultipart(context.Background(), "k"); err == nil {
		t.Fatal("expected server to reject unsupported version")
	}
}

func TestSLKTRetransmitsLostPackets(t *testing.T) {
	ctx := context.Background()
	mem := storage.NewMemStore()
	addr, stop := startServer(t, mem)
	defer stop()

	c := NewClient(addr)
	// Drop the first datagram at each of a couple of offsets, exactly once, to
	// force the server to NAK and the client to retransmit.
	var mu sync.Mutex
	dropped := map[int64]bool{}
	c.dropPacket = func(off int64) bool {
		mu.Lock()
		defer mu.Unlock()
		if (off == 0 || off == dataPerPacket*3) && !dropped[off] {
			dropped[off] = true
			return true
		}
		return false
	}

	mp, _ := c.InitMultipart(ctx, "k")
	payload := make([]byte, dataPerPacket*10) // 10 datagrams
	rand.Read(payload)
	part, err := c.UploadPart(ctx, "k", mp, 1, bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("upload part with loss: %v", err)
	}
	if len(dropped) != 2 {
		t.Fatalf("expected 2 dropped packets, got %d", len(dropped))
	}
	_ = c.CompleteMultipart(ctx, "k", mp, []storage.Part{{PartNumber: 1, ETag: part.ETag}})

	rc, _ := c.Get(ctx, "k")
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Fatal("object bytes differ after retransmit")
	}
}

// Full stack: the real upload engine, over the UDP+TCP SLKT transport, into an
// object store — with packet loss injected.
func TestSLKTThroughEngine(t *testing.T) {
	ctx := context.Background()
	mem := storage.NewMemStore()
	addr, stop := startServer(t, mem)
	defer stop()

	content := make([]byte, 12<<20) // 12 MiB -> three 5/5/2 MiB parts
	rand.Read(content)
	src := filepath.Join(t.TempDir(), "movie.mov")
	if err := os.WriteFile(src, content, 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	client := NewClient(addr)
	var mu sync.Mutex
	once := false
	client.dropPacket = func(int64) bool { // drop exactly one datagram overall
		mu.Lock()
		defer mu.Unlock()
		if !once {
			once = true
			return true
		}
		return false
	}

	eng := uploader.New(manifests.NewStore(db), client, discardLog(), uploader.Config{Workers: 4})
	id, err := eng.Enqueue(ctx, uploader.EnqueueReq{User: "u1", Email: "u1@x.y", Path: src})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Run(ctx, id); err != nil {
		t.Fatalf("run over slkt: %v", err)
	}

	u, _ := manifests.NewStore(db).LoadUpload(ctx, id)
	if u.Status != common.StatusCompleted {
		t.Fatalf("status = %s, want completed", u.Status)
	}
	rc, _ := mem.Get(ctx, u.ObjectKey)
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if sha256.Sum256(got) != sha256.Sum256(content) {
		t.Fatal("assembled object differs from source")
	}
}
