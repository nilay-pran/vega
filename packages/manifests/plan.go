// Package manifests plans how a file is split into chunks and persists the
// resulting manifest and per-chunk state. A chunk is one S3 multipart part.
package manifests

import "code.sli.ke/go/vega/packages/common"

// S3 multipart limits (docs/ARCHITECTURE.md §13). These bound adaptive sizing.
const (
	MinPartSize = int64(5) << 20 // 5 MiB — floor for every part except the last
	MaxPartSize = int64(5) << 30 // 5 GiB — ceiling for a single part
	MaxParts    = 10000          // parts per multipart upload
)

// PlanChunkSize picks a part size for a file that respects the multipart limits:
// at least 5 MiB, at most 5 GiB, and never more than 10 000 parts. It grows the
// part size for very large files so the part count stays under the ceiling
// (e.g. a 5 TB file lands around 512 MiB parts).
func PlanChunkSize(fileSize int64) int64 {
	if fileSize <= MinPartSize {
		if fileSize <= 0 {
			return MinPartSize
		}
		return fileSize // single, sub-floor part is allowed as the last part
	}
	size := MinPartSize
	if byParts := ceilDiv(fileSize, MaxParts); byParts > size {
		size = roundUpMiB(byParts)
	}
	if size > MaxPartSize {
		size = MaxPartSize
	}
	return size
}

// PlanChunks lays out the chunk offsets for a file of fileSize using chunkSize.
// The last chunk carries the remainder. A zero-length file yields one empty chunk.
func PlanChunks(fileSize, chunkSize int64) []Chunk {
	if chunkSize <= 0 {
		chunkSize = MinPartSize
	}
	var chunks []Chunk
	idx := 0
	for off := int64(0); off < fileSize; off += chunkSize {
		length := chunkSize
		if off+length > fileSize {
			length = fileSize - off
		}
		chunks = append(chunks, Chunk{Index: idx, Offset: off, Length: length, State: common.StatePending})
		idx++
	}
	if len(chunks) == 0 {
		chunks = append(chunks, Chunk{Index: 0, Offset: 0, Length: 0, State: common.StatePending})
	}
	return chunks
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

func roundUpMiB(n int64) int64 {
	const mib = int64(1) << 20
	return ((n + mib - 1) / mib) * mib
}
