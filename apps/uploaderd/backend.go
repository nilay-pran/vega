package main

import (
	"context"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/ipc"
	"code.sli.ke/go/vega/packages/manifests"
	"code.sli.ke/go/vega/packages/telemetry"
	"code.sli.ke/go/vega/packages/uploader"
)

// backend implements ipc.Backend by delegating to the engine (enqueue),
// the supervisor (pause/resume/cancel), and the store (read views). The daemon
// serves a single OS user, so it holds that identity and stamps it on enqueues
// rather than trusting the wire.
type backend struct {
	store   *manifests.Store
	engine  *uploader.Engine
	sup     *uploader.Supervisor
	metrics *telemetry.Metrics
	user    common.UserID
	email   string
}

func (b *backend) Enqueue(ctx context.Context, req ipc.EnqueueRequest) (common.UploadID, error) {
	id, err := b.engine.Enqueue(ctx, uploader.EnqueueReq{
		User:      b.user,
		Email:     b.email,
		Path:      req.Path,
		CMSFileID: req.CMSFileID,
		ObjectKey: req.ObjectKey,
	})
	if err != nil {
		return "", err
	}
	b.sup.Kick() // start it now if a slot is free
	return id, nil
}

func (b *backend) List(ctx context.Context) ([]ipc.UploadView, error) {
	sums, err := b.store.ListSummaries(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]ipc.UploadView, len(sums))
	for i, s := range sums {
		views[i] = toView(s)
	}
	return views, nil
}

func (b *backend) Get(ctx context.Context, id common.UploadID) (ipc.UploadView, error) {
	s, err := b.store.LoadSummary(ctx, id)
	if err != nil {
		return ipc.UploadView{}, err
	}
	return toView(s), nil
}

func (b *backend) Pause(ctx context.Context, id common.UploadID) error  { return b.sup.Pause(ctx, id) }
func (b *backend) Resume(ctx context.Context, id common.UploadID) error { return b.sup.Resume(ctx, id) }
func (b *backend) Cancel(ctx context.Context, id common.UploadID) error { return b.sup.Cancel(ctx, id) }

func (b *backend) Metrics(context.Context) map[string]int64 { return b.metrics.Snapshot() }

func toView(s manifests.Summary) ipc.UploadView {
	return ipc.UploadView{
		ID:         string(s.ID),
		Filename:   s.Filename,
		Size:       s.Size,
		Status:     string(s.Status),
		BytesDone:  s.BytesDone,
		CurBPS:     s.CurBPS,
		AvgBPS:     s.AvgBPS,
		ETASeconds: s.ETASeconds,
		Error:      s.Error,
		UpdatedAt:  s.UpdatedAt,
	}
}
