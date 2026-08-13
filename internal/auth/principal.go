package auth

import (
	"errors"
	"net/http"
)

var ErrUnauthenticated = errors.New("unauthenticated")

type Principal struct {
	UserID string
	Scopes []string
}

type Authenticator interface {
	Authenticate(*http.Request) (Principal, error)
}
