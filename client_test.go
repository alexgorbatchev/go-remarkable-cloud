package cloud_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestClient_GetRootState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/v3/root" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer valid-user-token" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hash": "root-schema-hash", "generation": 42, "schemaVersion": 3}`))
	}))
	defer server.Close()

	client, err := cloud.NewClient(
		cloud.WithStorageHost(server.URL),
		cloud.WithConfig(&cloud.Config{
			UserToken: "valid-user-token",
		}),
	)
	if err != nil {
		t.Fatalf("failed creating client: %v", err)
	}

	ctx := context.Background()
	rootState, err := client.GetRootState(ctx)
	if err != nil {
		t.Fatalf("unexpected error getting root state: %v", err)
	}

	if rootState.Hash != "root-schema-hash" {
		t.Errorf("expected hash 'root-schema-hash', got '%s'", rootState.Hash)
	}
	if rootState.Generation != 42 {
		t.Errorf("expected generation 42, got %d", rootState.Generation)
	}
	if rootState.SchemaVersion != 3 {
		t.Errorf("expected schemaVersion 3, got %d", rootState.SchemaVersion)
	}
}

func TestClient_GetManifestAndBlob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/v3/files/file-hash-123" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		filename := r.Header.Get("rm-filename")
		switch filename {
		case "root", "root.docSchema":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("meta-hash:doc1.metadata:0:120\ncontent-hash:doc1.content:0:450\n"))
		case "doc1.content":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"fileType": "pdf", "pageCount": 10}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("raw-blob-content"))
		}
	}))
	defer server.Close()

	client, err := cloud.NewClient(
		cloud.WithStorageHost(server.URL),
		cloud.WithConfig(&cloud.Config{
			UserToken: "test-token",
		}),
	)
	if err != nil {
		t.Fatalf("failed creating client: %v", err)
	}

	ctx := context.Background()

	// Test GetManifest
	manifest, err := client.GetManifest(ctx, "file-hash-123", "root")
	if err != nil {
		t.Fatalf("unexpected error getting manifest: %v", err)
	}
	if len(manifest.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(manifest.Entries))
	}

	// Test GetBlob
	blob, err := client.GetBlob(ctx, "file-hash-123", "custom.bin")
	if err != nil {
		t.Fatalf("unexpected error getting blob: %v", err)
	}
	if string(blob) != "raw-blob-content" {
		t.Errorf("expected 'raw-blob-content', got '%s'", string(blob))
	}

	// Test GetDocumentContent
	dc, err := client.GetDocumentContent(ctx, "file-hash-123", "doc1.content")
	if err != nil {
		t.Fatalf("unexpected error getting document content: %v", err)
	}
	if dc.FileType != "pdf" || dc.PageCount != 10 {
		t.Errorf("unexpected parsed content: %+v", dc)
	}
}

func TestClient_AutoRenewTokenOn401(t *testing.T) {
	var renewCalls int32
	var storageCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token/json/2/user/new":
			atomic.AddInt32(&renewCalls, 1)
			if r.Header.Get("Authorization") != "Bearer my-device-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("renewed-user-token"))

		case "/sync/v3/root":
			call := atomic.AddInt32(&storageCalls, 1)
			auth := r.Header.Get("Authorization")
			if call == 1 {
				// First call fails with 401
				if auth != "Bearer stale-user-token" {
					t.Errorf("expected stale token on first call, got %s", auth)
				}
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// Second call should have renewed token
			if auth != "Bearer renewed-user-token" {
				t.Errorf("expected renewed token on second call, got %s", auth)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"hash": "new-root", "generation": 1, "schemaVersion": 3}`))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := cloud.NewClient(
		cloud.WithAuthBaseURL(server.URL),
		cloud.WithStorageHost(server.URL),
		cloud.WithAutoRenew(true),
		cloud.WithSaveOnRenew(false),
		cloud.WithConfig(&cloud.Config{
			DeviceToken: "my-device-token",
			UserToken:   "stale-user-token",
		}),
	)
	if err != nil {
		t.Fatalf("failed creating client: %v", err)
	}

	ctx := context.Background()
	rootState, err := client.GetRootState(ctx)
	if err != nil {
		t.Fatalf("unexpected error after auto-renewal: %v", err)
	}

	if rootState.Hash != "new-root" {
		t.Errorf("expected hash 'new-root', got '%s'", rootState.Hash)
	}
	if atomic.LoadInt32(&renewCalls) != 1 {
		t.Errorf("expected exactly 1 renew call, got %d", renewCalls)
	}
	if atomic.LoadInt32(&storageCalls) != 2 {
		t.Errorf("expected 2 storage calls, got %d", storageCalls)
	}
	if client.Config().UserToken != "renewed-user-token" {
		t.Errorf("expected client config updated to renewed token, got %s", client.Config().UserToken)
	}
}

func TestClient_RenewalFailureErrorText(t *testing.T) {
	tests := []struct {
		name        string
		renewStatus int
		renewBody   string
		userToken   string
		call        func(context.Context, *cloud.Client) error
		want        string
	}{
		{
			name:        "RenewToken rejected with 401",
			renewStatus: http.StatusUnauthorized,
			renewBody:   "invalid Authorization header",
			call: func(ctx context.Context, c *cloud.Client) error {
				_, err := c.RenewToken(ctx)
				return err
			},
			want: "unauthorized: missing or invalid credentials: renew user token failed with status 401: invalid Authorization header",
		},
		{
			name:        "GetRootState minting a missing user token rejected with 401",
			renewStatus: http.StatusUnauthorized,
			renewBody:   "invalid Authorization header",
			call: func(ctx context.Context, c *cloud.Client) error {
				_, err := c.GetRootState(ctx)
				return err
			},
			want: "unauthorized: missing or invalid credentials: renew user token failed with status 401: invalid Authorization header",
		},
		{
			name:        "GetRootState renewing a stale user token rejected with 401",
			renewStatus: http.StatusUnauthorized,
			renewBody:   "invalid Authorization header",
			userToken:   "stale-user-token",
			call: func(ctx context.Context, c *cloud.Client) error {
				_, err := c.GetRootState(ctx)
				return err
			},
			want: "auth renewal failed after 401: unauthorized: missing or invalid credentials: renew user token failed with status 401: invalid Authorization header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token/json/2/user/new":
					w.WriteHeader(tt.renewStatus)
					_, _ = w.Write([]byte(tt.renewBody))
				case "/sync/v3/root":
					w.WriteHeader(http.StatusUnauthorized)
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			client, err := cloud.NewClient(
				cloud.WithAuthBaseURL(server.URL),
				cloud.WithStorageHost(server.URL),
				cloud.WithSaveOnRenew(false),
				cloud.WithConfig(&cloud.Config{
					DeviceToken: "rejected-device-token",
					UserToken:   tt.userToken,
				}),
			)
			if err != nil {
				t.Fatalf("failed creating client: %v", err)
			}

			err = tt.call(context.Background(), client)
			if !errors.Is(err, cloud.ErrUnauthorized) {
				t.Fatalf("expected ErrUnauthorized, got %v", err)
			}
			if err.Error() != tt.want {
				t.Fatalf("error text mismatch\n got: %q\nwant: %q", err.Error(), tt.want)
			}
		})
	}
}
