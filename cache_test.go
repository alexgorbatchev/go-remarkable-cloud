package cloud_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestClient_BlobDiskCaching(t *testing.T) {
	var requestCount atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		switch r.URL.Path {
		case "/token/v2/user":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("mock-token"))
		case "/sync/v3/files/sample-hash":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("sample-blob-data"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	cacheDir := t.TempDir()

	client, err := cloud.NewClient(
		cloud.WithConfig(&cloud.Config{DeviceToken: "dev-token", UserToken: "user-token"}),
		cloud.WithEndpoints(&cloud.Endpoints{WebappHost: ts.URL, StorageHost: ts.URL}),
		cloud.WithCacheDir(cacheDir),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	// 1. First fetch: should make an HTTP request and save to cache
	data1, err := client.GetBlob(ctx, "sample-hash", "test.pdf")
	if err != nil {
		t.Fatalf("first GetBlob failed: %v", err)
	}
	if string(data1) != "sample-blob-data" {
		t.Fatalf("expected 'sample-blob-data', got %q", string(data1))
	}
	if requestCount.Load() != 1 {
		t.Fatalf("expected 1 HTTP request, got %d", requestCount.Load())
	}

	// Verify file is on disk
	cachedFile := filepath.Join(cacheDir, "blobs", "sample-hash")
	if _, err := os.Stat(cachedFile); err != nil {
		t.Fatalf("expected cached file at %s, got: %v", cachedFile, err)
	}

	// 2. Second fetch: should hit disk cache, 0 additional HTTP requests!
	data2, err := client.GetBlob(ctx, "sample-hash", "test.pdf")
	if err != nil {
		t.Fatalf("second GetBlob failed: %v", err)
	}
	if string(data2) != "sample-blob-data" {
		t.Fatalf("expected 'sample-blob-data', got %q", string(data2))
	}
	if requestCount.Load() != 1 {
		t.Fatalf("expected request count to remain 1 (cache hit), got %d", requestCount.Load())
	}
}
