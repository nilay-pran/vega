package manifests

import "code.sli.ke/go/vega/packages/common"

// Upload is the engine's working view of an upload: the uploads row and its
// upload_manifests row joined together. Persistence splits them back apart.
type Upload struct {
	ID          common.UploadID
	UserID      common.UserID
	CMSFileID   string
	SourcePath  string
	Filename    string
	Size        int64
	Status      common.UploadStatus
	ObjectKey   string
	MultipartID string
	SHA256      string // whole-file hash, verified after assembly
	ChunkSize   int64
	ChunkCount  int
}

// Summary is the read-only view a controller (UI, CLI) shows in an upload list:
// identity plus live progress. It is derived from the uploads + upload_manifests
// rows and never carries source paths or secrets.
type Summary struct {
	ID         common.UploadID
	Filename   string
	Size       int64
	Status     common.UploadStatus
	BytesDone  int64
	CurBPS     int64
	AvgBPS     int64
	ETASeconds int64
	Error      string
	UpdatedAt  int64
}

// Chunk is one part's plan and state.
type Chunk struct {
	Index  int
	Offset int64
	Length int64
	State  common.ChunkState
	ETag   string // set once the part is stored
	SHA256 string // per-chunk hash, computed while streaming
	Retry  int
}
