package cloud_test

import (
	"net/http"
	"strconv"
	"strings"
)

// blobFixtures publishes test blobs under the SHA-256 of their bytes, the address the cloud gives a
// file, so every download verifies against the hash it was requested by.
type blobFixtures map[string][]byte

// add publishes data and returns its address.
func (f blobFixtures) add(data string) string {
	hash := blobHash([]byte(data))
	f[hash] = []byte(data)
	return hash
}

// manifest publishes schema records, one per line, and returns the manifest's address.
func (f blobFixtures) manifest(lines ...string) string {
	return f.add(strings.Join(lines, "\n") + "\n")
}

// unpublished returns a valid address that serves nothing, for a blob a test never downloads or
// expects to be missing.
func unpublished(name string) string {
	return blobHash([]byte("unpublished " + name))
}

// serve answers a /sync/v3/files/<hash> request with the published blob, or 404 when nothing is
// published under hash. It reports false for any other path.
func (f blobFixtures) serve(w http.ResponseWriter, r *http.Request) bool {
	hash, ok := strings.CutPrefix(r.URL.Path, "/sync/v3/files/")
	if !ok {
		return false
	}
	data, ok := f[hash]
	if !ok {
		http.NotFound(w, r)
		return true
	}
	_, _ = w.Write(data)
	return true
}

// size returns the length of data as an index record lists it.
func size(data string) string {
	return strconv.Itoa(len(data))
}

// fileRecord publishes data and returns the four-field index record "hash:id:0:size" that lists it
// under id with its exact size, as real document indexes list files.
func (f blobFixtures) fileRecord(id, data string) string {
	return f.add(data) + ":" + id + ":0:" + size(data)
}
