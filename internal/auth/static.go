package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"sort"
	"strings"
)

type Credential struct {
	Token     string
	Principal Principal
}

type digestEntry struct {
	digest    [sha256.Size]byte
	principal Principal
}

type digestCompare func([]byte, []byte) bool

type Static struct {
	entries []digestEntry
	equal   digestCompare
}

func NewStatic(credentials []Credential) (*Static, error) {
	return newStatic(credentials, func(left, right []byte) bool {
		return subtle.ConstantTimeCompare(left, right) == 1
	})
}

func newStatic(credentials []Credential, equal digestCompare) (*Static, error) {
	if len(credentials) == 0 || equal == nil {
		return nil, errors.New("invalid static credentials")
	}
	entries := make([]digestEntry, len(credentials))
	seen := make(map[[sha256.Size]byte]struct{}, len(credentials))
	for index, credential := range credentials {
		if !validBearerToken(credential.Token) || !validIdentifier(credential.Principal.UserID, false) {
			return nil, errors.New("invalid static credentials")
		}
		digest := sha256.Sum256([]byte(credential.Token))
		if _, exists := seen[digest]; exists {
			return nil, errors.New("invalid static credentials")
		}
		seen[digest] = struct{}{}
		scopes, ok := canonicalScopes(credential.Principal.Scopes)
		if !ok {
			return nil, errors.New("invalid static credentials")
		}
		entries[index] = digestEntry{digest: digest, principal: Principal{UserID: credential.Principal.UserID, Scopes: scopes}}
	}
	return &Static{entries: entries, equal: equal}, nil
}

func (authenticator *Static) Authenticate(request *http.Request) (Principal, error) {
	if authenticator == nil || request == nil || len(request.Header.Values("Authorization")) != 1 {
		return Principal{}, ErrUnauthenticated
	}
	value := request.Header.Values("Authorization")[0]
	parts := strings.Split(value, " ")
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !validBearerToken(parts[1]) {
		return Principal{}, ErrUnauthenticated
	}
	digest := sha256.Sum256([]byte(parts[1]))
	matched := -1
	for index := range authenticator.entries {
		if authenticator.equal(digest[:], authenticator.entries[index].digest[:]) {
			matched = index
		}
	}
	if matched < 0 {
		return Principal{}, ErrUnauthenticated
	}
	principal := authenticator.entries[matched].principal
	principal.Scopes = append([]string(nil), principal.Scopes...)
	return principal, nil
}

func canonicalScopes(scopes []string) ([]string, bool) {
	if len(scopes) == 0 {
		return nil, false
	}
	result := append([]string(nil), scopes...)
	for _, scope := range result {
		if !validIdentifier(scope, true) {
			return nil, false
		}
	}
	sort.Strings(result)
	result = compact(result)
	return result, true
}

func validBearerToken(token string) bool {
	if token == "" || len(token) > 4096 {
		return false
	}
	for _, character := range token {
		if character <= 0x20 || character > 0x7e || character == ',' {
			return false
		}
	}
	return true
}

func validIdentifier(value string, allowColon bool) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' || allowColon && character == ':' {
			continue
		}
		return false
	}
	return true
}

func compact(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

var _ Authenticator = (*Static)(nil)
