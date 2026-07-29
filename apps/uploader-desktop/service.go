package main

import (
	"context"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/ipc"
)

// UploadService is the desktop app's controller over the uploader daemon. Every
// method is a thin call across the daemon's Unix socket, so the UI holds no
// upload state of its own: close the window and uploads keep running, reopen it
// and List reattaches to the same daemon. The leading context.Context on each
// method is injected by Wails and never appears in the generated JS signature.
type UploadService struct {
	client *ipc.Client
}

// newUploadService dials the per-user daemon socket. It does not require the
// daemon to be up yet — calls simply fail until it is, and the UI reflects that.
func newUploadService() (*UploadService, error) {
	socket, err := ipc.DefaultSocketPath()
	if err != nil {
		return nil, err
	}
	token, err := ipc.LoadOrCreateToken()
	if err != nil {
		return nil, err
	}
	return &UploadService{client: ipc.NewClient(socket, token)}, nil
}

func (s *UploadService) List(ctx context.Context) ([]ipc.UploadView, error) {
	return s.client.List(ctx)
}

func (s *UploadService) Enqueue(ctx context.Context, path string) (string, error) {
	id, err := s.client.Enqueue(ctx, ipc.EnqueueRequest{Path: path})
	return string(id), err
}

func (s *UploadService) Pause(ctx context.Context, id string) error {
	return s.client.Pause(ctx, common.UploadID(id))
}

func (s *UploadService) Resume(ctx context.Context, id string) error {
	return s.client.Resume(ctx, common.UploadID(id))
}

func (s *UploadService) Cancel(ctx context.Context, id string) error {
	return s.client.Cancel(ctx, common.UploadID(id))
}

func (s *UploadService) Metrics(ctx context.Context) (map[string]int64, error) {
	return s.client.Metrics(ctx)
}
