package ipc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"code.sli.ke/go/vega/packages/common"
)

// fakeBackend records commands and serves canned views so the test exercises the
// server/client transport, encoding, auth, and SSE — not real upload behavior.
type fakeBackend struct {
	mu       sync.Mutex
	views    map[common.UploadID]UploadView
	commands []string
}

func (f *fakeBackend) Enqueue(_ context.Context, req EnqueueRequest) (common.UploadID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := common.UploadID("up_" + filepath.Base(req.Path))
	f.views[id] = UploadView{ID: string(id), Filename: filepath.Base(req.Path), Status: "ready"}
	f.commands = append(f.commands, "enqueue")
	return id, nil
}

func (f *fakeBackend) List(context.Context) ([]UploadView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]UploadView, 0, len(f.views))
	for _, v := range f.views {
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeBackend) Get(_ context.Context, id common.UploadID) (UploadView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.views[id]
	if !ok {
		return UploadView{}, common.ErrNotFound
	}
	return v, nil
}

func (f *fakeBackend) record(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, name)
	return nil
}

func (f *fakeBackend) Pause(_ context.Context, _ common.UploadID) error  { return f.record("pause") }
func (f *fakeBackend) Resume(_ context.Context, _ common.UploadID) error { return f.record("resume") }
func (f *fakeBackend) Cancel(_ context.Context, _ common.UploadID) error { return f.record("cancel") }
func (f *fakeBackend) Metrics(context.Context) map[string]int64          { return map[string]int64{"x": 7} }

// serve starts a server on a temp socket and returns a client plus the bus.
func serve(t *testing.T) (*Client, *fakeBackend, *common.EventBus) {
	t.Helper()
	be := &fakeBackend{views: map[common.UploadID]UploadView{}}
	bus := common.NewEventBus()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewServer(be, bus, "secret-token", log)
	socket := filepath.Join(t.TempDir(), "d.sock")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx, socket) }()
	t.Cleanup(func() { cancel(); <-done })

	// Wait for the socket to accept connections.
	c := NewClient(socket, "secret-token")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := c.List(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not come up")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return c, be, bus
}

func TestRoundTrip(t *testing.T) {
	c, be, _ := serve(t)
	ctx := context.Background()

	id, err := c.Enqueue(ctx, EnqueueRequest{Path: "/data/movie.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "up_movie.mp4" {
		t.Fatalf("id = %s", id)
	}

	got, err := c.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Filename != "movie.mp4" {
		t.Fatalf("filename = %s", got.Filename)
	}

	list, err := c.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d", len(list))
	}

	for _, fn := range []func(context.Context, common.UploadID) error{c.Pause, c.Resume, c.Cancel} {
		if err := fn(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	be.mu.Lock()
	cmds := append([]string(nil), be.commands...)
	be.mu.Unlock()
	want := []string{"enqueue", "pause", "resume", "cancel"}
	if len(cmds) != len(want) {
		t.Fatalf("commands = %v, want %v", cmds, want)
	}

	m, err := c.Metrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m["x"] != 7 {
		t.Fatalf("metrics = %v", m)
	}
}

func TestGetNotFound(t *testing.T) {
	c, _, _ := serve(t)
	_, err := c.Get(context.Background(), "nope")
	if !errors.Is(err, common.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestAuthRejectsBadToken(t *testing.T) {
	c, _, _ := serve(t)
	// A client with the wrong token must be rejected.
	bad := NewClient(c.socket, "wrong-token")
	if _, err := bad.List(context.Background()); err == nil {
		t.Fatal("expected auth failure with wrong token")
	}
}

func TestEventStream(t *testing.T) {
	c, _, bus := serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Publish after subscribing; give the SSE subscription a moment to attach.
	time.Sleep(50 * time.Millisecond)
	bus.Publish(common.Event{Kind: "progress", UploadID: "up_1", At: 100, Fields: map[string]any{"done": float64(42)}})

	select {
	case ev := <-events:
		if ev.Kind != "progress" || ev.UploadID != "up_1" {
			t.Fatalf("event = %+v", ev)
		}
		if ev.Fields["done"] != float64(42) {
			t.Fatalf("fields = %v", ev.Fields)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event received")
	}
}
