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

	// ErrAmbiguousName is matched by every AmbiguousNameError. It never matches ErrItemNotFound.
	ErrAmbiguousName = errors.New("ambiguous item name")

	// ErrInvalidSchema is returned when a manifest or schema file is malformed.
	ErrInvalidSchema = errors.New("invalid schema format")

	// ErrDiscoveryFailed is returned when service discovery fails.
	ErrDiscoveryFailed = errors.New("service discovery failed")
)

// StatusError reports an HTTP response whose status the requested operation does not accept.
// Callers read the status with errors.As. When the status has a domain meaning, Err holds the
// matching sentinel so errors.Is also reports it: ErrItemNotFound for a missing blob,
// ErrGenerationConflict for a 409 or 412 root commit, ErrDiscoveryFailed for every discovery
// status including 401 and 403, and ErrUnauthorized for 401 and 403 from every other request.
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

// AmbiguousNameError reports that a visible name, or one segment of a path, matches more than one
// live item, so name and path resolution cannot choose between them. Its Error text states only
// the name and the candidate count; callers read and render the candidates with errors.As.
// errors.Is matches ErrAmbiguousName.
type AmbiguousNameError struct {
	// Query is the name or path the caller asked to resolve, exactly as passed to the resolver.
	Query string
	// Name is the visible name that matched several items: the whole query for a name lookup, or
	// the ambiguous segment of a path.
	Name string
	// Candidates holds every live item that matched Name, in the order described on
	// AmbiguousCandidate. A path lookup stops at its first ambiguous segment, so these are the
	// candidates for that segment only.
	Candidates []AmbiguousCandidate
}

// AmbiguousCandidate is one live item that matched an ambiguous name.
//
// Candidates are ordered with reachable items first, then by FolderPath, then by Item.ID, each
// compared byte-wise. The order does not depend on the order in which the listing returned items.
type AmbiguousCandidate struct {
	// Item is the matching item. Item.ID selects it unambiguously.
	Item *Item
	// FolderPath joins with "/" the visible names of the live collections above the item, from the
	// top level down to its parent: the segments ResolveByPath walks before the item's own name.
	// It is empty for an item at the root.
	FolderPath string
	// Unreachable reports that the walk toward the root reached a parent that is not a live
	// collection: a trashed or deleted folder, an ID absent from the listing, a document, or a
	// parent loop. ResolveByPath cannot reach such an item, and FolderPath then holds only the live
	// collections below that parent, empty when the item's own parent is the one that broke the walk.
	// A reachable candidate's FolderPath plus its name can still be ambiguous: another item in the
	// same folder can share its name, or another folder beside one of its ancestors can share that
	// ancestor's name. Only Item.ID always selects one candidate.
	Unreachable bool
}

// Error names the ambiguous name, the query when it differs, and the number of candidates, for
// example `ambiguous item name: "Notes" in "Work/Notes" matches 2 items`. It does not list the
// candidates: callers that show them read Candidates with errors.As and render them in their own
// format.
func (e *AmbiguousNameError) Error() string {
	msg := fmt.Sprintf("%s: %q", ErrAmbiguousName, e.Name)
	if e.Query != e.Name {
		msg += fmt.Sprintf(" in %q", e.Query)
	}
	return msg + fmt.Sprintf(" matches %d items", len(e.Candidates))
}

// Unwrap returns ErrAmbiguousName so errors.Is classifies every ambiguity the same way.
func (e *AmbiguousNameError) Unwrap() error {
	return ErrAmbiguousName
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
