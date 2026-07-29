package uploader

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"code.sli.ke/go/vega/packages/manifests"
)

func TestHTTPNotifierSignsAndSendsIdempotencyKey(t *testing.T) {
	secret := []byte("cms-secret")
	var gotKey, gotSig string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		gotSig = r.Header.Get("X-Slike-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := HTTPNotifier{URL: srv.URL, Secret: secret, Client: srv.Client()}
	u := &manifests.Upload{ID: "up1", CMSFileID: "file9", ObjectKey: "k", SHA256: "abc", Size: 10}
	if err := n.AssetReady(context.Background(), u); err != nil {
		t.Fatalf("notify: %v", err)
	}

	if gotKey != "up1" {
		t.Fatalf("idempotency key = %q, want up1", gotKey)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(gotBody)
	if want := hex.EncodeToString(mac.Sum(nil)); gotSig != want {
		t.Fatalf("signature = %q, want %q", gotSig, want)
	}
}

func TestHTTPNotifierErrorsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	n := HTTPNotifier{URL: srv.URL, Client: srv.Client()}
	if err := n.AssetReady(context.Background(), &manifests.Upload{ID: "x"}); err == nil {
		t.Fatal("expected error on 500")
	}
}
