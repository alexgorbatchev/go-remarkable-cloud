package cloud_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestClient_OptionsAndAccessors(t *testing.T) {
	customHTTP := &http.Client{}
	endpoints := cloud.DefaultEndpoints()
	endpoints.RawHost = "https://example-raw.com"

	c, err := cloud.NewClient(
		cloud.WithHTTPClient(customHTTP),
		cloud.WithEndpoints(endpoints),
		cloud.WithConfigFile("/custom/test/.rmapi"),
		cloud.WithStorageHost("https://example-storage.com"),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c.Endpoints().RawHost != "https://example-raw.com" {
		t.Errorf("unexpected raw host: %s", c.Endpoints().RawHost)
	}
	if c.Endpoints().StorageHost != "https://example-storage.com" {
		t.Errorf("unexpected storage host: %s", c.Endpoints().StorageHost)
	}
}

func TestClient_PairDevice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token/json/2/device/new":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("mock-paired-device-token"))
		case "/token/json/2/user/new":
			if r.Header.Get("Authorization") != "Bearer mock-paired-device-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("mock-minted-user-token"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c, err := cloud.NewClient(
		cloud.WithAuthBaseURL(server.URL),
		cloud.WithAutoRenew(false),
	)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}

	ctx := context.Background()
	userToken, err := c.PairDevice(ctx, "pair1234")
	if err != nil {
		t.Fatalf("unexpected error pairing device: %v", err)
	}

	if userToken != "mock-minted-user-token" {
		t.Errorf("expected userToken 'mock-minted-user-token', got '%s'", userToken)
	}
	if c.Config().DeviceToken != "mock-paired-device-token" {
		t.Errorf("expected deviceToken 'mock-paired-device-token', got '%s'", c.Config().DeviceToken)
	}
	if c.Config().UserToken != "mock-minted-user-token" {
		t.Errorf("expected userToken 'mock-minted-user-token', got '%s'", c.Config().UserToken)
	}
}

func TestClient_RenewToken_NoDeviceToken(t *testing.T) {
	c, _ := cloud.NewClient()
	ctx := context.Background()
	_, err := c.RenewToken(ctx)
	if !errors.Is(err, cloud.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestClient_SaveOnRenew(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, ".rmapi")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("new-user-token-saved"))
	}))
	defer server.Close()

	c, _ := cloud.NewClient(
		cloud.WithAuthBaseURL(server.URL),
		cloud.WithConfigFile(confPath),
		cloud.WithSaveOnRenew(true),
		cloud.WithConfig(&cloud.Config{DeviceToken: "dev-tok"}),
	)

	ctx := context.Background()
	newToken, err := c.RenewToken(ctx)
	if err != nil {
		t.Fatalf("failed renewing: %v", err)
	}
	if newToken != "new-user-token-saved" {
		t.Errorf("expected 'new-user-token-saved', got '%s'", newToken)
	}

	savedCfg, err := cloud.ReadConfigFile(confPath)
	if err != nil {
		t.Fatalf("failed reading saved config file: %v", err)
	}
	if savedCfg.UserToken != "new-user-token-saved" {
		t.Errorf("saved user token mismatch: got %s", savedCfg.UserToken)
	}
}

func TestClient_GetErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/v3/root":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("internal root error"))
		case "/sync/v3/files/notfound":
			w.WriteHeader(http.StatusNotFound)
		case "/sync/v3/files/badmanifest":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("invalid:format"))
		case "/sync/v3/files/servererror":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("gateway error"))
		}
	}))
	defer server.Close()

	c, _ := cloud.NewClient(
		cloud.WithStorageHost(server.URL),
		cloud.WithConfig(&cloud.Config{UserToken: "test"}),
	)

	ctx := context.Background()

	// Root error
	_, err := c.GetRootState(ctx)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 error getting root state, got %v", err)
	}

	// 404 manifest
	_, err = c.GetManifest(ctx, "notfound", "")
	if !errors.Is(err, cloud.ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound, got %v", err)
	}

	// Invalid manifest schema
	_, err = c.GetManifest(ctx, "badmanifest", "")
	if !errors.Is(err, cloud.ErrInvalidSchema) {
		t.Errorf("expected ErrInvalidSchema, got %v", err)
	}

	// 502 manifest
	_, err = c.GetManifest(ctx, "servererror", "")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("expected 502 error getting manifest, got %v", err)
	}

	// 404 blob
	_, err = c.GetBlob(ctx, "notfound", "file.bin")
	if !errors.Is(err, cloud.ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound, got %v", err)
	}

	// 502 blob
	_, err = c.GetBlob(ctx, "servererror", "file.bin")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("expected 502 error getting blob, got %v", err)
	}
}

func TestLoadConfig(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, ".rmapi")
	_ = cloud.WriteConfigFile(confPath, &cloud.Config{
		DeviceToken: "d1",
		UserToken:   "u1",
	})

	cfg, path, err := cloud.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("unexpected error loading config: %v", err)
	}
	if path != confPath {
		t.Errorf("expected path %s, got %s", confPath, path)
	}
	if cfg.DeviceToken != "d1" || cfg.UserToken != "u1" {
		t.Errorf("unexpected config loaded: %+v", cfg)
	}
}

func TestItem_EdgeCases(t *testing.T) {
	ctx := context.Background()

	// Unbound item
	item := &cloud.Item{ID: "orphan"}
	if item.Client() != nil {
		t.Error("expected nil client on unbound item")
	}

	_, err := item.GetManifest(ctx)
	if err == nil {
		t.Error("expected error for unbound item manifest")
	}
	_, err = item.GetContent(ctx)
	if err == nil {
		t.Error("expected error for unbound item content")
	}

	// Bound client but no schema hash or content entry
	c, _ := cloud.NewClient()
	boundItem := &cloud.Item{
		ID: "no-hash",
	}
	boundItem.BindClient(c)
	_, err = boundItem.GetManifest(ctx)
	if !errors.Is(err, cloud.ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound, got %v", err)
	}
	_, err = boundItem.GetContent(ctx)
	if !errors.Is(err, cloud.ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound, got %v", err)
	}
}

func TestResolver_EmptyQueries(t *testing.T) {
	c, _ := cloud.NewClient()
	ctx := context.Background()

	_, err := c.ResolveByPath(ctx, "   ")
	if !errors.Is(err, cloud.ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound for empty path, got %v", err)
	}

	_, err = c.Resolve(ctx, "")
	if !errors.Is(err, cloud.ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound for empty query, got %v", err)
	}
}

func TestWriteNilGuards(t *testing.T) {
	if err := cloud.WriteConfig(nil, nil); !errors.Is(err, cloud.ErrInvalidConfig) {
		t.Errorf("expected ErrInvalidConfig, got %v", err)
	}
	if err := cloud.WriteManifest(nil, nil); !errors.Is(err, cloud.ErrInvalidSchema) {
		t.Errorf("expected ErrInvalidSchema, got %v", err)
	}
}

func TestAutoLoadConfigOnNewClient(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, ".rmapi")
	_ = cloud.WriteConfigFile(confPath, &cloud.Config{
		DeviceToken: "auto-device",
		UserToken:   "auto-user",
	})

	c, err := cloud.NewClient(cloud.WithConfigFile(confPath))
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}
	if c.Config().DeviceToken != "auto-device" || c.Config().UserToken != "auto-user" {
		t.Errorf("unexpected auto-loaded config: %+v", c.Config())
	}
}

func TestPairDevice_ServerErrors(t *testing.T) {
	ctx := context.Background()

	// 500 error
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server crashed"))
	}))
	defer server500.Close()

	_, err := cloud.PairDevice(ctx, server500.Client(), server500.URL, "code", "desc", "id")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 error, got %v", err)
	}

	// Empty token returned
	serverEmpty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`""`))
	}))
	defer serverEmpty.Close()

	_, err = cloud.PairDevice(ctx, serverEmpty.Client(), serverEmpty.URL, "code", "desc", "id")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected empty token error, got %v", err)
	}
}

func TestRenewUserToken_ServerErrors(t *testing.T) {
	ctx := context.Background()

	// 401 error
	server401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server401.Close()

	_, err := cloud.RenewUserToken(ctx, server401.Client(), server401.URL, "tok")
	if !errors.Is(err, cloud.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}

	// 500 error
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("renew crashed"))
	}))
	defer server500.Close()

	_, err = cloud.RenewUserToken(ctx, server500.Client(), server500.URL, "tok")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 error, got %v", err)
	}

	// Empty token returned
	serverEmpty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("   "))
	}))
	defer serverEmpty.Close()

	_, err = cloud.RenewUserToken(ctx, serverEmpty.Client(), serverEmpty.URL, "tok")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected empty token error, got %v", err)
	}
}

func TestDiscoverEndpoints_ServiceManagerKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"service-manager": "custom.tectonic.internal.net"
		}`))
	}))
	defer server.Close()

	ctx := context.Background()
	endpoints, err := cloud.DiscoverEndpoints(ctx, server.Client(), server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if endpoints.RawHost != "https://custom.tectonic.internal.net" {
		t.Errorf("expected RawHost from service-manager key, got %s", endpoints.RawHost)
	}
}

func TestManifest_ParseErrors(t *testing.T) {
	// Subfiles not an integer
	_, err := cloud.ParseManifest("h", strings.NewReader("hash:id:notanint:100\n"))
	if !errors.Is(err, cloud.ErrInvalidSchema) {
		t.Errorf("expected ErrInvalidSchema for invalid subfiles, got %v", err)
	}

	// Size not an integer
	_, err = cloud.ParseManifest("h", strings.NewReader("hash:id:0:notanint\n"))
	if !errors.Is(err, cloud.ErrInvalidSchema) {
		t.Errorf("expected ErrInvalidSchema for invalid size, got %v", err)
	}
}
