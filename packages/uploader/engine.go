// Package uploader is the upload engine: it plans a file into chunks, uploads
// the missing ones concurrently through an ObjectStore, assembles them, and
// verifies the result end to end. It is resumable — Run picks up from whatever
// SQLite says is still missing — and transport-agnostic, since ObjectStore may
// be backed by the custom protocol (server-in-path), by direct-to-Spaces, or by
// an in-memory fake in tests.
package uploader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"sync"
	"time"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/manifests"
	"code.sli.ke/go/vega/packages/scheduler"
	"code.sli.ke/go/vega/packages/storage"
	"code.sli.ke/go/vega/packages/telemetry"
)

// Config tunes an Engine. Workers is the max chunks uploaded concurrently.
type Config struct {
	Workers  int
	Notifier Notifier
	Bus      *common.EventBus // optional: progress/lifecycle events for controllers
	Metrics  *telemetry.Metrics
}

type Engine struct {
	store    *manifests.Store
	objects  storage.ObjectStore
	log      *slog.Logger
	notifier Notifier
	bus      *common.EventBus
	metrics  *telemetry.Metrics
	workers  int
}

func New(store *manifests.Store, objects storage.ObjectStore, log *slog.Logger, cfg Config) *Engine {
	if cfg.Workers < 1 {
		cfg.Workers = 4
	}
	if cfg.Notifier == nil {
		cfg.Notifier = LogNotifier{Log: log}
	}
	return &Engine{
		store:    store,
		objects:  objects,
		log:      log,
		notifier: cfg.Notifier,
		bus:      cfg.Bus,
		metrics:  cfg.Metrics,
		workers:  cfg.Workers,
	}
}

// progress tracks bytes transferred for one run and derives live speed/ETA.
type progress struct {
	mu      sync.Mutex
	total   int64
	done    int64
	sampler *telemetry.Sampler
}

func (p *progress) advance(n int64, at time.Time) (done, curBPS, avgBPS, etaSec int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done += n
	p.sampler.Observe(p.done, at)
	return p.done, int64(p.sampler.CurrentBPS()), int64(p.sampler.AverageBPS()), int64(p.sampler.ETA(p.total - p.done).Seconds())
}

func (e *Engine) emit(kind string, id common.UploadID, fields map[string]any) {
	if e.bus != nil {
		e.bus.Publish(common.Event{Kind: kind, UploadID: id, At: time.Now().Unix(), Fields: fields})
	}
}

func (e *Engine) count(name string, delta int64) {
	if e.metrics != nil {
		e.metrics.Add(name, delta)
	}
}

// EnqueueReq describes a new upload.
type EnqueueReq struct {
	User      common.UserID
	Email     string
	Path      string
	CMSFileID string
	ObjectKey string // optional; defaults to uploads/<id>/<filename>
}

// Enqueue hashes and plans a file and persists it as Ready. It does no network
// I/O, so it is cheap and crash-safe: the plan is durable before any transfer.
func (e *Engine) Enqueue(ctx context.Context, req EnqueueReq) (common.UploadID, error) {
	info, err := os.Stat(req.Path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", req.Path, err)
	}
	sum, err := hashFile(req.Path)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", req.Path, err)
	}

	id := common.NewUploadID()
	size := info.Size()
	chunkSize := manifests.PlanChunkSize(size)
	chunks := manifests.PlanChunks(size, chunkSize)

	key := req.ObjectKey
	if key == "" {
		key = path.Join("uploads", string(id), info.Name())
	}
	u := &manifests.Upload{
		ID:         id,
		UserID:     req.User,
		CMSFileID:  req.CMSFileID,
		SourcePath: req.Path,
		Filename:   info.Name(),
		Size:       size,
		Status:     common.StatusReady,
		ObjectKey:  key,
		SHA256:     sum,
		ChunkSize:  chunkSize,
		ChunkCount: len(chunks),
	}
	if err := e.store.UpsertUser(ctx, req.User, req.Email); err != nil {
		return "", err
	}
	if err := e.store.CreateUpload(ctx, u, chunks); err != nil {
		return "", err
	}
	_ = e.store.AppendEvent(ctx, string(req.User), "upload.created", id, info.Name())
	e.count("uploads_enqueued", 1)
	e.emit("upload.created", id, map[string]any{"filename": info.Name(), "size": size, "chunks": len(chunks)})
	e.log.Info("enqueued", "upload", id, "size", size, "chunk_size", chunkSize, "chunks", len(chunks))
	return id, nil
}

// Run transfers every missing chunk, then assembles and verifies. It is safe to
// call repeatedly: on a fresh upload it does the whole thing; after an
// interruption it resumes from the missing set. Only a verified checksum match
// moves the upload to Completed.
func (e *Engine) Run(ctx context.Context, id common.UploadID) error {
	u, err := e.store.LoadUpload(ctx, id)
	if err != nil {
		return err
	}
	if u.Status == common.StatusCompleted {
		return nil
	}

	// Recovery: any chunk left inflight by a crash goes back to pending.
	if err := e.store.ResetInFlight(ctx, id); err != nil {
		return err
	}
	if err := e.store.SetStatus(ctx, id, common.StatusUploading, ""); err != nil {
		return err
	}

	if u.MultipartID == "" {
		mp, err := e.objects.InitMultipart(ctx, u.ObjectKey)
		if err != nil {
			return fmt.Errorf("init multipart: %w", err)
		}
		if err := e.store.SetMultipartID(ctx, id, mp); err != nil {
			return err
		}
		u.MultipartID = mp
	}

	missing, err := e.store.MissingChunks(ctx, id)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		done0, err := e.store.DoneBytes(ctx, id)
		if err != nil {
			return err
		}
		prog := &progress{total: u.Size, done: done0, sampler: telemetry.NewSampler(0.3)}

		f, err := os.Open(u.SourcePath)
		if err != nil {
			return fmt.Errorf("open source: %w", err)
		}
		defer f.Close()

		tasks := make([]scheduler.Task, len(missing))
		for i, c := range missing {
			tasks[i] = func(ctx context.Context) error { return e.uploadChunk(ctx, f, u, c, prog) }
		}
		if err := scheduler.Run(ctx, e.workers, tasks); err != nil {
			// Leave the upload resumable; the missing set shrank by whatever succeeded.
			return fmt.Errorf("transfer interrupted: %w", err)
		}
	}

	return e.finish(ctx, u)
}

func (e *Engine) uploadChunk(ctx context.Context, f *os.File, u *manifests.Upload, c manifests.Chunk, prog *progress) error {
	if err := e.store.MarkInFlight(ctx, u.ID, c.Index); err != nil {
		return err
	}
	section := io.NewSectionReader(f, c.Offset, c.Length)
	h := sha256.New()
	part, err := e.objects.UploadPart(ctx, u.ObjectKey, u.MultipartID, c.Index+1, io.TeeReader(section, h), c.Length)
	if err != nil {
		_ = e.store.MarkPending(ctx, u.ID, c.Index)
		e.count("chunk_retries", 1)
		return fmt.Errorf("upload part %d: %w", c.Index, err)
	}
	if err := e.store.MarkAcked(ctx, u.ID, c.Index, part.ETag, hex.EncodeToString(h.Sum(nil))); err != nil {
		return err
	}

	done, cur, avg, eta := prog.advance(c.Length, time.Now())
	_ = e.store.UpdateProgress(ctx, u.ID, done, cur, avg, eta)
	_ = e.store.RecordStat(ctx, u.ID, c.Length, cur)
	e.count("chunks_uploaded", 1)
	e.count("bytes_uploaded", c.Length)
	e.emit("progress", u.ID, map[string]any{"done": done, "total": u.Size, "bps": cur, "eta_s": eta})
	return nil
}

// finish assembles the parts and verifies the whole-file checksum before the
// upload is allowed to be Completed.
func (e *Engine) finish(ctx context.Context, u *manifests.Upload) error {
	acked, err := e.store.AckedChunks(ctx, u.ID)
	if err != nil {
		return err
	}
	if len(acked) != u.ChunkCount {
		return fmt.Errorf("expected %d parts, have %d", u.ChunkCount, len(acked))
	}
	parts := make([]storage.Part, len(acked))
	for i, c := range acked {
		parts[i] = storage.Part{PartNumber: c.Index + 1, ETag: c.ETag}
	}

	if err := e.store.SetStatus(ctx, u.ID, common.StatusAssembling, ""); err != nil {
		return err
	}
	if err := e.objects.CompleteMultipart(ctx, u.ObjectKey, u.MultipartID, parts); err != nil {
		return fmt.Errorf("complete multipart: %w", err)
	}

	if err := e.store.SetStatus(ctx, u.ID, common.StatusVerifying, ""); err != nil {
		return err
	}
	got, err := hashObject(ctx, e.objects, u.ObjectKey)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if got != u.SHA256 {
		_ = e.store.SetStatus(ctx, u.ID, common.StatusFailed, "checksum mismatch after assembly")
		_ = e.store.AppendEvent(ctx, string(u.UserID), "upload.failed", u.ID, "checksum mismatch")
		e.count("uploads_failed", 1)
		e.emit("upload.failed", u.ID, map[string]any{"reason": "checksum mismatch"})
		return fmt.Errorf("checksum mismatch: got %s want %s", got, u.SHA256)
	}

	if err := e.store.SetStatus(ctx, u.ID, common.StatusCompleted, ""); err != nil {
		return err
	}
	_ = e.store.AppendEvent(ctx, string(u.UserID), "upload.completed", u.ID, u.SHA256)
	e.count("uploads_completed", 1)
	if err := e.notifier.AssetReady(ctx, u); err != nil {
		e.log.Warn("cms notify failed", "upload", u.ID, "err", err)
	}
	e.emit("upload.completed", u.ID, map[string]any{"key": u.ObjectKey, "sha256": u.SHA256})
	e.log.Info("completed", "upload", u.ID, "sha256", u.SHA256)
	return nil
}

// Abort cancels an upload: it aborts the storage multipart (so no orphan parts
// are billed) and marks the upload Canceled. Best-effort on the storage side —
// a failed abort is logged, not fatal, since the upload is being discarded
// anyway. The controller is responsible for stopping any in-flight Run first.
func (e *Engine) Abort(ctx context.Context, id common.UploadID) error {
	u, err := e.store.LoadUpload(ctx, id)
	if err != nil {
		return err
	}
	if u.MultipartID != "" {
		if err := e.objects.AbortMultipart(ctx, u.ObjectKey, u.MultipartID); err != nil {
			e.log.Warn("abort multipart failed", "upload", id, "err", err)
		}
	}
	if err := e.store.SetStatus(ctx, id, common.StatusCanceled, ""); err != nil {
		return err
	}
	_ = e.store.AppendEvent(ctx, string(u.UserID), "upload.canceled", id, u.Filename)
	e.count("uploads_canceled", 1)
	e.emit("upload.canceled", id, map[string]any{"filename": u.Filename})
	return nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashObject(ctx context.Context, store storage.ObjectStore, key string) (string, error) {
	rc, err := store.Get(ctx, key)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
