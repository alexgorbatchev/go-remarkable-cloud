package cloud

import "errors"

var (
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
