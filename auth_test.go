package cloud_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestNewDeviceID(t *testing.T) {
	uuidRegex := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	id1 := cloud.NewDeviceID()
	id2 := cloud.NewDeviceID()

	if !uuidRegex.MatchString(id1) {
		t.Errorf("device ID %s does not match UUID v4 format", id1)
	}
	if !uuidRegex.MatchString(id2) {
		t.Errorf("device ID %s does not match UUID v4 format", id2)
	}
	if id1 == id2 {
		t.Errorf("expected distinct device IDs, got collision: %s", id1)
	}
}

func TestPairDevice_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token/json/2/device/new" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed reading request body: %v", err)
		}

		var req cloud.PairDeviceRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed unmarshaling request: %v", err)
		}

		if req.Code != "abc12345" {
			t.Errorf("expected code 'abc12345', got '%s'", req.Code)
		}
		if req.DeviceDesc != "desktop-macos" {
			t.Errorf("expected deviceDesc 'desktop-macos', got '%s'", req.DeviceDesc)
		}
		if req.DeviceID == "" {
			t.Error("expected non-empty deviceID")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`"mock-device-token-987"`))
	}))
	defer server.Close()

	ctx := context.Background()
	token, err := cloud.PairDevice(ctx, server.Client(), server.URL, "abc12345", "", "")
	if err != nil {
		t.Fatalf("unexpected error pairing device: %v", err)
	}
	if token != "mock-device-token-987" {
		t.Errorf("expected token 'mock-device-token-987', got '%s'", token)
	}
}

func TestPairDevice_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Invalid pairing code"))
	}))
	defer server.Close()

	ctx := context.Background()
	_, err := cloud.PairDevice(ctx, server.Client(), server.URL, "badcode", "", "")
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
	if !errors.Is(err, cloud.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestRenewUserToken_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token/json/2/user/new" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer my-device-token" {
			t.Errorf("expected header 'Bearer my-device-token', got '%s'", authHeader)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fresh-user-token-xyz"))
	}))
	defer server.Close()

	ctx := context.Background()
	userToken, err := cloud.RenewUserToken(ctx, server.Client(), server.URL, "my-device-token")
	if err != nil {
		t.Fatalf("unexpected error renewing user token: %v", err)
	}
	if userToken != "fresh-user-token-xyz" {
		t.Errorf("expected token 'fresh-user-token-xyz', got '%s'", userToken)
	}
}

func TestRenewUserToken_EmptyDeviceToken(t *testing.T) {
	ctx := context.Background()
	_, err := cloud.RenewUserToken(ctx, http.DefaultClient, "http://example.com", "")
	if err == nil {
		t.Fatal("expected error with empty device token, got nil")
	}
	if !errors.Is(err, cloud.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}
