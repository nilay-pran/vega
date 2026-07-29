// Package transport carries chunks to the upload server. HTTPStore is the v1
// server-in-path adapter: it satisfies storage.ObjectStore by proxying each
// multipart operation to the upload server over HTTPS, so the same engine code
// runs unchanged whether it talks to this, to direct-to-Spaces, or to a fake.
// The binary SLKT transport (docs/ARCHITECTURE.md §5–§6) will slot in here as a
// second ObjectStore-shaped adapter without touching the engine.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"code.sli.ke/go/vega/packages/storage"
)

// Wire DTOs shared with the server (the server imports these so there is one
// definition of the contract).
type (
	InitRequest struct {
		Key string `json:"key"`
	}
	InitResponse struct {
		MultipartID string `json:"multipartId"`
	}
	PartResponse struct {
		PartNumber int    `json:"partNumber"`
		ETag       string `json:"etag"`
	}
	PartDTO struct {
		PartNumber int    `json:"partNumber"`
		ETag       string `json:"etag"`
	}
	CompleteRequest struct {
		Key   string    `json:"key"`
		Parts []PartDTO `json:"parts"`
	}
	// PresignResponse carries a direct-to-storage upload URL for one part. The
	// URL is self-authenticating (S3 SigV4 query signature); the client PUTs the
	// part bytes to it without any bearer token.
	PresignResponse struct {
		URL    string `json:"url"`
		Method string `json:"method"`
	}
)

type HTTPStore struct {
	base   string
	token  string
	client *http.Client
}

// Ensure HTTPStore satisfies the port.
var _ storage.ObjectStore = (*HTTPStore)(nil)

func NewHTTPStore(baseURL, token string, client *http.Client) *HTTPStore {
	if client == nil {
		client = &http.Client{}
	}
	return &HTTPStore{base: strings.TrimRight(baseURL, "/"), token: token, client: client}
}

// Probe checks the upload server answers over HTTP(S). Used by the transport
// chooser as the reachable fallback when the custom protocol is blocked.
func (h *HTTPStore) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base+"/v1/ping", nil)
	if err != nil {
		return err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ping: status %s", resp.Status)
	}
	return nil
}

func (h *HTTPStore) InitMultipart(ctx context.Context, key string) (string, error) {
	var out InitResponse
	if err := h.doJSON(ctx, http.MethodPost, "/v1/multipart", InitRequest{Key: key}, &out); err != nil {
		return "", err
	}
	return out.MultipartID, nil
}

func (h *HTTPStore) UploadPart(ctx context.Context, key, uploadID string, partNumber int, r io.Reader, size int64) (storage.Part, error) {
	u := fmt.Sprintf("%s/v1/multipart/%s/parts/%d?key=%s", h.base, url.PathEscape(uploadID), partNumber, url.QueryEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, r)
	if err != nil {
		return storage.Part{}, err
	}
	req.ContentLength = size // stream the part; no full-buffer copy
	req.Header.Set("Content-Type", "application/octet-stream")
	h.auth(req)

	resp, err := h.client.Do(req)
	if err != nil {
		return storage.Part{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return storage.Part{}, errFromResp(resp)
	}
	var out PartResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return storage.Part{}, err
	}
	return storage.Part{PartNumber: out.PartNumber, ETag: out.ETag}, nil
}

// PresignPart asks the server for a direct-to-storage upload URL for one part.
// It is the control-plane call the presigned transport makes before PUTting the
// bytes straight to S3/Spaces.
func (h *HTTPStore) PresignPart(ctx context.Context, key, uploadID string, partNumber int) (PresignResponse, error) {
	path := fmt.Sprintf("/v1/multipart/%s/parts/%d/presign?key=%s", url.PathEscape(uploadID), partNumber, url.QueryEscape(key))
	var out PresignResponse
	if err := h.doJSON(ctx, http.MethodPost, path, nil, &out); err != nil {
		return PresignResponse{}, err
	}
	return out, nil
}

func (h *HTTPStore) CompleteMultipart(ctx context.Context, key, uploadID string, parts []storage.Part) error {
	dto := CompleteRequest{Key: key, Parts: make([]PartDTO, len(parts))}
	for i, p := range parts {
		dto.Parts[i] = PartDTO{PartNumber: p.PartNumber, ETag: p.ETag}
	}
	path := fmt.Sprintf("/v1/multipart/%s/complete", url.PathEscape(uploadID))
	return h.doJSON(ctx, http.MethodPost, path, dto, nil)
}

func (h *HTTPStore) AbortMultipart(ctx context.Context, key, uploadID string) error {
	u := fmt.Sprintf("%s/v1/multipart/%s?key=%s", h.base, url.PathEscape(uploadID), url.QueryEscape(key))
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	h.auth(req)
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return errFromResp(resp)
	}
	return nil
}

func (h *HTTPStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	u := fmt.Sprintf("%s/v1/object?key=%s", h.base, url.QueryEscape(key))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	h.auth(req)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, storage.ErrNoSuchKey
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, errFromResp(resp)
	}
	return resp.Body, nil // caller closes
}

func (h *HTTPStore) auth(req *http.Request) {
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
}

func (h *HTTPStore) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	h.auth(req)
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return errFromResp(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func errFromResp(resp *http.Response) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("server %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
}
