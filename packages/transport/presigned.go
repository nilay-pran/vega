package transport

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"strings"

	"code.sli.ke/go/vega/packages/storage"
)

// PresignedStore uploads part bytes directly to S3/Spaces instead of through the
// upload server. The control plane (init, complete, abort, get, and the presign
// request itself) still goes to the server over HTTPS — so ownership and auth
// are unchanged — but the bulk bytes take the client→storage path, freeing the
// server from proxying them. This is the cheapest transport to operate: no data
// egress through our fleet, and it inherits S3's own durability and throughput.
//
// It embeds *HTTPStore for every operation except UploadPart, which it overrides
// to fetch a presigned URL and PUT to it. The URL is self-authenticating, so the
// direct PUT carries no bearer token.
type PresignedStore struct {
	*HTTPStore
	s3 *http.Client
}

var _ storage.ObjectStore = (*PresignedStore)(nil)

// NewPresignedStore builds a presigned transport. control carries the server
// (JSON) traffic; a separate client carries the direct-to-storage PUTs so their
// timeouts and TLS settings can differ. insecure skips verification on the
// direct PUT for a self-signed dev MinIO; production Spaces has real certs.
func NewPresignedStore(baseURL, token string, control *http.Client, insecure bool) *PresignedStore {
	s3 := &http.Client{}
	if insecure {
		s3.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return &PresignedStore{HTTPStore: NewHTTPStore(baseURL, token, control), s3: s3}
}

// UploadPart presigns the part with the server, then streams the bytes straight
// to storage. S3 returns the part ETag in the response header; it is trimmed of
// surrounding quotes to match the form the server's CompleteMultipart expects.
func (p *PresignedStore) UploadPart(ctx context.Context, key, uploadID string, partNumber int, r io.Reader, size int64) (storage.Part, error) {
	pre, err := p.PresignPart(ctx, key, uploadID, partNumber)
	if err != nil {
		return storage.Part{}, err
	}
	method := pre.Method
	if method == "" {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(ctx, method, pre.URL, r)
	if err != nil {
		return storage.Part{}, err
	}
	// The presigned URL signs only host; Content-Length is allowed unsigned and
	// lets the part stream without buffering. Do not add Content-Type — it is not
	// in the signature and some gateways reject unsigned signed-header mismatches.
	req.ContentLength = size

	resp, err := p.s3.Do(req)
	if err != nil {
		return storage.Part{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return storage.Part{}, errFromResp(resp)
	}
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	return storage.Part{PartNumber: partNumber, ETag: etag}, nil
}
