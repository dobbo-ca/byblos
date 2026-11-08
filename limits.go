package byblos

import (
	"errors"

	"github.com/dobbo-ca/byblos/internal/limits"
)

// Limits bounds one call; the zero value, and any field <= 0, is the safe
// default. See docs/guides/memory-limits.md for the caps and their defaults.
type Limits = limits.Limits

// LimitError names the cap an input breached. It wraps ErrLimit.
type LimitError = limits.LimitError

// ErrLimit is wrapped by every *LimitError: permanent, do not retry.
var ErrLimit = limits.ErrLimit

// ErrMemoryLimit means other operations hold the memory budget and this one
// would fit alone: transient, retry. A *LimitError never wraps it.
var ErrMemoryLimit = limits.ErrMemoryLimit

// ErrPassword means the document needs a non-empty user password, which
// byblos does not have. An owner-password-only document opens normally.
var ErrPassword = errors.New("byblos: document needs a password")

// SetMemoryLimit sets the bytes all byblos operations in flight may hold
// together; n <= 0 means none. It returns the previous value. Unset, the limit
// is half of the Go memory limit (GOMEMLIMIT), else half of the cgroup
// memory.max, else none.
func SetMemoryLimit(n int64) int64 { return limits.SetMemoryLimit(n) }
