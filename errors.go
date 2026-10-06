package cloud

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

var (
	// ErrGenerationConflict means another client changed the cloud root before commit.
	ErrGenerationConflict = errors.New("cloud root generation conflict")

	// ErrConfigNotFound is returned when no rmapi config file can be located.
	ErrConfigNotFound = errors.New("rmapi config file not found")

	// ErrInvalidConfig is returned when a config file cannot be parsed.
	ErrInvalidConfig = errors.New("invalid rmapi config")

	// ErrUnauthorized is returned when an auth token is missing, invalid, or expired.
	ErrUnauthorized = errors.New("unauthorized: missing or invalid credentials")

	// ErrItemNotFound is returned when a document, collection, or blob does not exist.
	ErrItemNotFound = errors.New("item not found")

	// ErrInvalidSchema is returned when a manifest or schema file is malformed.
	ErrInvalidSchema = errors.New("invalid schema format")

	// ErrDiscoveryFailed is returned when service discovery fails.
	ErrDiscoveryFailed = errors.New("service discovery failed")
)

// StatusError reports an HTTP response whose status the requested operation does not accept.
// Callers read the status with errors.As. When the status has a domain meaning, Err holds the
// matching sentinel so errors.Is also reports it: ErrUnauthorized for 401 and 403,
// ErrItemNotFound for a missing blob, ErrGenerationConflict for a rejected root commit, and
// ErrDiscoveryFailed for every discovery failure.
type StatusError struct {
	// Op names the request that failed, such as "get root state".
	Op string
	// StatusCode is the HTTP response status code.
	StatusCode int
	// Body is the response body exactly as the server sent it.
	Body string
	// Err is the sentinel classification for the status, or nil when it has none.
	Err error
}

// Error returns the classification, operation, status code, and server response body.
func (e *StatusError) Error() string {
	msg := fmt.Sprintf("%s failed with status %d", e.Op, e.StatusCode)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	if e.Err != nil {
		return e.Err.Error() + ": " + msg
	}
	return msg
}

// Unwrap returns the sentinel classification so errors.Is matches it.
func (e *StatusError) Unwrap() error {
	return e.Err
}

// newStatusError classifies a rejected response; 401 and 403 always mean the credentials were refused.
func newStatusError(op string, statusCode int, body []byte) *StatusError {
	e := &StatusError{Op: op, StatusCode: statusCode, Body: string(body)}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		e.Err = ErrUnauthorized
	}
	return e
}

// readStatusError reads the rejected response body and classifies the response.
func readStatusError(op string, resp *http.Response) *StatusError {
	// The status code already decides the failure; a body read error only loses the server reason.
	body, _ := io.ReadAll(resp.Body)
	return newStatusError(op, resp.StatusCode, body)
}
