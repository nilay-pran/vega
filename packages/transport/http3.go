package transport

import (
	"crypto/tls"
	"net/http"

	"github.com/quic-go/quic-go/http3"
)

// NewHTTP3Store returns an HTTPStore that speaks HTTP/3 (QUIC) instead of
// TCP-based HTTP. The multipart wire contract is unchanged — only the transport
// underneath differs — so the engine and server code are identical to the
// HTTP/1.1+2 path. QUIC brings TLS 1.3, BBR-style congestion control, stream
// multiplexing, and connection migration for free, none of which the bespoke
// SLKT transport implements yet.
//
// insecure skips certificate verification for the self-signed dev server; it
// mirrors the -insecure flag the HTTP path already honours.
func NewHTTP3Store(baseURL, token string, insecure bool) *HTTPStore {
	rt := &http3.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure},
	}
	return NewHTTPStore(baseURL, token, &http.Client{Transport: rt})
}
