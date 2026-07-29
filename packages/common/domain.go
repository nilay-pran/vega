// Package common holds the core domain types shared across every package.
// It has no dependencies of its own on purpose: everything else may import it,
// it imports nothing from this project.
package common

// UploadStatus is the upload-level state machine (see docs/ARCHITECTURE.md §8.1).
type UploadStatus string

const (
	StatusQueued       UploadStatus = "queued"
	StatusPreparing    UploadStatus = "preparing"
	StatusReady        UploadStatus = "ready"
	StatusConnecting   UploadStatus = "connecting"
	StatusUploading    UploadStatus = "uploading"
	StatusPaused       UploadStatus = "paused"
	StatusReconnecting UploadStatus = "reconnecting"
	StatusVerifying    UploadStatus = "verifying"
	StatusAssembling   UploadStatus = "assembling"
	StatusCompleted    UploadStatus = "completed"
	StatusFailed       UploadStatus = "failed"
	StatusCanceled     UploadStatus = "canceled"
)

// IsTerminal reports whether the upload will not change state again on its own.
func (s UploadStatus) IsTerminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCanceled
}

// ChunkState is the per-part state machine (see docs/ARCHITECTURE.md §8.2).
// A "missing" chunk is any chunk still in StatePending or StateInFlight.
type ChunkState string

const (
	StatePending  ChunkState = "pending"
	StateInFlight ChunkState = "inflight"
	StateAcked    ChunkState = "acked"
	StateVerified ChunkState = "verified"
	StateSkipped  ChunkState = "skipped"
)

// Missing reports whether the chunk still needs to be sent.
func (s ChunkState) Missing() bool {
	return s == StatePending || s == StateInFlight
}

// Transport identifies which path carried (or will carry) an upload.
type Transport string

const (
	TransportSLKT   Transport = "slkt"   // primary: custom protocol, server-in-path
	TransportDirect Transport = "direct" // failover: S3 multipart straight to object storage
)
