package cloud_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestDiscoveryFailureCauses(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		_, err := cloud.DiscoverEndpoints(t.Context(), nil, "://invalid")
		if !errors.Is(err, cloud.ErrDiscoveryFailed) {
			t.Fatalf("request error: %v", err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		client := &http.Client{Transport: handlerTransport{http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("cancelled request reached handler") })}}
		_, err := cloud.DiscoverEndpoints(ctx, client, "https://cloud.test")
		if !errors.Is(err, cloud.ErrDiscoveryFailed) || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation cause: %v", err)
		}
	})
	t.Run("json", func(t *testing.T) {
		client := &http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{")) })}}
		_, err := cloud.DiscoverEndpoints(t.Context(), client, "https://cloud.test")
		var syntax *json.SyntaxError
		if !errors.Is(err, cloud.ErrDiscoveryFailed) || !errors.As(err, &syntax) {
			t.Fatalf("JSON cause: %v", err)
		}
	})
}

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
