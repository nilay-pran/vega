// Package ipc is the local control API between the uploader daemon and its
// thin controllers (the desktop UI, the CLI). It is plain HTTP+JSON spoken over
// a per-user Unix domain socket, guarded by a bearer token, with a
// server-sent-events stream for live progress. The socket keeps the API off the
// network entirely; the token stops other local users from driving the daemon.
package ipc

import (
	"crypto/rand"
	"os"
	"path/filepath"
)

// UploadView is the wire form of an upload in a list or detail response. It
// carries identity and live progress only — never a source path or secret.
type UploadView struct {
	ID         string `json:"id"`
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	Status     string `json:"status"`
	BytesDone  int64  `json:"bytesDone"`
	CurBPS     int64  `json:"curBps"`
	AvgBPS     int64  `json:"avgBps"`
	ETASeconds int64  `json:"etaSeconds"`
	Error      string `json:"error,omitempty"`
	UpdatedAt  int64  `json:"updatedAt"`
}

// EnqueueRequest asks the daemon to plan and start uploading a local file. The
// daemon already knows which user it serves, so no identity travels on the wire.
type EnqueueRequest struct {
	Path      string `json:"path"`
	CMSFileID string `json:"cmsFileId,omitempty"`
	ObjectKey string `json:"objectKey,omitempty"`
}

// IDResponse is returned by enqueue.
type IDResponse struct {
	ID string `json:"id"`
}

// errorResponse is the body of any non-2xx reply.
type errorResponse struct {
	Error string `json:"error"`
}

// DefaultSocketPath returns the per-user control-socket path, creating its
// parent directory. All controllers and the daemon derive it the same way.
func DefaultSocketPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "uploaderd.sock"), nil
}

// StatePath returns a path named within the per-user state directory, creating
// the directory. The daemon keeps its socket, token, and database side by side
// here so it depends on no working directory — a background service has none.
func StatePath(name string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// LoadOrCreateToken reads the shared bearer token, generating a 0600 one on
// first run. The daemon and every controller run as the same OS user, so the
// filesystem is the trust boundary.
func LoadOrCreateToken() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "token")
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		return string(b), nil
	}
	tok := rand.Text()
	if err := os.WriteFile(path, []byte(tok), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

func stateDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "vega")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
