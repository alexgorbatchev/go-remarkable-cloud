package cloud

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validBlobHash(hash string) bool {
	if len(hash) != sha256.Size*2 {
		return false
	}
	for _, c := range hash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func verifyBlob(hash, filename string, data []byte) error {
	actual := contentHash(data)
	// Schema v3 roots use the same child-hash digest as document schemas.
	// Schema v4 roots instead hash their complete serialized bytes.
	v3 := bytes.HasPrefix(data, []byte("3\n"))
	if strings.HasSuffix(filename, ".docSchema") && (filename != "root.docSchema" || v3) {
		manifest, err := ParseManifest(hash, bytes.NewReader(data))
		if err != nil {
			return err
		}
		_, actual, _, err = encodeCloudManifest(manifest.Entries, false)
		if err != nil {
			return fmt.Errorf("verify document manifest: %w", err)
		}
	}
	if actual != hash {
		return fmt.Errorf("%w: blob checksum mismatch", ErrInvalidSchema)
	}
	return nil
}

func readCachedBlob(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readBoundedBody(f, maxBlobBody)
}

func writeCachedBlob(dir, hash string, data []byte) error {
	for _, path := range []string{dir, filepath.Join(dir, "blobs")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		if err := os.Chmod(path, 0700); err != nil {
			return err
		}
	}
	dir = filepath.Join(dir, "blobs")
	f, err := os.CreateTemp(dir, ".blob-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, hash))
}
