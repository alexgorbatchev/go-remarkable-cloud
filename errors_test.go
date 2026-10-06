package cloud_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

// cannedResponse is the status and body an httptest route answers with.
type cannedResponse struct {
	status int
	body   string
}

// newCannedServer answers each request path with its canned response and fails on unknown paths.
func newCannedServer(t *testing.T, routes map[string]cannedResponse) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.WriteHeader(route.status)
		if _, err := fmt.Fprint(w, route.body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// newCannedClient builds a client whose auth and storage requests go to one canned server.
func newCannedClient(t *testing.T, routes map[string]cannedResponse, cfg cloud.Config, opts ...cloud.Option) *cloud.Client {
	t.Helper()
	server := newCannedServer(t, routes)
	opts = append([]cloud.Option{
		cloud.WithAuthBaseURL(server.URL),
		cloud.WithStorageHost(server.URL),
		cloud.WithSaveOnRenew(false),
		cloud.WithConfig(&cfg),
	}, opts...)
	client, err := cloud.NewClient(opts...)
	if err != nil {
		t.Fatalf("failed creating client: %v", err)
	}
	return client
}

// closedServerURL returns the URL of a local server that no longer accepts connections.
func closedServerURL(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	return server.URL
}

var classificationSentinels = []error{cloud.ErrUnauthorized, cloud.ErrItemNotFound, cloud.ErrGenerationConflict, cloud.ErrDiscoveryFailed}

const (
	rootPath  = "/sync/v3/root"
	renewPath = "/token/json/2/user/new"
	pairPath  = "/token/json/2/device/new"
	blobPath  = "/sync/v3/files/blob-hash"
)

func TestStatusErrorClassification(t *testing.T) {
	userToken := cloud.Config{UserToken: "user-token"}
	deviceToken := cloud.Config{DeviceToken: "device-token"}
	tests := []struct {
		name       string
		call       func(*testing.T) error
		wantStatus int
		wantBody   string
		sentinel   error
		wantText   string
	}{
		{
			name: "GetRootState 503",
			call: func(t *testing.T) error {
				c := newCannedClient(t, map[string]cannedResponse{rootPath: {http.StatusServiceUnavailable, "maintenance"}}, userToken)
				_, err := c.GetRootState(context.Background())
				return err
			},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "maintenance",
			wantText:   "get root state failed with status 503: maintenance",
		},
		{
			name: "GetRootState 401 without renewal",
			call: func(t *testing.T) error {
				c := newCannedClient(t, map[string]cannedResponse{rootPath: {http.StatusUnauthorized, "token expired"}}, userToken, cloud.WithAutoRenew(false))
				_, err := c.GetRootState(context.Background())
				return err
			},
			wantStatus: http.StatusUnauthorized,
			wantBody:   "token expired",
			sentinel:   cloud.ErrUnauthorized,
			wantText:   "unauthorized: missing or invalid credentials: get root state failed with status 401: token expired",
		},
		{
			name: "GetRootState 403 after successful renewal",
			call: func(t *testing.T) error {
				c := newCannedClient(t, map[string]cannedResponse{
					rootPath:  {http.StatusForbidden, "forbidden"},
					renewPath: {http.StatusOK, "renewed-user-token"},
				}, cloud.Config{DeviceToken: "device-token", UserToken: "stale-user-token"})
				_, err := c.GetRootState(context.Background())
				return err
			},
			wantStatus: http.StatusForbidden,
			wantBody:   "forbidden",
			sentinel:   cloud.ErrUnauthorized,
			wantText:   "unauthorized: missing or invalid credentials: get root state failed with status 403: forbidden",
		},
		{
			name: "GetBlob 500",
			call: func(t *testing.T) error {
				c := newCannedClient(t, map[string]cannedResponse{blobPath: {http.StatusInternalServerError, "storage failure"}}, userToken)
				_, err := c.GetBlob(context.Background(), "blob-hash", "doc.pdf")
				return err
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "storage failure",
			wantText:   "get blob blob-hash (doc.pdf) failed with status 500: storage failure",
		},
		{
			name: "GetBlobFresh 404",
			call: func(t *testing.T) error {
				c := newCannedClient(t, map[string]cannedResponse{blobPath: {http.StatusNotFound, "no such object"}}, userToken)
				_, err := c.GetBlobFresh(context.Background(), "blob-hash", "doc.pdf")
				return err
			},
			wantStatus: http.StatusNotFound,
			wantBody:   "no such object",
			sentinel:   cloud.ErrItemNotFound,
			wantText:   "item not found: get blob blob-hash (doc.pdf) failed with status 404: no such object",
		},
		{
			name: "GetManifest 403 without renewal",
			call: func(t *testing.T) error {
				c := newCannedClient(t, map[string]cannedResponse{blobPath: {http.StatusForbidden, "denied"}}, userToken, cloud.WithAutoRenew(false))
				_, err := c.GetManifest(context.Background(), "blob-hash", "root")
				return err
			},
			wantStatus: http.StatusForbidden,
			wantBody:   "denied",
			sentinel:   cloud.ErrUnauthorized,
			wantText:   "unauthorized: missing or invalid credentials: get blob blob-hash (root.docSchema) failed with status 403: denied",
		},
		{
			name: "RenewToken 500",
			call: func(t *testing.T) error {
				c := newCannedClient(t, map[string]cannedResponse{renewPath: {http.StatusInternalServerError, "boom"}}, deviceToken)
				_, err := c.RenewToken(context.Background())
				return err
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "boom",
			wantText:   "renew user token failed with status 500: boom",
		},
		{
			name: "RenewToken 401",
			call: func(t *testing.T) error {
				c := newCannedClient(t, map[string]cannedResponse{renewPath: {http.StatusUnauthorized, "invalid Authorization header"}}, deviceToken)
				_, err := c.RenewToken(context.Background())
				return err
			},
			wantStatus: http.StatusUnauthorized,
			wantBody:   "invalid Authorization header",
			sentinel:   cloud.ErrUnauthorized,
			wantText:   "unauthorized: missing or invalid credentials: renew user token failed with status 401: invalid Authorization header",
		},
		{
			name: "GetRootState minting a user token against 502",
			call: func(t *testing.T) error {
				c := newCannedClient(t, map[string]cannedResponse{renewPath: {http.StatusBadGateway, "gateway"}}, deviceToken)
				_, err := c.GetRootState(context.Background())
				return err
			},
			wantStatus: http.StatusBadGateway,
			wantBody:   "gateway",
			wantText:   "renew user token failed with status 502: gateway",
		},
		{
			name: "PairDevice 500",
			call: func(t *testing.T) error {
				c := newCannedClient(t, map[string]cannedResponse{pairPath: {http.StatusInternalServerError, "pairing down"}}, cloud.Config{})
				_, err := c.PairDevice(context.Background(), "abc12345")
				return err
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "pairing down",
			wantText:   "pair device failed with status 500: pairing down",
		},
		{
			name: "PairDevice 403",
			call: func(t *testing.T) error {
				c := newCannedClient(t, map[string]cannedResponse{pairPath: {http.StatusForbidden, "code expired"}}, cloud.Config{})
				_, err := c.PairDevice(context.Background(), "abc12345")
				return err
			},
			wantStatus: http.StatusForbidden,
			wantBody:   "code expired",
			sentinel:   cloud.ErrUnauthorized,
			wantText:   "unauthorized: missing or invalid credentials: pair device failed with status 403: code expired",
		},
		{
			name:       "CreateDocument upload 502",
			call:       createDocumentFailure("document-upload"),
			wantStatus: http.StatusBadGateway,
			wantBody:   "upload unavailable",
			wantText:   "staging document manifest: upload created.docSchema failed with status 502: upload unavailable",
		},
		{
			name:       "CreateDocument commit 503",
			call:       createDocumentFailure("commit-error"),
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "commit unavailable",
			wantText:   "commit root failed with status 503: commit unavailable",
		},
		{
			name:       "CreateDocument commit 412",
			call:       createDocumentFailure("conflict"),
			wantStatus: http.StatusPreconditionFailed,
			wantBody:   "generation changed",
			sentinel:   cloud.ErrGenerationConflict,
			wantText:   "cloud root generation conflict: commit root failed with status 412: generation changed",
		},
		{
			name:       "CreateDocument commit 409",
			call:       createDocumentFailure("conflict-409"),
			wantStatus: http.StatusConflict,
			wantBody:   "generation changed",
			sentinel:   cloud.ErrGenerationConflict,
			wantText:   "cloud root generation conflict: commit root failed with status 409: generation changed",
		},
		{
			name: "DiscoverEndpoints 503",
			call: func(t *testing.T) error {
				server := newCannedServer(t, map[string]cannedResponse{"/discovery": {http.StatusServiceUnavailable, "discovery down"}})
				_, err := cloud.DiscoverEndpoints(context.Background(), server.Client(), server.URL+"/discovery")
				return err
			},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "discovery down",
			sentinel:   cloud.ErrDiscoveryFailed,
			wantText:   "service discovery failed: discover endpoints failed with status 503: discovery down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(t)
			var statusErr *cloud.StatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("expected *cloud.StatusError, got %T: %v", err, err)
			}
			if statusErr.StatusCode != tt.wantStatus || statusErr.Body != tt.wantBody {
				t.Fatalf("status error = (%d, %q), want (%d, %q)", statusErr.StatusCode, statusErr.Body, tt.wantStatus, tt.wantBody)
			}
			for _, sentinel := range classificationSentinels {
				if got, want := errors.Is(err, sentinel), sentinel == tt.sentinel; got != want {
					t.Fatalf("errors.Is(err, %q) = %v, want %v (err: %v)", sentinel, got, want, err)
				}
			}
			if err.Error() != tt.wantText {
				t.Fatalf("error text mismatch\n got: %q\nwant: %q", err.Error(), tt.wantText)
			}
		})
	}
}

// createDocumentFailure runs CreateDocument against a creation server configured with failure.
func createDocumentFailure(failure string) func(*testing.T) error {
	return func(t *testing.T) error {
		s, c := newCreationServer(t, failure)
		_, err := c.CreateDocument(context.Background(), cloud.CreateDocumentOptions{ID: "created", ExpectedRoot: s.initial, Files: creationFiles()})
		return err
	}
}

func TestRenewTokenKeepsNonAuthFailuresUnclassified(t *testing.T) {
	tests := []struct {
		name  string
		call  func(*testing.T) error
		check func(*testing.T, error)
	}{
		{
			name: "auth service unreachable",
			call: func(t *testing.T) error {
				c := newClosedAuthClient(t)
				_, err := c.RenewToken(context.Background())
				return err
			},
			check: requireTransportError,
		},
		{
			name: "GetRootState renewing a stale user token from an unreachable auth service",
			call: func(t *testing.T) error {
				storage := newCannedServer(t, map[string]cannedResponse{rootPath: {http.StatusUnauthorized, "token expired"}})
				c, err := cloud.NewClient(
					cloud.WithAuthBaseURL(closedServerURL(t)),
					cloud.WithStorageHost(storage.URL),
					cloud.WithSaveOnRenew(false),
					cloud.WithConfig(&cloud.Config{DeviceToken: "device-token", UserToken: "stale-user-token"}),
				)
				if err != nil {
					t.Fatal(err)
				}
				_, err = c.GetRootState(context.Background())
				return err
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if !strings.HasPrefix(err.Error(), "auth renewal failed after 401: perform renew request: ") {
					t.Fatalf("expected renewal failure after the storage 401, got %v", err)
				}
				requireTransportError(t, err)
			},
		},
		{
			name: "GetRootState minting a user token from an unreachable auth service",
			call: func(t *testing.T) error {
				c := newClosedAuthClient(t)
				_, err := c.GetRootState(context.Background())
				return err
			},
			check: requireTransportError,
		},
		{
			name: "caller canceled renewal",
			call: func(t *testing.T) error {
				c := newCannedClient(t, map[string]cannedResponse{}, cloud.Config{DeviceToken: "device-token"})
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				_, err := c.RenewToken(ctx)
				return err
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected context.Canceled, got %v", err)
				}
			},
		},
		{
			name: "renewal deadline exceeded",
			call: func(t *testing.T) error {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					<-r.Context().Done()
				}))
				t.Cleanup(server.Close)
				c, err := cloud.NewClient(cloud.WithAuthBaseURL(server.URL), cloud.WithSaveOnRenew(false), cloud.WithConfig(&cloud.Config{DeviceToken: "device-token"}))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
				_, err = c.RenewToken(ctx)
				return err
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected context.DeadlineExceeded, got %v", err)
				}
			},
		},
		{
			name: "empty user token",
			call: func(t *testing.T) error {
				c := newCannedClient(t, map[string]cannedResponse{renewPath: {http.StatusOK, " "}}, cloud.Config{DeviceToken: "device-token"})
				_, err := c.RenewToken(context.Background())
				return err
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if err == nil || err.Error() != "empty user token received from server" {
					t.Fatalf("expected empty token error, got %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(t)
			if err == nil {
				t.Fatal("expected renewal failure")
			}
			if errors.Is(err, cloud.ErrUnauthorized) {
				t.Fatalf("non-auth renewal failure classified as unauthorized: %v", err)
			}
			var statusErr *cloud.StatusError
			if errors.As(err, &statusErr) {
				t.Fatalf("renewal without an HTTP status carries a status error: %v", err)
			}
			tt.check(t, err)
		})
	}
}

// newClosedAuthClient builds a client whose auth and storage hosts refuse connections.
func newClosedAuthClient(t *testing.T) *cloud.Client {
	t.Helper()
	closed := closedServerURL(t)
	c, err := cloud.NewClient(
		cloud.WithAuthBaseURL(closed),
		cloud.WithStorageHost(closed),
		cloud.WithSaveOnRenew(false),
		cloud.WithConfig(&cloud.Config{DeviceToken: "device-token"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func requireTransportError(t *testing.T, err error) {
	t.Helper()
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("expected *url.Error, got %T: %v", err, err)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) {
		t.Fatalf("expected net.Error, got %T: %v", err, err)
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "dial" {
		t.Fatalf("expected dial *net.OpError, got %v", err)
	}
}
