package commerce

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestWriteIdentitiesAreStableAndBindEveryArgument(t *testing.T) {
	tests := []struct {
		name       string
		baseline   func() (IdempotencyIdentity, error)
		variations []func() (IdempotencyIdentity, error)
	}{
		{
			name: "return",
			baseline: func() (IdempotencyIdentity, error) {
				return newReturnIdentity("user-1", "order-1", "damaged", "key-1")
			},
			variations: []func() (IdempotencyIdentity, error){
				func() (IdempotencyIdentity, error) { return newReturnIdentity("user-2", "order-1", "damaged", "key-1") },
				func() (IdempotencyIdentity, error) { return newReturnIdentity("user-1", "order-2", "damaged", "key-1") },
				func() (IdempotencyIdentity, error) {
					return newReturnIdentity("user-1", "order-1", "wrong item", "key-1")
				},
			},
		},
		{
			name: "replacement",
			baseline: func() (IdempotencyIdentity, error) {
				return newReplacementIdentity("user-1", "order-1", "sku-1", "damaged", "key-1")
			},
			variations: []func() (IdempotencyIdentity, error){
				func() (IdempotencyIdentity, error) {
					return newReplacementIdentity("user-2", "order-1", "sku-1", "damaged", "key-1")
				},
				func() (IdempotencyIdentity, error) {
					return newReplacementIdentity("user-1", "order-2", "sku-1", "damaged", "key-1")
				},
				func() (IdempotencyIdentity, error) {
					return newReplacementIdentity("user-1", "order-1", "sku-2", "damaged", "key-1")
				},
				func() (IdempotencyIdentity, error) {
					return newReplacementIdentity("user-1", "order-1", "sku-1", "wrong item", "key-1")
				},
			},
		},
		{
			name: "refund",
			baseline: func() (IdempotencyIdentity, error) {
				return newRefundIdentity("user-1", "order-1", 1000, "key-1")
			},
			variations: []func() (IdempotencyIdentity, error){
				func() (IdempotencyIdentity, error) { return newRefundIdentity("user-2", "order-1", 1000, "key-1") },
				func() (IdempotencyIdentity, error) { return newRefundIdentity("user-1", "order-2", 1000, "key-1") },
				func() (IdempotencyIdentity, error) { return newRefundIdentity("user-1", "order-1", 1001, "key-1") },
			},
		},
		{
			name: "coupon",
			baseline: func() (IdempotencyIdentity, error) {
				return newCouponIdentity("user-1", 1000, "service recovery", "key-1")
			},
			variations: []func() (IdempotencyIdentity, error){
				func() (IdempotencyIdentity, error) {
					return newCouponIdentity("user-2", 1000, "service recovery", "key-1")
				},
				func() (IdempotencyIdentity, error) {
					return newCouponIdentity("user-1", 1001, "service recovery", "key-1")
				},
				func() (IdempotencyIdentity, error) {
					return newCouponIdentity("user-1", 1000, "different reason", "key-1")
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, err := tt.baseline()
			if err != nil {
				t.Fatal(err)
			}
			second, err := tt.baseline()
			if err != nil {
				t.Fatal(err)
			}
			if first != second {
				t.Fatalf("identity is not stable: first=%#v second=%#v", first, second)
			}
			if first.PrincipalID != "user-1" || first.Key != "key-1" || len(first.RequestFingerprint) != 64 {
				t.Fatalf("identity = %#v", first)
			}
			if _, err := hex.DecodeString(first.RequestFingerprint); err != nil {
				t.Fatalf("fingerprint is not hexadecimal SHA-256: %q", first.RequestFingerprint)
			}
			for i, variation := range tt.variations {
				changed, err := variation()
				if err != nil {
					t.Fatal(err)
				}
				if changed.RequestFingerprint == first.RequestFingerprint {
					t.Errorf("variation %d did not change fingerprint", i)
				}
			}
		})
	}
}

func TestWriteIdentityRequiresIdempotencyKey(t *testing.T) {
	_, err := newRefundIdentity("user-1", "order-1", 1000, "")
	if err != ErrIdempotencyKey {
		t.Fatalf("error = %v, want ErrIdempotencyKey", err)
	}
}

func TestReturnIdentityHashesCanonicalTypedJSON(t *testing.T) {
	identity, err := newReturnIdentity("user-1", "order-1", "damaged", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	const want = "5a16503e2015fda33c014bf303ce923adaa0efc523dab5230fa955aa32e3d53e"
	if identity.RequestFingerprint != want {
		t.Fatalf("fingerprint = %q, want %q", identity.RequestFingerprint, want)
	}
}

func TestIdempotencyIdentityValidationRequiresLowercaseASCIIHexFingerprint(t *testing.T) {
	valid := IdempotencyIdentity{
		Operation:          createReturnOperation,
		Key:                "key-1",
		PrincipalID:        "user-1",
		RequestFingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	tests := []struct {
		name        string
		fingerprint string
		wantErr     bool
	}{
		{name: "lowercase hex", fingerprint: valid.RequestFingerprint},
		{name: "uppercase hex", fingerprint: "0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef", wantErr: true},
		{name: "invalid ASCII character", fingerprint: "g123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", wantErr: true},
		{name: "unicode character", fingerprint: strings.Repeat("a", 62) + "é", wantErr: true},
		{name: "wrong length", fingerprint: valid.RequestFingerprint[:63], wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity := valid
			identity.RequestFingerprint = tt.fingerprint
			err := identity.validate(createReturnOperation)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validate() error = %v, want error = %t", err, tt.wantErr)
			}
		})
	}
}
