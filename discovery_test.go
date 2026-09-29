package cloud_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestDiscoverEndpoints_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"raw_host": "https://custom.tectonic.remarkable.com",
			"webapp_host": "https://custom.webapp.remarkable.com",
			"storage_host": "https://custom.storage.remarkable.com"
		}`))
	}))
	defer server.Close()

	ctx := context.Background()
	endpoints, err := cloud.DiscoverEndpoints(ctx, server.Client(), server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if endpoints.RawHost != "https://custom.tectonic.remarkable.com" {
		t.Errorf("unexpected RawHost: %s", endpoints.RawHost)
	}
	if endpoints.WebappHost != "https://custom.webapp.remarkable.com" {
		t.Errorf("unexpected WebappHost: %s", endpoints.WebappHost)
	}
	if endpoints.StorageHost != "https://custom.storage.remarkable.com" {
		t.Errorf("unexpected StorageHost: %s", endpoints.StorageHost)
	}
}

func TestDiscoverEndpoints_FallbackOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx := context.Background()
	endpoints, err := cloud.DiscoverEndpoints(ctx, server.Client(), server.URL)
	if err == nil {
		t.Fatal("expected discovery error on 500 status")
	}

	// Fallback values should remain intact
	if endpoints.RawHost != cloud.DefaultRawHost {
		t.Errorf("expected fallback RawHost %s, got %s", cloud.DefaultRawHost, endpoints.RawHost)
	}
	if endpoints.WebappHost != cloud.DefaultWebappHost {
		t.Errorf("expected fallback WebappHost %s, got %s", cloud.DefaultWebappHost, endpoints.WebappHost)
	}
	if endpoints.StorageHost != cloud.DefaultStorageHost {
		t.Errorf("expected fallback StorageHost %s, got %s", cloud.DefaultStorageHost, endpoints.StorageHost)
	}
}
