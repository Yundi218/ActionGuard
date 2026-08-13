package tools

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Yundi218/ActionGuard/internal/toolkit"
)

const (
	maxMCPResponseBytes int64 = 1 << 20
	mcpCleanupTimeout         = 250 * time.Millisecond
)

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

type responseLimitState struct {
	tooLarge       atomic.Bool
	initializing   atomic.Bool
	connectContext context.Context
}

type responseLimitStateKey struct{}

func withResponseLimitState(ctx context.Context) (context.Context, *responseLimitState) {
	state := &responseLimitState{}
	return context.WithValue(ctx, responseLimitStateKey{}, state), state
}

func withConnectResponseLimitState(ctx context.Context) (context.Context, *responseLimitState) {
	state := &responseLimitState{connectContext: ctx}
	state.initializing.Store(true)
	return context.WithValue(ctx, responseLimitStateKey{}, state), state
}

func responseLimitStateFromContext(ctx context.Context) *responseLimitState {
	state, _ := ctx.Value(responseLimitStateKey{}).(*responseLimitState)
	return state
}

func (transport responseLimitTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodDelete {
		boundedRequest, cancel := boundedCleanupRequest(request)
		defer cancel()
		request = boundedRequest
	}
	response, err := transport.next.RoundTrip(request)
	if request.Method == http.MethodDelete && response != nil && response.Body != nil {
		_ = response.Body.Close()
		response.Body = http.NoBody
		response.ContentLength = 0
		response.Header.Del("Content-Length")
	}
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, ErrMCPTransport
	}
	if request.Method == http.MethodDelete {
		return response, nil
	}
	if response.ContentLength > maxMCPResponseBytes {
		markResponseTooLarge(request.Context())
		_ = response.Body.Close()
		return nil, ErrMCPResponseTooLarge
	}
	response.Body = &limitedResponseBody{
		body: response.Body, remaining: maxMCPResponseBytes,
		state: responseLimitStateFromContext(request.Context()),
	}
	return response, nil
}

func boundedCleanupRequest(request *http.Request) (*http.Request, context.CancelFunc) {
	base := context.Background()
	if state := responseLimitStateFromContext(request.Context()); state != nil && state.initializing.Load() && state.connectContext != nil {
		base = state.connectContext
	}
	ctx, cancel := context.WithTimeout(base, mcpCleanupTimeout)
	return request.Clone(ctx), cancel
}

func markResponseTooLarge(ctx context.Context) {
	if state := responseLimitStateFromContext(ctx); state != nil {
		state.tooLarge.Store(true)
	}
}

type limitedResponseBody struct {
	body      io.ReadCloser
	remaining int64
	exceeded  bool
	state     *responseLimitState
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
			if body.state != nil {
				body.state.tooLarge.Store(true)
			}
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
