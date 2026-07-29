package uploader

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"code.sli.ke/go/vega/packages/manifests"
)

// Notifier is told when an upload is fully assembled and verified, so the CMS
// can be informed the asset is ready. The real adapter posts to the CMS webhook
// (docs/ARCHITECTURE.md §12); LogNotifier is the default for local/dev.
type Notifier interface {
	AssetReady(ctx context.Context, u *manifests.Upload) error
}

type LogNotifier struct{ Log *slog.Logger }

func (n LogNotifier) AssetReady(_ context.Context, u *manifests.Upload) error {
	n.Log.Info("asset ready", "upload", u.ID, "key", u.ObjectKey, "sha256", u.SHA256)
	return nil
}

// AssetReadyPayload is the body POSTed to the CMS webhook.
type AssetReadyPayload struct {
	UploadID  string `json:"uploadId"`
	FileID    string `json:"fileId"`
	ObjectKey string `json:"objectKey"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

// HTTPNotifier posts "asset ready" to the CMS webhook. The body is signed with a
// shared secret (HMAC-SHA256 in the X-Slike-Signature header) so the CMS can
// verify authenticity, and the upload id is sent as an Idempotency-Key so
// retries never create duplicate assets.
type HTTPNotifier struct {
	URL    string
	Secret []byte
	Client *http.Client
}

func (n HTTPNotifier) AssetReady(ctx context.Context, u *manifests.Upload) error {
	body, err := json.Marshal(AssetReadyPayload{
		UploadID:  string(u.ID),
		FileID:    u.CMSFileID,
		ObjectKey: u.ObjectKey,
		SHA256:    u.SHA256,
		Size:      u.Size,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", string(u.ID))
	if len(n.Secret) > 0 {
		mac := hmac.New(sha256.New, n.Secret)
		mac.Write(body)
		req.Header.Set("X-Slike-Signature", hex.EncodeToString(mac.Sum(nil)))
	}

	client := n.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("cms notify: status %s", resp.Status)
	}
	return nil
}
