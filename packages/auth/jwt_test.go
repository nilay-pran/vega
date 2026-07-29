package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// mint builds an HS256 JWT for tests.
func mint(secret []byte, claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	head := enc(map[string]string{"alg": "HS256", "typ": "JWT"})
	body := enc(claims)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(head + "." + body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return head + "." + body + "." + sig
}

func TestJWTVerifyValid(t *testing.T) {
	secret := []byte("topsecret")
	v := JWTVerifier{Secret: secret, Audience: "upload", Issuer: "cms", Now: func() time.Time { return time.Unix(1000, 0) }}
	tok := mint(secret, map[string]any{"sub": "user-42", "exp": 2000, "aud": "upload", "iss": "cms"})

	sub, err := v.Verify(context.Background(), tok)
	if err != nil || sub != "user-42" {
		t.Fatalf("got sub=%q err=%v; want user-42", sub, err)
	}
}

func TestJWTVerifyRejects(t *testing.T) {
	secret := []byte("topsecret")
	base := JWTVerifier{Secret: secret, Audience: "upload", Issuer: "cms", Now: func() time.Time { return time.Unix(1000, 0) }}

	cases := map[string]string{
		"expired":        mint(secret, map[string]any{"sub": "u", "exp": 500, "aud": "upload", "iss": "cms"}),
		"wrong audience": mint(secret, map[string]any{"sub": "u", "exp": 2000, "aud": "other", "iss": "cms"}),
		"wrong issuer":   mint(secret, map[string]any{"sub": "u", "exp": 2000, "aud": "upload", "iss": "evil"}),
		"no subject":     mint(secret, map[string]any{"exp": 2000, "aud": "upload", "iss": "cms"}),
		"bad signature":  mint([]byte("wrong-secret"), map[string]any{"sub": "u", "exp": 2000, "aud": "upload", "iss": "cms"}),
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := base.Verify(context.Background(), tok); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("expected ErrUnauthorized, got %v", err)
			}
		})
	}
}
