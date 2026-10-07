package cloud

import (
	"fmt"
	"io"
)

const (
	// maxErrorBodySize is how many bytes of a rejected response's body a StatusError keeps. It holds a
	// service's JSON error message whole and keeps a proxy's error page out of the error text.
	maxErrorBodySize = 4 << 10

	// maxServiceResponseSize bounds the body of an auth, discovery, root state, or root commit answer,
	// which is a token or a small JSON object. Blob downloads are documents and are not bounded.
	maxServiceResponseSize = 64 << 10
)

// readAtMost reads up to limit bytes of r and reports whether r held more.
func readAtMost(r io.Reader, limit int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if int64(len(data)) > limit {
		return data[:limit], true, err
	}
	return data, false, err
}

// readServiceResponse reads the body of a successful auth, discovery, or root answer named op, and
// returns ErrResponseTooLarge instead of reading past maxServiceResponseSize.
func readServiceResponse(op string, r io.Reader) ([]byte, error) {
	data, truncated, err := readAtMost(r, maxServiceResponseSize)
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", op, err)
	}
	if truncated {
		return nil, fmt.Errorf("%w: %s response exceeds %d bytes", ErrResponseTooLarge, op, maxServiceResponseSize)
	}
	return data, nil
}
