package cloud_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

// blobServer answers /sync/v3/root with rootHash and /sync/v3/files/<name> with blobs[name], and
// counts the requests for each blob name exactly as the request path spells it.
type blobServer struct {
	mu       sync.Mutex
	rootHash string
	blobs    map[string][]byte
	requests map[string]int
}

func (s *blobServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == rootPath {
		_, _ = w.Write([]byte(`{"hash":"` + s.rootHash + `","generation":1,"schemaVersion":4}`))
		return
	}
	name, ok := strings.CutPrefix(r.URL.Path, "/sync/v3/files/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.requests[name]++
	data, ok := s.blobs[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(data)
}

func (s *blobServer) count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[name]
}

func (s *blobServer) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, n := range s.requests {
		total += n
	}
	return total
}

// newBlobCacheClient returns a client that caches blobs in cacheDir and downloads them from a
// blobServer holding blobs.
func newBlobCacheClient(t *testing.T, cacheDir, rootHash string, blobs map[string][]byte) (*cloud.Client, *blobServer) {
	t.Helper()
	s := &blobServer{rootHash: rootHash, blobs: blobs, requests: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	t.Cleanup(server.Close)
	client, err := cloud.NewClient(
		cloud.WithConfig(&cloud.Config{DeviceToken: "dev-token", UserToken: "user-token"}),
		cloud.WithStorageHost(server.URL),
		cloud.WithAuthBaseURL(server.URL),
		cloud.WithSaveOnRenew(false),
		cloud.WithCacheDir(cacheDir),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	return client, s
}

// entryHashDigest computes the schema v3 index address independently of the library: the SHA-256
// of the entries' decoded hashes, given here already in entry-ID order.
func entryHashDigest(t *testing.T, orderedHashes ...string) string {
	t.Helper()
	h := sha256.New()
	for _, hash := range orderedHashes {
		raw, err := hex.DecodeString(hash)
		if err != nil {
			t.Fatal(err)
		}
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func requireMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("mode of %s = %04o, want %04o", path, got, want)
	}
}

func TestClient_BlobDiskCaching(t *testing.T) {
	payload := []byte("sample-blob-data")
	hash := blobHash(payload)
	cacheDir := t.TempDir()
	client, server := newBlobCacheClient(t, cacheDir, "", map[string][]byte{hash: payload})
	ctx := context.Background()

	// 1. First fetch: should make an HTTP request and save to cache
	data1, err := client.GetBlob(ctx, hash, "test.pdf")
	if err != nil {
		t.Fatalf("first GetBlob failed: %v", err)
	}
	if string(data1) != "sample-blob-data" {
		t.Fatalf("expected 'sample-blob-data', got %q", string(data1))
	}
	if server.total() != 1 {
		t.Fatalf("expected 1 HTTP request, got %d", server.total())
	}

	// Verify file is on disk
	cachedFile := filepath.Join(cacheDir, "blobs", hash)
	if _, err := os.Stat(cachedFile); err != nil {
		t.Fatalf("expected cached file at %s, got: %v", cachedFile, err)
	}

	// 2. Second fetch: should hit disk cache, 0 additional HTTP requests!
	data2, err := client.GetBlob(ctx, hash, "test.pdf")
	if err != nil {
		t.Fatalf("second GetBlob failed: %v", err)
	}
	if string(data2) != "sample-blob-data" {
		t.Fatalf("expected 'sample-blob-data', got %q", string(data2))
	}
	if server.total() != 1 {
		t.Fatalf("expected request count to remain 1 (cache hit), got %d", server.total())
	}
}

func TestBlobCacheRejectsManifestHashThatEscapesCache(t *testing.T) {
	const escape = "../../../planted.txt"
	rootBytes := []byte("4\n0:.:1:0\n" + escape + ":0:doc1:0:0\n")
	rootHash := blobHash(rootBytes)
	plantedBytes := []byte("3\n# planted outside the cache\n")

	for _, planted := range []bool{false, true} {
		name := "write"
		if planted {
			name = "read"
		}
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			// blobs/../../../planted.txt from base/a/b/cache resolves to base/a/planted.txt.
			cacheDir := filepath.Join(base, "a", "b", "cache")
			plantedPath := filepath.Join(base, "a", "planted.txt")
			if planted {
				if err := os.MkdirAll(filepath.Dir(plantedPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(plantedPath, plantedBytes, 0600); err != nil {
					t.Fatal(err)
				}
			}
			client, server := newBlobCacheClient(t, cacheDir, rootHash, map[string][]byte{
				rootHash: rootBytes,
				escape:   []byte("3\n"),
			})

			items, err := client.ListItems(context.Background())
			if !errors.Is(err, cloud.ErrInvalidBlobHash) {
				t.Errorf("ListItems = %d items, err %v; want ErrInvalidBlobHash", len(items), err)
			}
			if n := server.count(escape); n != 0 {
				t.Errorf("downloaded the escaping hash %d times", n)
			}
			data, readErr := os.ReadFile(plantedPath)
			switch {
			case planted && string(data) != string(plantedBytes):
				t.Errorf("planted file changed to %q (err %v)", data, readErr)
			case !planted && !errors.Is(readErr, fs.ErrNotExist):
				t.Errorf("wrote %q outside the cache directory (err %v)", data, readErr)
			}
		})
	}
}

func TestGetBlobRejectsHashesThatAreNotCacheNames(t *testing.T) {
	valid := blobHash([]byte("payload"))
	for name, hash := range map[string]string{
		"parent traversal": "../../outside",
		"absolute path":    "/etc/hosts",
		"nested name":      valid[:32] + "/" + valid[33:],
		"empty":            "",
		"uppercase":        strings.ToUpper(valid),
		"too short":        valid[:63],
		"too long":         valid + "0",
		"not hex":          "g" + valid[1:],
		"placeholder":      "sample-hash",
	} {
		t.Run(name, func(t *testing.T) {
			cacheDir := filepath.Join(t.TempDir(), "cache")
			client, server := newBlobCacheClient(t, cacheDir, "", map[string][]byte{hash: []byte("payload")})

			data, err := client.GetBlob(context.Background(), hash, "doc.pdf")
			if !errors.Is(err, cloud.ErrInvalidBlobHash) {
				t.Fatalf("GetBlob(%q) = %q, %v; want ErrInvalidBlobHash", hash, data, err)
			}
			if server.total() != 0 {
				t.Errorf("made %d requests for an invalid hash", server.total())
			}
			if _, err := os.Stat(cacheDir); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("touched the cache directory for an invalid hash: %v", err)
			}
		})
	}
}

func TestBlobCacheCreatesPrivateEntries(t *testing.T) {
	payload := []byte("handwriting")
	hash := blobHash(payload)
	cacheDir := filepath.Join(t.TempDir(), "cache")
	client, _ := newBlobCacheClient(t, cacheDir, "", map[string][]byte{hash: payload})

	if _, err := client.GetBlob(context.Background(), hash, "doc/page.rm"); err != nil {
		t.Fatal(err)
	}

	requireMode(t, cacheDir, 0700)
	requireMode(t, filepath.Join(cacheDir, "blobs"), 0700)
	requireMode(t, filepath.Join(cacheDir, "blobs", hash), 0600)
	entries, err := os.ReadDir(filepath.Join(cacheDir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != hash {
		t.Errorf("cache holds %v, want only %s", entries, hash)
	}
}

func TestBlobCacheTightensExistingDirectory(t *testing.T) {
	payload := []byte("cached by an earlier version")
	hash := blobHash(payload)
	cacheDir := t.TempDir()
	blobs := filepath.Join(cacheDir, "blobs")
	if err := os.Mkdir(blobs, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobs, hash), payload, 0600); err != nil {
		t.Fatal(err)
	}
	// Earlier versions created the directory 0755 and entries 0644.
	if err := os.Chmod(blobs, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(blobs, hash), 0644); err != nil {
		t.Fatal(err)
	}
	client, server := newBlobCacheClient(t, cacheDir, "", map[string][]byte{hash: payload})

	data, err := client.GetBlob(context.Background(), hash, "doc.pdf")
	if err != nil || string(data) != string(payload) {
		t.Fatalf("GetBlob = %q, %v", data, err)
	}
	if server.total() != 0 {
		t.Errorf("made %d requests for a verified cache entry", server.total())
	}
	requireMode(t, blobs, 0700)
}

func TestBlobCacheRefetchesEntriesThatFailVerification(t *testing.T) {
	fileBytes := []byte("cloud file bytes")
	fileHash := blobHash(fileBytes)
	pageHash := blobHash([]byte("page"))
	contentHash := blobHash([]byte("content"))
	// Written out of entry-ID order: "doc.content" sorts before "doc/page.rm".
	schemaBytes := []byte("3\n" + pageHash + ":0:doc/page.rm:0:4\n" + contentHash + ":0:doc.content:0:7\n")
	schemaHash := entryHashDigest(t, contentHash, pageHash)
	tamperedSchema := []byte("3\n" + fileHash + ":0:doc/page.rm:0:4\n" + contentHash + ":0:doc.content:0:7\n")

	tests := []struct {
		name     string
		hash     string
		filename string
		want     []byte
		seed     func(t *testing.T, entry string)
	}{
		{"tampered file", fileHash, "doc.pdf", fileBytes, func(t *testing.T, entry string) {
			if err := os.WriteFile(entry, []byte("tampered"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"empty file", fileHash, "doc.pdf", fileBytes, func(t *testing.T, entry string) {
			if err := os.WriteFile(entry, nil, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"tampered document schema", schemaHash, "doc.docSchema", schemaBytes, func(t *testing.T, entry string) {
			if err := os.WriteFile(entry, tamperedSchema, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink outside the cache", fileHash, "doc.pdf", fileBytes, func(t *testing.T, entry string) {
			outside := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(entry))), "outside")
			if err := os.WriteFile(outside, fileBytes, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, entry); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cacheDir := filepath.Join(t.TempDir(), "cache")
			if err := os.MkdirAll(filepath.Join(cacheDir, "blobs"), 0700); err != nil {
				t.Fatal(err)
			}
			entry := filepath.Join(cacheDir, "blobs", tt.hash)
			tt.seed(t, entry)
			client, server := newBlobCacheClient(t, cacheDir, "", map[string][]byte{tt.hash: tt.want})

			data, err := client.GetBlob(context.Background(), tt.hash, tt.filename)
			if err != nil || string(data) != string(tt.want) {
				t.Fatalf("GetBlob = %q, %v; want %q", data, err, tt.want)
			}
			if n := server.count(tt.hash); n != 1 {
				t.Errorf("downloaded %d times, want 1", n)
			}
			info, err := os.Lstat(entry)
			if err != nil || !info.Mode().IsRegular() {
				t.Fatalf("cache entry is not a regular file: %v, %v", info, err)
			}
			if cached, err := os.ReadFile(entry); err != nil || string(cached) != string(tt.want) {
				t.Errorf("cache entry holds %q, %v; want the downloaded bytes", cached, err)
			}
		})
	}
}

func TestBlobCacheStoresOnlyVerifiedDownloads(t *testing.T) {
	fileBytes := []byte("cloud file bytes")
	pageHash := blobHash([]byte("page"))
	contentHash := blobHash([]byte("content"))
	schemaBytes := []byte("3\n" + pageHash + ":0:doc/page.rm:0:4\n" + contentHash + ":0:doc.content:0:7\n")
	schemaHash := entryHashDigest(t, contentHash, pageHash)
	rootBytes := []byte("4\n0:.:1:11\n" + schemaHash + ":0:doc:2:11\n")
	// A schema-shaped blob whose entry hashes digest to its address: valid only for a .docSchema.
	shapedHash := entryHashDigest(t, pageHash)
	shapedBytes := []byte("3\n" + pageHash + ":0:x:0:1\n")

	tests := []struct {
		name     string
		hash     string
		filename string
		served   []byte
		cached   bool
	}{
		{"file by content hash", blobHash(fileBytes), "doc.pdf", fileBytes, true},
		{"document schema v3 by entry hashes", schemaHash, "doc.docSchema", schemaBytes, true},
		{"root schema v4 by content hash", blobHash(rootBytes), "root.docSchema", rootBytes, true},
		{"file with wrong bytes", blobHash(fileBytes), "doc.pdf", []byte("wrong bytes"), false},
		{"document schema with wrong entries", schemaHash, "doc.docSchema", rootBytes, false},
		{"schema-shaped file", shapedHash, "doc.pdf", shapedBytes, false},
		{"unparseable document schema", schemaHash, "doc.docSchema", []byte("3\nnot a record\n"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cacheDir := t.TempDir()
			client, server := newBlobCacheClient(t, cacheDir, "", map[string][]byte{tt.hash: tt.served})
			ctx := context.Background()

			for range 2 {
				data, err := client.GetBlob(ctx, tt.hash, tt.filename)
				if err != nil || string(data) != string(tt.served) {
					t.Fatalf("GetBlob = %q, %v; want the served bytes %q", data, err, tt.served)
				}
			}
			wantRequests := 2
			if tt.cached {
				wantRequests = 1
			}
			if n := server.count(tt.hash); n != wantRequests {
				t.Errorf("downloaded %d times, want %d", n, wantRequests)
			}
			_, err := os.Stat(filepath.Join(cacheDir, "blobs", tt.hash))
			if tt.cached != (err == nil) {
				t.Errorf("cache entry present = %v, want %v (%v)", err == nil, tt.cached, err)
			}
		})
	}
}

func TestBlobCacheThatCannotBeCreatedStillDownloads(t *testing.T) {
	payload := []byte("cloud file bytes")
	hash := blobHash(payload)
	// A regular file where the cache directory should be: the cache cannot be created.
	cacheDir := filepath.Join(t.TempDir(), "cache")
	if err := os.WriteFile(cacheDir, nil, 0600); err != nil {
		t.Fatal(err)
	}
	client, server := newBlobCacheClient(t, cacheDir, "", map[string][]byte{hash: payload})

	for range 2 {
		data, err := client.GetBlob(context.Background(), hash, "doc.pdf")
		if err != nil || string(data) != string(payload) {
			t.Fatalf("GetBlob = %q, %v; want the downloaded bytes", data, err)
		}
	}
	if n := server.count(hash); n != 2 {
		t.Errorf("downloaded %d times, want 2", n)
	}
}
