package ivy

import (
	"errors"
)

// Sentinel errors returned by the ivy engine; compare with errors.Is.
var (
	// ErrNotExistsTree tree not exists
	ErrNotExistsTree = errors.New("tree not exists")
	// ErrRateLimited rate limited
	ErrRateLimited = errors.New("rate limited")
)
