package auth

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestStaticAuthenticatorCanonicalizesAndDefensivelyCopiesPrincipal(t *testing.T) {
	authenticator, err := NewStatic([]Credential{{Token: "token-a", Principal: Principal{UserID: "user_018", Scopes: []string{"replacement:write", "order:read", "order:read"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%#v", authenticator), "token-a") {
		t.Fatal("authenticator retained plaintext token")
	}
	req := httptest.NewRequest("GET", "/v1/runs/id", nil)
	req.Header.Set("Authorization", "Bearer token-a")
	principal, err := authenticator.Authenticate(req)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"order:read", "replacement:write"}
	if !reflect.DeepEqual(principal.Scopes, want) {
		t.Fatalf("scopes = %#v, want %#v", principal.Scopes, want)
	}
	principal.Scopes[0] = "mutated"
	again, err := authenticator.Authenticate(req)
	if err != nil || !reflect.DeepEqual(again.Scopes, want) {
		t.Fatalf("second principal = %#v, %v", again, err)
	}
}

func TestStaticAuthenticatorRejectsMalformedCredentialsWithStableError(t *testing.T) {
	authenticator, err := NewStatic([]Credential{{Token: "token-a", Principal: Principal{UserID: "user_018", Scopes: []string{"order:read"}}}})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		headers []string
	}{
		{name: "missing"}, {name: "empty", headers: []string{"Bearer "}}, {name: "wrong scheme", headers: []string{"Basic token-a"}},
		{name: "unknown", headers: []string{"Bearer unknown"}}, {name: "multiple", headers: []string{"Bearer token-a", "Bearer token-a"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			for _, value := range test.headers {
				req.Header.Add("Authorization", value)
			}
			_, err := authenticator.Authenticate(req)
			if !errors.Is(err, ErrUnauthenticated) || err != ErrUnauthenticated {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestStaticAuthenticatorScansEveryDigestAndIgnoresSpoofedIdentityHeaders(t *testing.T) {
	calls := 0
	authenticator, err := newStatic([]Credential{
		{Token: "first", Principal: Principal{UserID: "user_018", Scopes: []string{"order:read"}}},
		{Token: "second", Principal: Principal{UserID: "user_999", Scopes: []string{"order:read"}}},
	}, func(left, right []byte) bool { calls++; return string(left) == string(right) })
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer first")
	req.Header.Set("X-ActionGuard-User", "user_999")
	req.Header.Set("X-ActionGuard-Scopes", "refund:write")
	principal, err := authenticator.Authenticate(req)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("digest comparisons = %d, want 2", calls)
	}
	if principal.UserID != "user_018" || len(principal.Scopes) != 1 || principal.Scopes[0] != "order:read" {
		t.Fatalf("principal = %#v", principal)
	}
}

func TestNewStaticRejectsInvalidAndDuplicateCredentials(t *testing.T) {
	tests := [][]Credential{
		nil,
		{{Token: "", Principal: Principal{UserID: "user_018", Scopes: []string{"order:read"}}}},
		{{Token: "a,b", Principal: Principal{UserID: "user_018", Scopes: []string{"order:read"}}}},
		{{Token: "a", Principal: Principal{UserID: "", Scopes: []string{"order:read"}}}},
		{{Token: "a", Principal: Principal{UserID: " user_018", Scopes: []string{"order:read"}}}},
		{{Token: "a", Principal: Principal{UserID: "user_018", Scopes: []string{""}}}},
		{{Token: "a", Principal: Principal{UserID: "user_018", Scopes: []string{"order read"}}}},
		{{Token: "a", Principal: Principal{UserID: "user_018", Scopes: []string{"order:read"}}}, {Token: "a", Principal: Principal{UserID: "user_999", Scopes: []string{"order:read"}}}},
	}
	for index, credentials := range tests {
		if _, err := NewStatic(credentials); err == nil {
			t.Fatalf("case %d: error = nil", index)
		}
	}
}
