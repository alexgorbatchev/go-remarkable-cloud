package cloud

import (
	"fmt"
	"io"
)

const (
	maxStatusBody  = 4 * 1024
	maxControlBody = 1024 * 1024
	maxBlobBody    = 512 * 1024 * 1024
)

func readBoundedBody(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds %d-byte limit", limit)
	}
	return body, nil
}
