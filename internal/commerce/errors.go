package commerce

import "errors"

var (
	ErrNotFound            = errors.New("not found")
	ErrForbidden           = errors.New("forbidden")
	ErrIneligible          = errors.New("ineligible")
	ErrInventoryEmpty      = errors.New("inventory empty")
	ErrInvalidAmount       = errors.New("invalid amount")
	ErrIdempotencyKey      = errors.New("idempotency key required")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	ErrInvalidToolContext  = errors.New("invalid trusted tool context")
	ErrInternalTool        = errors.New("internal tool error")
)
