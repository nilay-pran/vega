package common

import "crypto/rand"

// Typed IDs. They are plain strings so they cross SQLite and the wire without
// conversion, but the distinct types stop us from passing an UploadID where a
// SessionID is wanted.
type (
	UploadID  string
	SessionID string
	UserID    string
)

// newID returns a URL-safe, collision-resistant random token.
// crypto/rand.Text (Go 1.24+) returns 26 random base32 chars and never errors.
func newID(prefix string) string { return prefix + rand.Text() }

func NewUploadID() UploadID   { return UploadID(newID("up_")) }
func NewSessionID() SessionID { return SessionID(newID("se_")) }
