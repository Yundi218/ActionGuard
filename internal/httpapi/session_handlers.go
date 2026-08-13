package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"

	"github.com/Yundi218/ActionGuard/internal/auth"
	"github.com/Yundi218/ActionGuard/internal/orchestrator"
	"github.com/go-chi/chi/v5"
)

const maxRequestBodyBytes = 1 << 20

var (
	errInvalidJSON = errors.New("invalid json request")
	errTooLarge    = errors.New("request too large")
	errMediaType   = errors.New("unsupported media type")
)

type sessionHandlers struct {
	authenticator auth.Authenticator
	sessions      SessionService
}

func (handlers sessionHandlers) createSession(writer http.ResponseWriter, request *http.Request) {
	principal, ok := handlers.authenticate(writer, request)
	if !ok {
		return
	}
	var body struct {
		Region *string `json:"region"`
	}
	if !decodeBody(writer, request, &body, "region") {
		return
	}
	if body.Region == nil {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	view, err := handlers.sessions.CreateSession(request.Context(), principal, *body.Region)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, view)
}

func (handlers sessionHandlers) runMessage(writer http.ResponseWriter, request *http.Request) {
	principal, ok := handlers.authenticate(writer, request)
	if !ok {
		return
	}
	var body struct {
		Message *string `json:"message"`
	}
	if !decodeBody(writer, request, &body, "message") {
		return
	}
	if body.Message == nil {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	view, err := handlers.sessions.RunMessage(request.Context(), principal, chi.URLParam(request, "session_id"), *body.Message)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (handlers sessionHandlers) getRun(writer http.ResponseWriter, request *http.Request) {
	principal, ok := handlers.authenticate(writer, request)
	if !ok {
		return
	}
	view, err := handlers.sessions.GetRun(request.Context(), principal, chi.URLParam(request, "run_id"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (handlers sessionHandlers) authenticate(writer http.ResponseWriter, request *http.Request) (auth.Principal, bool) {
	if handlers.authenticator == nil || handlers.sessions == nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return auth.Principal{}, false
	}
	principal, err := handlers.authenticator.Authenticate(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, "unauthenticated")
		return auth.Principal{}, false
	}
	return principal, true
}

func decodeBody(writer http.ResponseWriter, request *http.Request, destination any, allowedFields ...string) bool {
	if request.Context().Err() != nil {
		writeError(writer, http.StatusRequestTimeout, "request_canceled")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeDecodeError(writer, errMediaType)
		return false
	}
	if request.ContentLength > maxRequestBodyBytes {
		writeDecodeError(writer, errTooLarge)
		return false
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBodyBytes+1))
	if err != nil {
		if request.Context().Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			writeError(writer, http.StatusRequestTimeout, "request_canceled")
			return false
		}
		writeDecodeError(writer, errInvalidJSON)
		return false
	}
	if request.Context().Err() != nil {
		writeError(writer, http.StatusRequestTimeout, "request_canceled")
		return false
	}
	if len(data) > maxRequestBodyBytes {
		writeDecodeError(writer, errTooLarge)
		return false
	}
	if !utf8.Valid(data) || rejectDuplicateMembers(data) != nil {
		writeDecodeError(writer, errInvalidJSON)
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		writeDecodeError(writer, errInvalidJSON)
		return false
	}
	allowed := make(map[string]struct{}, len(allowedFields))
	for _, field := range allowedFields {
		allowed[field] = struct{}{}
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			writeDecodeError(writer, errInvalidJSON)
			return false
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeDecodeError(writer, errInvalidJSON)
		return false
	}
	if _, err := decoder.Token(); err != io.EOF {
		writeDecodeError(writer, errInvalidJSON)
		return false
	}
	return true
}

func rejectDuplicateMembers(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errInvalidJSON
	}
	return nil
}
func scanValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errInvalidJSON
			}
			if _, exists := seen[key]; exists {
				return errInvalidJSON
			}
			seen[key] = struct{}{}
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errInvalidJSON
		}
		return nil
	case '[':
		for decoder.More() {
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errInvalidJSON
		}
		return nil
	default:
		return errInvalidJSON
	}
}

func writeDecodeError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errTooLarge):
		writeError(writer, http.StatusRequestEntityTooLarge, "request_too_large")
	case errors.Is(err, errMediaType):
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type")
	default:
		writeError(writer, http.StatusBadRequest, "invalid_request")
	}
}
func writeServiceError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, orchestrator.ErrInvalid):
		writeError(writer, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, orchestrator.ErrNotFound):
		writeError(writer, http.StatusNotFound, "not_found")
	case errors.Is(err, orchestrator.ErrConflict):
		writeError(writer, http.StatusConflict, "state_conflict")
	case errors.Is(err, orchestrator.ErrCanceled):
		writeError(writer, http.StatusRequestTimeout, "request_canceled")
	default:
		writeError(writer, http.StatusInternalServerError, "internal_error")
	}
}
func writeError(writer http.ResponseWriter, status int, code string) {
	writeJSON(writer, status, struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}{Error: struct {
		Code string `json:"code"`
	}{Code: code}})
}
func writeJSON(writer http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		data = []byte(`{"error":{"code":"internal_error"}}`)
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(data, '\n'))
}
