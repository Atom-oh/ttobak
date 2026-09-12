package service

import "errors"

// ErrResummaryNoSource rejects empty or unhydrated summary evidence.
var ErrResummaryNoSource = errors.New("no saved summary source")
