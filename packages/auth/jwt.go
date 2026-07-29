package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// JWTVerifier validates HS256-signed JWTs issued by the CMS: it checks the
// signature, expiry/not-before, and (when configured) audience and issuer, then
// returns the subject (CMS user id) used for upload ownership. RS256 via the CMS
// JWKS plugs in behind the same Verifier interface once the CMS exposes its keys
// (docs/ARCHITECTURE.md §14, Appendix B/1); the shape here is deliberately
// stdlib-only so security-critical code has no third-party surface.
type JWTVerifier struct {
	Secret   []byte           // HS256 shared secret
	Audience string           // required "aud" if non-empty
	Issuer   string           // required "iss" if non-empty
	Now      func() time.Time // injectable clock; defaults to time.Now
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type jwtClaims struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
	Nbf int64  `json:"nbf"`
	Iss string `json:"iss"`
	Aud any    `json:"aud"` // string or []string per RFC 7519
}

func (v JWTVerifier) Verify(_ context.Context, token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("%w: malformed token", ErrUnauthorized)
	}
	// Signature over "header.payload", verified before we trust any claim.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("%w: bad signature encoding", ErrUnauthorized)
	}
	mac := hmac.New(sha256.New, v.Secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", fmt.Errorf("%w: signature mismatch", ErrUnauthorized)
	}

	var hdr jwtHeader
	if err := decodeSegment(parts[0], &hdr); err != nil || hdr.Alg != "HS256" {
		return "", fmt.Errorf("%w: unsupported alg", ErrUnauthorized)
	}
	var c jwtClaims
	if err := decodeSegment(parts[1], &c); err != nil {
		return "", fmt.Errorf("%w: bad claims", ErrUnauthorized)
	}

	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	if c.Exp != 0 && now.Unix() >= c.Exp {
		return "", fmt.Errorf("%w: expired", ErrUnauthorized)
	}
	if c.Nbf != 0 && now.Unix() < c.Nbf {
		return "", fmt.Errorf("%w: not yet valid", ErrUnauthorized)
	}
	if v.Issuer != "" && c.Iss != v.Issuer {
		return "", fmt.Errorf("%w: wrong issuer", ErrUnauthorized)
	}
	if v.Audience != "" && !audienceContains(c.Aud, v.Audience) {
		return "", fmt.Errorf("%w: wrong audience", ErrUnauthorized)
	}
	if c.Sub == "" {
		return "", fmt.Errorf("%w: no subject", ErrUnauthorized)
	}
	return c.Sub, nil
}

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func audienceContains(aud any, want string) bool {
	switch a := aud.(type) {
	case string:
		return a == want
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}
