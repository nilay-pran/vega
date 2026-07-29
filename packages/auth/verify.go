// Package auth verifies bearer tokens for the upload server and (later) refreshes
// them on the client. v1 ships a dev verifier; the production JWKS verifier that
// validates CMS-issued JWTs (signature, exp, aud, scope, ownership subject) plugs
// in behind the same Verifier interface (docs/ARCHITECTURE.md §14).
package auth

import (
	"context"
	"crypto/subtle"
	"errors"
)

// ErrUnauthorized is returned when a token is missing or invalid.
var ErrUnauthorized = errors.New("auth: unauthorized")

// Verifier turns a bearer token into the authenticated subject (CMS user id).
type Verifier interface {
	Verify(ctx context.Context, token string) (subject string, err error)
}

// DevVerifier accepts a single configured token and maps it to a fixed subject.
// For local development and tests only — never wire this into production.
type DevVerifier struct {
	Token   string
	Subject string
}

func (v DevVerifier) Verify(_ context.Context, token string) (string, error) {
	if v.Token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(v.Token)) != 1 {
		return "", ErrUnauthorized
	}
	return v.Subject, nil
}
