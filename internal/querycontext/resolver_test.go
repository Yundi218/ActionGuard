package querycontext

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/commerce"
)

type resolveOrderContextCall struct {
	userID  string
	orderID string
}

type fakeOrderContextReader struct {
	result   commerce.OrderContext
	err      error
	calls    []resolveOrderContextCall
	contexts []context.Context
}

func (f *fakeOrderContextReader) ResolveOrderContext(ctx context.Context, userID, orderID string) (commerce.OrderContext, error) {
	f.calls = append(f.calls, resolveOrderContextCall{userID: userID, orderID: orderID})
	f.contexts = append(f.contexts, ctx)
	if f.err != nil {
		return commerce.OrderContext{}, fmt.Errorf("resolve order context: %w", f.err)
	}
	return f.result, nil
}

func TestResolverBuildsTrustedContextFromOneDistinctOrderID(t *testing.T) {
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("request"), "request-1")
	local := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, 8, 13, 19, 30, 0, 123, local)
	reader := &fakeOrderContextReader{result: commerce.OrderContext{
		Order:           commerce.Order{ID: "AG-1042", UserID: "user_018", SKU: "HP-71"},
		ProductCategory: "electronics",
	}}
	resolver := NewResolverWithClock(reader, func() time.Time { return now })

	got, err := resolver.Resolve(ctx, "user_018", "CN", "For region US, replace AG-1042; yes, AG-1042.")
	if err != nil {
		t.Fatal(err)
	}
	want := Context{
		OrderID:         "AG-1042",
		Region:          "CN",
		ProductCategory: "electronics",
		At:              now.UTC(),
	}
	if got != want {
		t.Fatalf("context = %#v, want %#v", got, want)
	}
	if got.At.Location() != time.UTC {
		t.Fatalf("clock location = %v, want UTC", got.At.Location())
	}
	if fmt.Sprint(reader.calls) != fmt.Sprint([]resolveOrderContextCall{{userID: "user_018", orderID: "AG-1042"}}) {
		t.Fatalf("reader calls = %#v", reader.calls)
	}
	if len(reader.contexts) != 1 || reader.contexts[0] != ctx {
		t.Fatalf("reader contexts = %#v, want original context", reader.contexts)
	}
}

func TestResolverUsesExactCaseSensitiveOrderIDBoundary(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{name: "missing", message: "replace my damaged headphones"},
		{name: "lowercase prefix", message: "replace ag-1042"},
		{name: "too few digits", message: "replace AG-123"},
		{name: "embedded left", message: "replace XAG-1042"},
		{name: "embedded right", message: "replace AG-1042X"},
		{name: "underscore boundary", message: "replace _AG-1042_"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &fakeOrderContextReader{}
			got, err := NewResolverWithClock(reader, time.Now).Resolve(context.Background(), "user_018", "CN", tt.message)
			if got != (Context{}) || !errors.Is(err, ErrNeedsInput) {
				t.Fatalf("context = %#v, err = %v, want empty context and ErrNeedsInput", got, err)
			}
			if len(reader.calls) != 0 {
				t.Fatalf("reader called for invalid message: %#v", reader.calls)
			}
		})
	}
}

func TestResolverRejectsMultipleDistinctOrderIDsBeforeReadingFacts(t *testing.T) {
	reader := &fakeOrderContextReader{}

	got, err := NewResolverWithClock(reader, time.Now).Resolve(
		context.Background(), "user_018", "CN", "Compare AG-1042 with AG-1043 and AG-1042.",
	)
	if got != (Context{}) || !errors.Is(err, ErrAmbiguousOrder) {
		t.Fatalf("context = %#v, err = %v, want empty context and ErrAmbiguousOrder", got, err)
	}
	if len(reader.calls) != 0 {
		t.Fatalf("reader called for ambiguous message: %#v", reader.calls)
	}
}

func TestResolverAcceptsFourOrMoreOrderDigits(t *testing.T) {
	reader := &fakeOrderContextReader{result: commerce.OrderContext{
		Order:           commerce.Order{ID: "AG-12345", UserID: "user_018", SKU: "HP-71"},
		ProductCategory: "electronics",
	}}

	got, err := NewResolverWithClock(reader, func() time.Time { return time.Unix(0, 0) }).Resolve(
		context.Background(), "user_018", "CN", "Inspect [AG-12345].",
	)
	if err != nil || got.OrderID != "AG-12345" {
		t.Fatalf("context = %#v, err = %v", got, err)
	}
}

func TestResolverPreservesOwnershipAndProductLookupErrors(t *testing.T) {
	for _, wantErr := range []error{commerce.ErrForbidden, commerce.ErrNotFound} {
		t.Run(wantErr.Error(), func(t *testing.T) {
			reader := &fakeOrderContextReader{err: wantErr}

			got, err := NewResolverWithClock(reader, time.Now).Resolve(
				context.Background(), "user_999", "CN", "Inspect AG-1042.",
			)
			if got != (Context{}) || !errors.Is(err, wantErr) {
				t.Fatalf("context = %#v, err = %v, want empty context and %v", got, err, wantErr)
			}
			if fmt.Sprint(reader.calls) != fmt.Sprint([]resolveOrderContextCall{{userID: "user_999", orderID: "AG-1042"}}) {
				t.Fatalf("reader calls = %#v", reader.calls)
			}
		})
	}
}
