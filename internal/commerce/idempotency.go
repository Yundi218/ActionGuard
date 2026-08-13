package commerce

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type IdempotencyIdentity struct {
	Operation          string
	Key                string
	PrincipalID        string
	RequestFingerprint string
}

type returnFingerprintRequest struct {
	Operation   string `json:"operation"`
	PrincipalID string `json:"principal_id"`
	OrderID     string `json:"order_id"`
	Reason      string `json:"reason"`
}

type replacementFingerprintRequest struct {
	Operation   string `json:"operation"`
	PrincipalID string `json:"principal_id"`
	OrderID     string `json:"order_id"`
	SKU         string `json:"sku"`
	Reason      string `json:"reason"`
}

type refundFingerprintRequest struct {
	Operation   string `json:"operation"`
	PrincipalID string `json:"principal_id"`
	OrderID     string `json:"order_id"`
	AmountCents int64  `json:"amount_cents"`
}

type couponFingerprintRequest struct {
	Operation   string `json:"operation"`
	PrincipalID string `json:"principal_id"`
	AmountCents int64  `json:"amount_cents"`
	Reason      string `json:"reason"`
}

func newReturnIdentity(principalID, orderID, reason, key string) (IdempotencyIdentity, error) {
	return newIdempotencyIdentity(key, principalID, returnFingerprintRequest{
		Operation: createReturnOperation, PrincipalID: principalID, OrderID: orderID, Reason: reason,
	})
}

func newReplacementIdentity(principalID, orderID, sku, reason, key string) (IdempotencyIdentity, error) {
	return newIdempotencyIdentity(key, principalID, replacementFingerprintRequest{
		Operation: createReplacementOperation, PrincipalID: principalID, OrderID: orderID, SKU: sku, Reason: reason,
	})
}

func newRefundIdentity(principalID, orderID string, amountCents int64, key string) (IdempotencyIdentity, error) {
	return newIdempotencyIdentity(key, principalID, refundFingerprintRequest{
		Operation: issueRefundOperation, PrincipalID: principalID, OrderID: orderID, AmountCents: amountCents,
	})
}

func newCouponIdentity(principalID string, amountCents int64, reason, key string) (IdempotencyIdentity, error) {
	return newIdempotencyIdentity(key, principalID, couponFingerprintRequest{
		Operation: issueCouponOperation, PrincipalID: principalID, AmountCents: amountCents, Reason: reason,
	})
}

func newIdempotencyIdentity(key, principalID string, request any) (IdempotencyIdentity, error) {
	if key == "" {
		return IdempotencyIdentity{}, ErrIdempotencyKey
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return IdempotencyIdentity{}, fmt.Errorf("marshal idempotency request: %w", err)
	}
	digest := sha256.Sum256(payload)
	operation := operationFromRequest(request)
	return IdempotencyIdentity{
		Operation:          operation,
		Key:                key,
		PrincipalID:        principalID,
		RequestFingerprint: hex.EncodeToString(digest[:]),
	}, nil
}

func operationFromRequest(request any) string {
	switch value := request.(type) {
	case returnFingerprintRequest:
		return value.Operation
	case replacementFingerprintRequest:
		return value.Operation
	case refundFingerprintRequest:
		return value.Operation
	case couponFingerprintRequest:
		return value.Operation
	default:
		return ""
	}
}

func (identity IdempotencyIdentity) validate(operation string) error {
	if identity.Key == "" {
		return ErrIdempotencyKey
	}
	if identity.Operation != operation || identity.PrincipalID == "" || len(identity.RequestFingerprint) != sha256.Size*2 {
		return ErrInternalTool
	}
	for i := 0; i < len(identity.RequestFingerprint); i++ {
		fingerprintByte := identity.RequestFingerprint[i]
		if (fingerprintByte < '0' || fingerprintByte > '9') && (fingerprintByte < 'a' || fingerprintByte > 'f') {
			return ErrInternalTool
		}
	}
	return nil
}
