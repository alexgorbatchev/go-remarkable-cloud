package cloud_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func fixtureHash(data string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(data)))
}

// blobFixture builds protocol-addressed test data independently of the client encoder.
type blobFixture map[string]string

func (b blobFixture) add(id, data string) cloud.SchemaEntry {
	hash := fixtureHash(data)
	b[hash] = data
	return cloud.SchemaEntry{Hash: hash, ID: id, Size: int64(len(data))}
}

func (b blobFixture) manifest(id string, root bool, entries ...cloud.SchemaEntry) cloud.SchemaEntry {
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b cloud.SchemaEntry) int { return strings.Compare(a.ID, b.ID) })
	var data strings.Builder
	var hashes []byte
	for _, entry := range entries {
		fmt.Fprintf(&data, "%s:%s:%d:%d\n", entry.Hash, entry.ID, entry.Subfiles, entry.Size)
		hash, err := hex.DecodeString(entry.Hash)
		if err != nil {
			panic(err)
		}
		hashes = append(hashes, hash...)
	}
	hash := fixtureHash(string(hashes))
	if root {
		hash = fixtureHash(data.String())
	}
	b[hash] = data.String()
	return cloud.SchemaEntry{Hash: hash, ID: id, Subfiles: len(entries), Size: int64(data.Len())}
}

func (b blobFixture) handler(t *testing.T, root string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sync/v3/root" {
			_, _ = fmt.Fprintf(w, `{"hash":%q,"generation":1,"schemaVersion":4}`, root)
			return
		}
		data, ok := b[strings.TrimPrefix(r.URL.Path, "/sync/v3/files/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if _, err := fmt.Fprint(w, data); err != nil {
			t.Error(err)
		}
	}
}

func decodedFixtureHash(t *testing.T, hash string) []byte {
	t.Helper()
	data, err := hex.DecodeString(hash)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// handlerTransport exercises request/response behavior without allocating TCP ports.
// Tests of transport failures and cancellation use actual servers separately.
type handlerTransport struct{ handler http.Handler }

func (tr handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	recorder := httptest.NewRecorder()
	tr.handler.ServeHTTP(recorder, req)
	return recorder.Result(), nil
}
