package tools

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Yundi218/ActionGuard/internal/toolkit"
)

const maxMCPResponseBytes int64 = 1 << 20

var trustedRequestHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"X-ActionGuard-User",
	"X-ActionGuard-Run",
	"X-ActionGuard-Step",
	"X-ActionGuard-Scopes",
	"Idempotency-Key",
}

type metadataTransport struct {
	next         http.RoundTripper
	gatewayToken string
}

func (transport metadataTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	for _, name := range trustedRequestHeaders {
		clone.Header.Del(name)
	}
	clone.Header.Set("Authorization", "Bearer "+transport.gatewayToken)
	if metadata, err := toolkit.FromContext(request.Context()); err == nil {
		clone.Header.Set("X-ActionGuard-User", metadata.UserID)
		clone.Header.Set("X-ActionGuard-Run", metadata.RunID)
		clone.Header.Set("X-ActionGuard-Step", metadata.StepID)
		clone.Header.Set("X-ActionGuard-Scopes", strings.Join(append([]string(nil), metadata.Scopes...), " "))
		if metadata.IdempotencyKey != "" {
			clone.Header.Set("Idempotency-Key", metadata.IdempotencyKey)
		}
	}
	return transport.next.RoundTrip(clone)
}

type responseLimitTransport struct {
	next http.RoundTripper
}

func (transport responseLimitTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.next.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, ErrMCPTransport
	}
	if response.ContentLength > maxMCPResponseBytes {
		_ = response.Body.Close()
		return nil, ErrMCPResponseTooLarge
	}
	response.Body = &limitedResponseBody{body: response.Body, remaining: maxMCPResponseBytes}
	return response, nil
}

type limitedResponseBody struct {
	body      io.ReadCloser
	remaining int64
	exceeded  bool
}

func (body *limitedResponseBody) Read(buffer []byte) (int, error) {
	if body.exceeded {
		return 0, ErrMCPResponseTooLarge
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if body.remaining == 0 {
		var probe [1]byte
		n, err := body.body.Read(probe[:])
		if n > 0 {
			body.exceeded = true
			return 0, ErrMCPResponseTooLarge
		}
		return 0, err
	}
	if int64(len(buffer)) > body.remaining {
		buffer = buffer[:body.remaining]
	}
	n, err := body.body.Read(buffer)
	body.remaining -= int64(n)
	return n, err
}

func (body *limitedResponseBody) Close() error {
	return body.body.Close()
}

func sanitizeTransportError(ctxErr error, err error) error {
	if ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, ErrMCPResponseTooLarge) {
		return ErrMCPResponseTooLarge
	}
	if errors.Is(err, ErrExecutorClosed) {
		return ErrExecutorClosed
	}
	return ErrMCPTransport
}
