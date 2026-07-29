package common

import "errors"

// ErrNotFound is returned by repositories when a row does not exist.
// Callers check it with errors.Is; repositories wrap it with context.
var ErrNotFound = errors.New("not found")
