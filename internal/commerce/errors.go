package commerce

import "errors"

var (
	ErrNotFound       = errors.New("not found")
	ErrForbidden      = errors.New("forbidden")
	ErrIneligible     = errors.New("ineligible")
	ErrInventoryEmpty = errors.New("inventory empty")
	ErrInvalidAmount  = errors.New("invalid amount")
	ErrIdempotencyKey = errors.New("idempotency key required")
)
