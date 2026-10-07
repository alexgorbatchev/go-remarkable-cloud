package cloud

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// blobCacheSubdir is the directory under the cache directory that holds one file per blob hash.
	blobCacheSubdir = "blobs"

	// Cached blobs are the user's documents, so only the owner may list or read them, as with the
	// config file that WriteConfigFile writes.
	cacheDirMode  fs.FileMode = 0700
	cacheFileMode fs.FileMode = 0600

	// documentSchemaSuffix ends the file name of every document and root schema index.
	documentSchemaSuffix = ".docSchema"
)

// validateBlobHash rejects a hash that is not 64 lowercase hexadecimal digits, the form of every
// sync v3 blob address. Only a valid hash reaches a request URL or names a cache entry, so no hash a
// manifest carries can address another endpoint or a path outside the cache directory.
func validateBlobHash(hash string) error {
	valid := len(hash) == 2*sha256.Size
	for i := 0; valid && i < len(hash); i++ {
		valid = '0' <= hash[i] && hash[i] <= '9' || 'a' <= hash[i] && hash[i] <= 'f'
	}
	if !valid {
		return fmt.Errorf("%w %q: want 64 lowercase hexadecimal digits", ErrInvalidBlobHash, hash)
	}
	return nil
}

// blobMatchesHash reports whether data is the blob that hash addresses. Files and schema v4 root
// indexes are addressed by the SHA-256 of their bytes. A schema v3 index, which a filename ending in
// .docSchema names, is addressed by indexEntryHash of its records, which covers their hashes and
// order but not their other fields. Every other file must match its bytes, so a blob shaped like an
// index cannot stand in for a file whose bytes it splits into record hashes.
func blobMatchesHash(hash, filename string, data []byte) bool {
	if contentHash(data) == hash {
		return true
	}
	if !strings.HasSuffix(filename, documentSchemaSuffix) {
		return false
	}
	manifest, err := ParseManifest(hash, bytes.NewReader(data))
	if err != nil {
		return false
	}
	entryHash, err := indexEntryHash(indexOrder(manifest.Entries))
	return err == nil && entryHash == hash
}

// openBlobCache creates the blob cache directory and the cache directory above it with mode 0700,
// removes group and other permissions from a blob cache directory created by an earlier version
// with 0755, and opens it as a root that no entry name or symbolic link can leave. A blob cache
// directory closed to other users denies them every entry in it, including entries written 0644,
// without rewriting each file; the caller's own cache directory keeps its permissions.
func (c *Client) openBlobCache() (*os.Root, error) {
	dir := filepath.Join(c.cacheDir, blobCacheSubdir)
	if err := os.MkdirAll(dir, cacheDirMode); err != nil {
		return nil, err
	}
	blobs, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	info, err := blobs.Stat(".")
	if err == nil && info.Mode().Perm()&^cacheDirMode != 0 {
		err = blobs.Chmod(".", info.Mode().Perm()&cacheDirMode)
	}
	if err != nil {
		_ = blobs.Close() // the open or chmod error is returned
		return nil, err
	}
	return blobs, nil
}

// readCachedBlob returns the cache entry for hash when its bytes are the blob hash addresses. It
// removes an entry that fails verification, so a corrupted or replaced entry is downloaded again.
// An entry that cannot be read, including a symbolic link out of the cache, is a miss that the
// verified download replaces.
func readCachedBlob(blobs *os.Root, hash, filename string) ([]byte, bool) {
	data, err := blobs.ReadFile(hash)
	if err != nil {
		return nil, false
	}
	if blobMatchesHash(hash, filename, data) {
		return data, true
	}
	_ = blobs.Remove(hash) // a failed removal leaves an entry that the next download replaces
	return nil, false
}

// writeCachedBlob stores data as the entry for hash. It writes a new temporary file with mode 0600
// and renames it over the entry, so readers never see a partial entry.
func writeCachedBlob(blobs *os.Root, hash string, data []byte) error {
	tmp := hash + ".tmp." + rand.Text()
	f, err := blobs.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, cacheFileMode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = blobs.Rename(tmp, hash)
	}
	if err != nil {
		_ = blobs.Remove(tmp) // the write or rename error is returned
	}
	return err
}
