package storage

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"sort"
	"sync"
)

// MemStore is an in-memory ObjectStore for tests and local dev. It mimics S3
// multipart semantics (parts held until CompleteMultipart concatenates them in
// part-number order) so the engine exercises the real code path without any
// external service.
type MemStore struct {
	mu      sync.Mutex
	seq     int
	uploads map[string]map[int][]byte // uploadID -> partNumber -> data
	objects map[string][]byte         // key -> assembled object
}

func NewMemStore() *MemStore {
	return &MemStore{
		uploads: make(map[string]map[int][]byte),
		objects: make(map[string][]byte),
	}
}

func (m *MemStore) InitMultipart(_ context.Context, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	id := fmt.Sprintf("mp-%d", m.seq)
	m.uploads[id] = make(map[int][]byte)
	return id, nil
}

func (m *MemStore) UploadPart(_ context.Context, _, uploadID string, partNumber int, r io.Reader, _ int64) (Part, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Part{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	parts, ok := m.uploads[uploadID]
	if !ok {
		return Part{}, fmt.Errorf("memstore: unknown multipart upload %q", uploadID)
	}
	parts[partNumber] = data // last write wins → re-delivery is idempotent
	return Part{PartNumber: partNumber, ETag: fmt.Sprintf("%x", md5.Sum(data))}, nil
}

func (m *MemStore) CompleteMultipart(_ context.Context, key, uploadID string, parts []Part) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.uploads[uploadID]
	if !ok {
		return fmt.Errorf("memstore: unknown multipart upload %q", uploadID)
	}
	ordered := append([]Part(nil), parts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].PartNumber < ordered[j].PartNumber })

	var buf bytes.Buffer
	for _, p := range ordered {
		data, ok := stored[p.PartNumber]
		if !ok {
			return fmt.Errorf("memstore: missing part %d on complete", p.PartNumber)
		}
		buf.Write(data)
	}
	m.objects[key] = buf.Bytes()
	delete(m.uploads, uploadID)
	return nil
}

func (m *MemStore) AbortMultipart(_ context.Context, _, uploadID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.uploads, uploadID)
	return nil
}

func (m *MemStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, ErrNoSuchKey
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), nil
}
