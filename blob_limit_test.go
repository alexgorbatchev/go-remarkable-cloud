package cloud_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

// fileFetches are the ways to read a file that a document index lists: through the cache, from a
// client without a cache directory, and fresh past a configured cache.
var fileFetches = []struct {
	name   string
	cached bool
	fetch  func(c *cloud.Client, entry cloud.SchemaEntry) ([]byte, error)
}{
	{"GetFile with cache", true, func(c *cloud.Client, entry cloud.SchemaEntry) ([]byte, error) {
		return c.GetFile(context.Background(), entry)
	}},
	{"GetFile without cache", false, func(c *cloud.Client, entry cloud.SchemaEntry) ([]byte, error) {
		return c.GetFile(context.Background(), entry)
	}},
	{"GetFileFresh", true, func(c *cloud.Client, entry cloud.SchemaEntry) ([]byte, error) {
		return c.GetFileFresh(context.Background(), entry)
	}},
}

func TestFileDownloadsAreBoundedByListedSize(t *testing.T) {
	payload := []byte("native page bytes")
	hash := blobHash(payload)
	size := int64(len(payload))
	tests := []struct {
		name      string
		listed    int64
		blobLimit int64
		want      error
	}{
		{"listed size", size, cloud.MaxBlobSize, nil},
		{"one byte over the listed size", size - 1, cloud.MaxBlobSize, cloud.ErrResponseTooLarge},
		{"listed size above the blob limit", 1 << 40, size - 1, cloud.ErrResponseTooLarge},
		{"negative listed size", -1, cloud.MaxBlobSize, cloud.ErrInvalidSchema},
	}
	for _, tt := range tests {
		for _, fetch := range fileFetches {
			t.Run(tt.name+"/"+fetch.name, func(t *testing.T) {
				cacheDir := t.TempDir()
				clientCache := cacheDir
				if !fetch.cached {
					clientCache = ""
				}
				client, _ := newBlobCacheClient(t, clientCache, "", map[string][]byte{hash: payload}, cloud.WithBlobSizeLimit(tt.blobLimit))

				data, err := fetch.fetch(client, cloud.SchemaEntry{Hash: hash, ID: "doc/page.rm", Size: tt.listed})
				if !errors.Is(err, tt.want) || tt.want == nil && string(data) != string(payload) {
					t.Fatalf("%s = %q, %v; want %v", fetch.name, data, err, tt.want)
				}
				if tt.want != nil && data != nil {
					t.Errorf("returned %q with the error", data)
				}
				_, statErr := os.Stat(filepath.Join(cacheDir, "blobs", hash))
				if cached := tt.want == nil && fetch.name == "GetFile with cache"; cached != (statErr == nil) {
					t.Errorf("cache entry present = %v, want %v", statErr == nil, cached)
				}
			})
		}
	}
}

func TestBlobDownloadsByHashAreBoundedByBlobLimit(t *testing.T) {
	payload := []byte("pdf bytes")
	hash := blobHash(payload)
	for _, limit := range []int64{int64(len(payload)), int64(len(payload)) - 1} {
		for _, fetch := range blobFetches {
			t.Run(strconv.FormatInt(limit, 10)+"/"+fetch.name, func(t *testing.T) {
				clientCache := t.TempDir()
				if !fetch.cached {
					clientCache = ""
				}
				client, _ := newBlobCacheClient(t, clientCache, "", map[string][]byte{hash: payload}, cloud.WithBlobSizeLimit(limit))

				data, err := fetch.fetch(client, hash, "doc.pdf")
				if limit < int64(len(payload)) {
					if data != nil || !errors.Is(err, cloud.ErrResponseTooLarge) {
						t.Fatalf("%s = %q, %v; want ErrResponseTooLarge", fetch.name, data, err)
					}
					return
				}
				if err != nil || string(data) != string(payload) {
					t.Fatalf("%s = %q, %v; want the blob", fetch.name, data, err)
				}
			})
		}
	}
}

func TestBlobCacheRejectsEntryLargerThanListed(t *testing.T) {
	payload := []byte("native page bytes")
	hash := blobHash(payload)
	cacheDir := t.TempDir()
	entry := filepath.Join(cacheDir, "blobs", hash)
	if err := os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
		t.Fatal(err)
	}
	// The genuine bytes, but the index lists one byte fewer.
	if err := os.WriteFile(entry, payload, 0600); err != nil {
		t.Fatal(err)
	}
	client, server := newBlobCacheClient(t, cacheDir, "", map[string][]byte{hash: payload})

	data, err := client.GetFile(context.Background(), cloud.SchemaEntry{Hash: hash, ID: "doc/page.rm", Size: int64(len(payload)) - 1})
	if data != nil || !errors.Is(err, cloud.ErrResponseTooLarge) || server.count(hash) != 1 {
		t.Fatalf("GetFile = %q, %v after %d downloads; want ErrResponseTooLarge from the download", data, err, server.count(hash))
	}
	if _, err := os.Lstat(entry); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the oversized cache entry is still present: %v", err)
	}
}

// TestDocumentFileReadsAreBoundedByListedSize lists a document's metadata and content one byte
// shorter than the bytes the server holds, so listing and content reads must refuse them.
func TestDocumentFileReadsAreBoundedByListedSize(t *testing.T) {
	metadata := `{"visibleName":"Doc","type":"DocumentType"}`
	content := `{"fileType":"pdf","pageCount":1}`
	for _, short := range []string{"metadata", "content"} {
		t.Run(short, func(t *testing.T) {
			metaSize, contentSize := len(metadata), len(content)
			if short == "metadata" {
				metaSize--
			} else {
				contentSize--
			}
			blobs := blobFixtures{}
			schema := blobs.manifest(
				blobs.add(metadata)+":0:doc.metadata:0:"+strconv.Itoa(metaSize),
				blobs.add(content)+":0:doc.content:0:"+strconv.Itoa(contentSize),
			)
			root := blobs.manifest("4", "0:.:1:0", schema+":0:doc:2:0")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == rootPath {
					_, _ = w.Write([]byte(`{"hash":"` + root + `","generation":1}`))
					return
				}
				blobs.serve(w, r)
			}))
			t.Cleanup(server.Close)
			client, err := cloud.NewClient(cloud.WithStorageHost(server.URL), cloud.WithConfig(&cloud.Config{UserToken: "user"}))
			if err != nil {
				t.Fatal(err)
			}

			items, err := client.ListItems(context.Background())
			if short == "metadata" {
				if !errors.Is(err, cloud.ErrResponseTooLarge) {
					t.Fatalf("ListItems = %v, %v; want ErrResponseTooLarge", items, err)
				}
				return
			}
			if err != nil || len(items) != 1 {
				t.Fatalf("ListItems = %v, %v", items, err)
			}
			if _, err := items[0].GetContent(context.Background()); !errors.Is(err, cloud.ErrResponseTooLarge) {
				t.Fatalf("GetContent error = %v, want ErrResponseTooLarge", err)
			}
		})
	}
}
